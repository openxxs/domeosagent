package monitor

import (
	"fmt"
	"log"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"domeagent/monitor/container"
	"domeagent/monitor/container/docker"
	"domeagent/monitor/info"
	"domeagent/monitor/storage"
	"domeagent/monitor/utils/cpuload"
)

var cgroupPathRegExp = regexp.MustCompile(".*devices.*:(.*?)[,;$].*")

type containerInfo struct {
	info.ContainerReference
	Subcontainers []info.ContainerReference
	Spec          info.ContainerSpec
}

type containerData struct {
	handler              container.ContainerHandler
	info                 containerInfo
	lock                 sync.Mutex
	loadReader           cpuload.CpuLoadReader
	loadDecay            float64
	loadAvg              float64
	housekeepingInterval time.Duration
	lastUpdatedTime      time.Time
	lastErrorTime        time.Time
	stop                 chan bool
	backendStorage       storage.StorageDriver
}

func (c *containerData) Start() error {
	// Only do housekeeping for root or docker containers
	if c.info.Name == "/" || c.info.Namespace == docker.DockerNamespace {
		go c.housekeeping()
	}
	return nil
}

func (c *containerData) Stop() error {
	c.stop <- true
	return nil
}

func (c *containerData) allowErrorLogging() bool {
	if time.Since(c.lastErrorTime) > time.Minute {
		c.lastErrorTime = time.Now()
		return true
	}
	return false
}

func (c *containerData) GetInfo() (*containerInfo, error) {
	// Get spec and subcontainers.
	if time.Since(c.lastUpdatedTime) > 5*time.Second {
		err := c.updateSpec()
		if err != nil {
			return nil, err
		}
		err = c.updateSubcontainers()
		if err != nil {
			return nil, err
		}
		c.lastUpdatedTime = time.Now()
	}
	// Make a copy of the info for the user.
	c.lock.Lock()
	defer c.lock.Unlock()
	return &c.info, nil
}

// Calculate new smoothed load average using the new sample of runnable threads.
// The decay used ensures that the load will stabilize on a new constant value within
// 10 seconds.
func (c *containerData) updateLoad(newLoad uint64) {
	if c.loadAvg < 0 {
		c.loadAvg = float64(newLoad) // initialize to the first seen sample for faster stabilization.
	} else {
		c.loadAvg = c.loadAvg*c.loadDecay + float64(newLoad)*(1.0-c.loadDecay)
	}
}

func (c *containerData) updateSubcontainers() error {
	var subcontainers info.ContainerReferenceSlice
	subcontainers, err := c.handler.ListContainers(container.ListSelf)
	if err != nil {
		// Ignore errors if the container is dead.
		if !c.handler.Exists() {
			return nil
		}
		return err
	}
	sort.Sort(subcontainers)
	c.lock.Lock()
	defer c.lock.Unlock()
	c.info.Subcontainers = subcontainers
	return nil
}

func (c *containerData) updateStats(doStorage bool) (*info.ContainerStats, error) {
	stats, statsErr := c.handler.GetStats()
	if statsErr != nil {
		// Ignore errors if the container is dead.
		if !c.handler.Exists() {
			return nil, nil
		}

		// Stats may be partially populated, push those before we return an error.
		statsErr = fmt.Errorf("%v, continuing to push stats", statsErr)
	}
	if stats == nil {
		return nil, statsErr
	}
	if c.loadReader != nil {
		path, err := c.handler.GetCgroupPath("cpu")
		if err == nil {
			loadStats, err := c.loadReader.GetCpuLoad(c.info.Name, path)
			if err != nil {
				return nil, fmt.Errorf("failed to get load stat for %q - path %q, error %s", c.info.Name, path, err)
			}
			stats.TaskStats = loadStats
			c.updateLoad(loadStats.NrRunning)
			stats.Cpu.LoadAverage = int32(c.loadAvg * 1000)
		}
	}
	if doStorage {
		ref, err := c.handler.ContainerReference()
		if err != nil {
			// Ignore errors if the container is dead.
			if !c.handler.Exists() {
				return nil, nil
			}
			return nil, err
		}
		err = c.backendStorage.AddStats(ref, stats)
		if err != nil {
			return stats, err
		}
	}
	if statsErr != nil {
		return nil, statsErr
	}
	return stats, nil
}

func (c *containerData) housekeepingTick() {
	_, err := c.updateStats(true)
	if err != nil {
		if c.allowErrorLogging() {
			log.Printf("[Error] Failed to update stats for container \"%s\": %s", c.info.Name, err)
		}
	}
}

// Determine when the next housekeeping should occur.
func (self *containerData) nextHousekeeping(lastHousekeeping time.Time) time.Time {
	return lastHousekeeping.Add(self.housekeepingInterval)
}

func (c *containerData) housekeeping() {
	// Long housekeeping is either 100ms or half of the housekeeping interval.
	longHousekeeping := 100 * time.Millisecond
	if c.housekeepingInterval/2 < longHousekeeping {
		longHousekeeping = c.housekeepingInterval / 2
	}

	// Housekeep every housekeepingInterval.
	remainder := time.Now().Unix() % int64(c.housekeepingInterval.Seconds())
	var wait int64
	if remainder > 0 {
		wait = int64(c.housekeepingInterval.Seconds()) - remainder
	} else {
		wait = 0
	}
	time.Sleep(time.Duration(wait * int64(time.Second)))
	lastHousekeeping := time.Now()
	for {
		select {
		case <-c.stop:
			// Stop housekeeping when signaled.
			return
		default:
			// Perform housekeeping.
			start := time.Now()
			//if c.info.Namespace == docker.DockerNamespace {
			//	log.Printf("[Info] Do housekeeping for container %q\n", c.info.Aliases[0])
			//} else {
			//	log.Printf("[Info] Do housekeeping for container %q\n", c.info.Name)
			//}
			c.housekeepingTick()
			// Check if housekeeping took too long.
			duration := time.Since(start)
			if duration >= longHousekeeping {
				//log.Printf("[%s] Housekeeping took %s", c.info.Name, duration)
			}
		}
		next := c.nextHousekeeping(lastHousekeeping)

		// Schedule the next housekeeping. Sleep until that time.
		if time.Now().Before(next) {
			time.Sleep(next.Sub(time.Now()))
		} else {
			next = time.Now()
		}
		lastHousekeeping = next
	}
}

func (c *containerData) updateSpec() error {
	spec, err := c.handler.GetSpec()
	if err != nil {
		// Ignore errors if the container is dead.
		if !c.handler.Exists() {
			return nil
		}
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	c.info.Spec = spec
	return nil
}

func newContainerData(containerName string, storage storage.StorageDriver, handler container.ContainerHandler, loadReader cpuload.CpuLoadReader, housekeepingInterval time.Duration) (*containerData, error) {

	if handler == nil {
		return nil, fmt.Errorf("nil container handler")
	}

	ref, err := handler.ContainerReference()
	if err != nil {
		return nil, err
	}

	cont := &containerData{
		handler:              handler,
		housekeepingInterval: housekeepingInterval,
		loadReader:           loadReader,
		loadDecay:            math.Exp(float64(-1 * housekeepingInterval.Seconds() / 10)),
		loadAvg:              -1.0,
		stop:                 make(chan bool, 1),
		backendStorage:       storage,
	}
	cont.info.ContainerReference = ref

	err = cont.updateSpec()
	if err != nil {
		return nil, err
	}

	return cont, nil
}

func (c *containerData) getCgroupPath(cgroups string) (string, error) {
	if cgroups == "-" {
		return "/", nil
	}
	matches := cgroupPathRegExp.FindSubmatch([]byte(cgroups))
	if len(matches) != 2 {
		log.Printf("[Error] Failed to get devices cgroup path from %q", cgroups)
		// return root in case of failures - devices hierarchy might not be enabled.
		return "/", nil
	}
	return string(matches[1]), nil
}

// Return output for ps command in host /proc with specified format
func (c *containerData) getPsOutput(inHostNamespace bool, format string) ([]byte, error) {
	args := []string{}
	command := "ps"
	if !inHostNamespace {
		command = "/usr/sbin/chroot"
		args = append(args, "/rootfs", "ps")
	}
	args = append(args, "-e", "-o", format)
	out, err := exec.Command(command, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("failed to execute %q command: %v", command, err)
	}
	return out, err
}

// Get pids of processes in this container.
// A slightly lighterweight call than GetProcessList if other details are not required.
func (c *containerData) getContainerPids(inHostNamespace bool) ([]string, error) {
	format := "pid,cgroup"
	out, err := c.getPsOutput(inHostNamespace, format)
	if err != nil {
		return nil, err
	}
	expectedFields := 2
	lines := strings.Split(string(out), "\n")
	pids := []string{}
	for _, line := range lines[1:] {
		if len(line) == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < expectedFields {
			return nil, fmt.Errorf("expected at least %d fields, found %d: output: %q", expectedFields, len(fields), line)
		}
		pid := fields[0]
		cgroup, err := c.getCgroupPath(fields[1])
		if err != nil {
			return nil, fmt.Errorf("could not parse cgroup path from %q: %v", fields[1], err)
		}
		if c.info.Name == cgroup {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func (c *containerData) GetProcessList(Container string, inHostNamespace bool) ([]info.ProcessInfo, error) {
	// report all processes for root.
	isRoot := c.info.Name == "/"
	format := "user,pid,ppid,stime,pcpu,pmem,rss,vsz,stat,time,comm,cgroup"
	out, err := c.getPsOutput(inHostNamespace, format)
	if err != nil {
		return nil, err
	}
	expectedFields := 12
	processes := []info.ProcessInfo{}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines[1:] {
		if len(line) == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < expectedFields {
			return nil, fmt.Errorf("expected at least %d fields, found %d: output: %q", expectedFields, len(fields), line)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("invalid pid %q: %v", fields[1], err)
		}
		ppid, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("invalid ppid %q: %v", fields[2], err)
		}
		percentCpu, err := strconv.ParseFloat(fields[4], 32)
		if err != nil {
			return nil, fmt.Errorf("invalid cpu percent %q: %v", fields[4], err)
		}
		percentMem, err := strconv.ParseFloat(fields[5], 32)
		if err != nil {
			return nil, fmt.Errorf("invalid memory percent %q: %v", fields[5], err)
		}
		rss, err := strconv.ParseUint(fields[6], 0, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid rss %q: %v", fields[6], err)
		}
		// convert to bytes
		rss *= 1024
		vs, err := strconv.ParseUint(fields[7], 0, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid virtual size %q: %v", fields[7], err)
		}
		// convert to bytes
		vs *= 1024
		cgroup, err := c.getCgroupPath(fields[11])
		if err != nil {
			return nil, fmt.Errorf("could not parse cgroup path from %q: %v", fields[11], err)
		}

		var cgroupPath string
		if isRoot {
			cgroupPath = cgroup
		}

		if isRoot || c.info.Name == cgroup {
			processes = append(processes, info.ProcessInfo{
				User:          fields[0],
				Pid:           pid,
				Ppid:          ppid,
				StartTime:     fields[3],
				PercentCpu:    float32(percentCpu),
				PercentMemory: float32(percentMem),
				RSS:           rss,
				VirtualSize:   vs,
				Status:        fields[8],
				RunningTime:   fields[9],
				Cmd:           fields[10],
				CgroupPath:    cgroupPath,
			})
		}
	}
	return processes, nil
}
