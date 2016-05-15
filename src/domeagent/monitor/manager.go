package monitor

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"domeagent/monitor/container"
	"domeagent/monitor/container/docker"
	"domeagent/monitor/container/raw"
	"domeagent/monitor/events"
	"domeagent/monitor/fs"
	"domeagent/monitor/info"
	"domeagent/monitor/storage"
	"domeagent/monitor/storage/influxdb"
	"domeagent/monitor/utils/cpuload"
	"domeagent/monitor/utils/oomparser"
	"domeagent/monitor/utils/sysfs"

	"github.com/docker/libcontainer/cgroups"
)

var globalHousekeepingInterval = 1 * time.Minute

// change enableLoadReader from true to false, to avoid "error failed to open cgroup path" error
var enableLoadReader = false

// The Manager interface defines operations for starting a manager and getting
// container and machine information & uploading to influxdb
type Manager interface {
	// Start the manager. Calling other manager methods before this returns
	// may produce undefined behavior.
	Start() error

	// Stops the manager.
	Stop() error

	// Get information about a container.
	GetContainerInfo(containerName string) (*info.ContainerInfo, error)

	// Gets all the Docker containers. Return is a map from full container name to ContainerInfo.
	AllDockerContainers() (map[string]*info.ContainerInfo, error)

	// Gets information about a specific Docker container. The specified name is within the Docker namespace.
	DockerContainer(containerName string) (*info.ContainerInfo, error)

	// Returns true if the named container exists.
	Exists(containerName string) bool

	// Get information about the machine.
	GetMachineInfo() (*info.MachineInfo, error)

	// Get version information about different components we depend on.
	GetVersionInfo() (*info.VersionInfo, error)

	// Get events streamed through passedChannel that fit the request.
	WatchForEvents(request *events.Request) (*events.EventChannel, error)

	// Get past events that have been detected and that fit the request.
	GetPastEvents(request *events.Request) ([]*info.Event, error)

	CloseEventChannel(watch_id int)

	// Get status information about docker.
	DockerInfo() (DockerStatus, error)

	// Get details about interesting docker images.
	DockerImages() ([]DockerImage, error)
}

type DockerStatus struct {
	Version       string            `json:"version"`
	KernelVersion string            `json:"kernel_version"`
	OS            string            `json:"os"`
	Hostname      string            `json:"hostname"`
	RootDir       string            `json:"root_dir"`
	Driver        string            `json:"driver"`
	DriverStatus  map[string]string `json:"driver_status"`
	ExecDriver    string            `json:"exec_driver"`
	NumImages     int               `json:"num_images"`
	NumContainers int               `json:"num_containers"`
}

type DockerImage struct {
	ID          string   `json:"id"`
	RepoTags    []string `json:"repo_tags"`
	Created     int64    `json:"created"`
	VirtualSize int64    `json:"virtual_size"`
	Size        int64    `json:"size"`
}

type InfluxConfig struct {
	Table          string        `json:"table,omitempty"`
	Database       string        `json:"dababase,omitempty"`
	Username       string        `json:"username,omitempty"`
	Password       string        `json:"password,omitempty"`
	Host           string        `json:"host,omitempty"`
	BufferDuration time.Duration `json:"buffer_duration,omitempty"`
	FilterPrefix   string        `json:"filter_prefix,omitempty"`
}

// A namespaced container name.
type namespacedContainerName struct {
	// The namespace of the container. Can be empty for the root namespace.
	Namespace string

	// The name of the container in this namespace.
	Name string
}

type manager struct {
	containers           map[namespacedContainerName]*containerData
	containersLock       sync.RWMutex
	backendStorage       storage.StorageDriver
	fsInfo               fs.FsInfo
	machineInfo          info.MachineInfo
	versionInfo          info.VersionInfo
	quitChannels         []chan error
	selfContainer        string
	loadReader           cpuload.CpuLoadReader
	eventHandler         events.EventManager
	startupTime          time.Time
	housekeepingInterval time.Duration
	inHostNamespace      bool
}

// New returns a new manager.
func New(housekeepingInterval time.Duration, config *InfluxConfig) (Manager, error) {

	// Initialize influxdb
	hostname, err := os.Hostname() // Agent's host name
	if err != nil {
		return nil, err
	}
	influxdbStorage, err := influxdb.New(hostname,
		config.Table,
		config.Database,
		config.Username,
		config.Password,
		config.Host,
		config.BufferDuration,
		config.FilterPrefix)
	if err != nil {
		return nil, err
	}
	//log.Printf("[Info] Connected to influxdb on: %q", config.Host)

	sysfs, err := sysfs.NewRealSysFs()
	if err != nil {
		log.Printf("[Error] Failed to create a system interface: %s", err)
		return nil, err
	}
	//log.Printf("[Info] Created a system interface)

	// Detect the container we are running on.
	selfContainer, err := cgroups.GetThisCgroupDir("cpu")
	if err != nil {
		return nil, err
	}
	//log.Printf("[Info] Running in container: %q", selfContainer)

	dockerInfo, err := docker.DockerInfo()
	if err != nil {
		log.Printf("[Error] Unable to connect to Docker: %v", err)
	}

	context := fs.Context{DockerRoot: docker.RootDir(), DockerInfo: dockerInfo}
	fsInfo, err := fs.NewFsInfo(context)
	if err != nil {
		return nil, err
	}

	// If started with host's rootfs mounted, assume that its running
	// in its own namespaces.
	inHostNamespace := false
	if _, err := os.Stat("/rootfs/proc"); os.IsNotExist(err) {
		inHostNamespace = true
	}

	newManager := &manager{
		containers:           make(map[namespacedContainerName]*containerData),
		backendStorage:       influxdbStorage,
		quitChannels:         make([]chan error, 0, 2),
		fsInfo:               fsInfo,
		selfContainer:        selfContainer,
		inHostNamespace:      inHostNamespace,
		startupTime:          time.Now(),
		housekeepingInterval: housekeepingInterval,
	}

	machineInfo, err := getMachineInfo(sysfs, fsInfo)
	if err != nil {
		return nil, err
	}
	newManager.machineInfo = *machineInfo
	//log.Printf("[Info] Machine: %+v", newManager.machineInfo)

	versionInfo, err := getVersionInfo()
	if err != nil {
		return nil, err
	}
	newManager.versionInfo = *versionInfo
	//log.Printf("[Info] Version: %+v", newManager.versionInfo)

	newManager.eventHandler = events.NewEventManager(events.DefaultStoragePolicy())
	return newManager, nil
}

// Start the container manager.
func (self *manager) Start() error {
	// Register Docker container factory.
	err := docker.Register(self, self.fsInfo)
	if err != nil {
		log.Printf("{Error] Docker container factory registration failed: %v.", err)
		return err
	}

	// Register the raw driver.
	err = raw.Register(self, self.fsInfo)
	if err != nil {
		log.Printf("[Error] Registration of the raw container factory failed: %v", err)
		return err
	}

	self.DockerInfo()
	self.DockerImages()

	if enableLoadReader {
		// Create cpu load reader.
		cpuLoadReader, err := cpuload.New()
		if err != nil {
			log.Printf("[Error] Could not initialize cpu load reader: %s", err)
		} else {
			err = cpuLoadReader.Start()
			if err != nil {
				log.Printf("[Error] Could not start cpu load stat collector: %s", err)
			} else {
				self.loadReader = cpuLoadReader
			}
		}
	}

	// Watch for OOMs.
	err = self.watchForNewOoms()
	if err != nil {
		log.Printf("[Error] Could not configure a source for OOM detection, disabling OOM events: %v", err)
	}

	// If there are no factories, don't start any housekeeping and serve the information we do have.
	if !container.HasFactories() {
		return nil
	}

	// Create root and then recover all containers.
	err = self.createContainer("/")
	if err != nil {
		return err
	}
	//log.Printf("[Info] Starting recovery of all containers")

	err = self.detectSubcontainers("/")
	if err != nil {
		return err
	}
	//log.Printf("[Info] Recovery completed")

	// Watch for new container.
	quitWatcher := make(chan error)
	err = self.watchForNewContainers(quitWatcher)
	if err != nil {
		return err
	}
	self.quitChannels = append(self.quitChannels, quitWatcher)

	// Look for new containers in the main housekeeping thread.
	quitGlobalHousekeeping := make(chan error)
	self.quitChannels = append(self.quitChannels, quitGlobalHousekeeping)
	go self.globalHousekeeping(quitGlobalHousekeeping)

	return nil
}

func (self *manager) Stop() error {
	// Stop and wait on all quit channels.
	for i, c := range self.quitChannels {
		// Send the exit signal and wait on the thread to exit (by closing the channel).
		c <- nil
		err := <-c
		if err != nil {
			// Remove the channels that quit successfully.
			self.quitChannels = self.quitChannels[i:]
			return err
		}
	}
	self.quitChannels = make([]chan error, 0, 2)
	if self.loadReader != nil {
		self.loadReader.Stop()
		self.loadReader = nil
	}
	return nil
}

// Get a container by name.
func (self *manager) GetContainerInfo(containerName string) (*info.ContainerInfo, error) {
	cont, err := self.getContainerData(containerName)
	if err != nil {
		return nil, err
	}
	return self.containerDataToContainerInfo(cont)
}

func (self *manager) getContainerData(containerName string) (*containerData, error) {
	var cont *containerData
	var ok bool
	func() {
		self.containersLock.RLock()
		defer self.containersLock.RUnlock()

		// Ensure we have the container.
		cont, ok = self.containers[namespacedContainerName{
			Name: containerName,
		}]
	}()
	if !ok {
		return nil, fmt.Errorf("unknown container %q", containerName)
	}
	return cont, nil
}

func (self *manager) containerDataToContainerInfo(cont *containerData) (*info.ContainerInfo, error) {
	// Get the info from the container.
	cinfo, err := cont.GetInfo()
	if err != nil {
		return nil, err
	}

	stats, err := cont.updateStats(false)
	if err != nil {
		return nil, err
	}

	// Make a copy of the info for the user.
	ret := &info.ContainerInfo{
		ContainerReference: cinfo.ContainerReference,
		Subcontainers:      cinfo.Subcontainers,
		Spec:               self.getAdjustedSpec(cinfo),
		Stats:              stats,
	}
	return ret, nil
}

func (self *manager) getAdjustedSpec(cinfo *containerInfo) info.ContainerSpec {
	spec := cinfo.Spec

	// Set default value to an actual value
	if spec.HasMemory {
		// Memory.Limit is 0 means there's no limit
		if spec.Memory.Limit == 0 {
			spec.Memory.Limit = uint64(self.machineInfo.MemoryCapacity)
		}
	}
	return spec
}

func (self *manager) AllDockerContainers() (map[string]*info.ContainerInfo, error) {
	containers := self.getAllDockerContainers()

	output := make(map[string]*info.ContainerInfo, len(containers))
	for name, cont := range containers {
		inf, err := self.containerDataToContainerInfo(cont)
		if err != nil {
			return nil, err
		}
		output[name] = inf
	}
	return output, nil
}

func (self *manager) getAllDockerContainers() map[string]*containerData {
	self.containersLock.RLock()
	defer self.containersLock.RUnlock()
	containers := make(map[string]*containerData, len(self.containers))

	// Get containers in the Docker namespace.
	for name, cont := range self.containers {
		if name.Namespace == docker.DockerNamespace {
			containers[cont.info.Name] = cont
		}
	}
	return containers
}

func (self *manager) DockerContainer(containerName string) (*info.ContainerInfo, error) {
	container, err := self.getDockerContainer(containerName)
	if err != nil {
		return &info.ContainerInfo{}, err
	}

	inf, err := self.containerDataToContainerInfo(container)
	if err != nil {
		return &info.ContainerInfo{}, err
	}
	return inf, nil
}

func (self *manager) getDockerContainer(containerName string) (*containerData, error) {
	self.containersLock.RLock()
	defer self.containersLock.RUnlock()

	// Check for the container in the Docker container namespace.
	cont, ok := self.containers[namespacedContainerName{
		Namespace: docker.DockerNamespace,
		Name:      containerName,
	}]
	if !ok {
		return nil, fmt.Errorf("unable to find Docker container %q", containerName)
	}
	return cont, nil
}

func (m *manager) Exists(containerName string) bool {
	m.containersLock.Lock()
	defer m.containersLock.Unlock()

	namespacedName := namespacedContainerName{
		Name: containerName,
	}

	_, ok := m.containers[namespacedName]
	if ok {
		return true
	}
	return false
}

func (m *manager) GetMachineInfo() (*info.MachineInfo, error) {
	// Copy and return the MachineInfo.
	return &m.machineInfo, nil
}

func (m *manager) GetVersionInfo() (*info.VersionInfo, error) {
	return &m.versionInfo, nil
}

// can be called by the api which will take events returned on the channel
func (self *manager) WatchForEvents(request *events.Request) (*events.EventChannel, error) {
	return self.eventHandler.WatchEvents(request)
}

// can be called by the api which will return all events satisfying the request
func (self *manager) GetPastEvents(request *events.Request) ([]*info.Event, error) {
	return self.eventHandler.GetEvents(request)
}

// called by the api when a client is no longer listening to the channel
func (self *manager) CloseEventChannel(watch_id int) {
	self.eventHandler.StopWatch(watch_id)
}

func (m *manager) DockerInfo() (DockerStatus, error) {
	info, err := docker.DockerInfo()
	if err != nil {
		return DockerStatus{}, err
	}
	out := DockerStatus{}
	out.Version = m.versionInfo.DockerVersion
	if val, ok := info["KernelVersion"]; ok {
		out.KernelVersion = val
	}
	if val, ok := info["OperatingSystem"]; ok {
		out.OS = val
	}
	if val, ok := info["Name"]; ok {
		out.Hostname = val
	}
	if val, ok := info["DockerRootDir"]; ok {
		out.RootDir = val
	}
	if val, ok := info["Driver"]; ok {
		out.Driver = val
	}
	if val, ok := info["ExecutionDriver"]; ok {
		out.ExecDriver = val
	}
	if val, ok := info["Images"]; ok {
		n, err := strconv.Atoi(val)
		if err == nil {
			out.NumImages = n
		}
	}
	if val, ok := info["Containers"]; ok {
		n, err := strconv.Atoi(val)
		if err == nil {
			out.NumContainers = n
		}
	}
	// cut, trim, cut - Example format:
	// DriverStatus=[["Root Dir","/var/lib/docker/aufs"],["Backing Filesystem","extfs"],["Dirperm1 Supported","false"]]
	if val, ok := info["DriverStatus"]; ok {
		out.DriverStatus = make(map[string]string)
		val = strings.TrimPrefix(val, "[[")
		val = strings.TrimSuffix(val, "]]")
		vals := strings.Split(val, "],[")
		for _, v := range vals {
			kv := strings.Split(v, "\",\"")
			if len(kv) != 2 {
				continue
			} else {
				out.DriverStatus[strings.Trim(kv[0], "\"")] = strings.Trim(kv[1], "\"")
			}
		}
	}
	return out, nil
}

func (m *manager) DockerImages() ([]DockerImage, error) {
	images, err := docker.DockerImages()
	if err != nil {
		return nil, err
	}
	out := []DockerImage{}
	const unknownTag = "<none>:<none>"
	for _, image := range images {
		if len(image.RepoTags) == 1 && image.RepoTags[0] == unknownTag {
			// images with repo or tags are uninteresting.
			continue
		}
		di := DockerImage{
			ID:          image.ID,
			RepoTags:    image.RepoTags,
			Created:     image.Created,
			VirtualSize: image.VirtualSize,
			Size:        image.Size,
		}
		out = append(out, di)
	}
	return out, nil
}

func (self *manager) watchForNewOoms() error {
	//log.Printf("[Info] Started watching for new ooms in manager")
	outStream := make(chan *oomparser.OomInstance, 10)
	oomLog, err := oomparser.New()
	if err != nil {
		return err
	}
	go oomLog.StreamOoms(outStream)

	go func() {
		for oomInstance := range outStream {
			// Surface OOM and OOM kill events.
			newEvent := &info.Event{
				ContainerName: oomInstance.ContainerName,
				Timestamp:     oomInstance.TimeOfDeath,
				EventType:     info.EventOom,
			}
			err := self.eventHandler.AddEvent(newEvent)
			if err != nil {
				log.Printf("[Error] failed to add OOM event for %q: %v", oomInstance.ContainerName, err)
			}
			//log.Printf("[Info] Created an OOM event in container %q at %v", oomInstance.ContainerName, oomInstance.TimeOfDeath)

			newEvent = &info.Event{
				ContainerName: oomInstance.VictimContainerName,
				Timestamp:     oomInstance.TimeOfDeath,
				EventType:     info.EventOomKill,
				EventData: info.EventData{
					OomKill: &info.OomKillEventData{
						Pid:         oomInstance.Pid,
						ProcessName: oomInstance.ProcessName,
					},
				},
			}
			err = self.eventHandler.AddEvent(newEvent)
			if err != nil {
				log.Printf("[Error] failed to add OOM kill event for %q: %v", oomInstance.ContainerName, err)
			}
		}
	}()
	return nil
}

// Create a container.
func (m *manager) createContainer(containerName string) error {
	handler, accept, err := container.NewContainerHandler(containerName, m.inHostNamespace)
	if err != nil {
		return err
	}
	if !accept {
		// ignoring this container.
		log.Printf("[Info] ignoring container %q", containerName)
		return nil
	}

	cont, err := newContainerData(containerName, m.backendStorage, handler, m.loadReader, m.housekeepingInterval)
	if err != nil {
		return err
	}

	// Add to the containers map.
	alreadyExists := func() bool {
		m.containersLock.Lock()
		defer m.containersLock.Unlock()

		namespacedName := namespacedContainerName{
			Name: containerName,
		}

		// Check that the container didn't already exist.
		_, ok := m.containers[namespacedName]
		if ok {
			return true
		}

		// Add the container name and all its aliases. The aliases must be within the namespace of the factory.
		m.containers[namespacedName] = cont
		for _, alias := range cont.info.Aliases {
			m.containers[namespacedContainerName{
				Namespace: cont.info.Namespace,
				Name:      alias,
			}] = cont
		}

		return false
	}()
	if alreadyExists {
		return nil
	}
	//log.Printf("[Info] Added container: %q (aliases: %v, namespace: %q)", containerName, cont.info.Aliases, cont.info.Namespace)

	contSpec, err := cont.handler.GetSpec()
	if err != nil {
		return err
	}

	contRef, err := cont.handler.ContainerReference()
	if err != nil {
		return err
	}

	newEvent := &info.Event{
		ContainerName: contRef.Name,
		Timestamp:     contSpec.CreationTime,
		EventType:     info.EventContainerCreation,
	}
	err = m.eventHandler.AddEvent(newEvent)
	if err != nil {
		return err
	}

	// Start the container's housekeeping.
	cont.Start()

	return nil
}

func (m *manager) destroyContainer(containerName string) error {
	m.containersLock.Lock()
	defer m.containersLock.Unlock()

	namespacedName := namespacedContainerName{
		Name: containerName,
	}
	cont, ok := m.containers[namespacedName]
	if !ok {
		// Already destroyed, done.
		return nil
	}

	// Tell the container to stop.
	err := cont.Stop()
	if err != nil {
		return err
	}

	// Remove the container from our records (and all its aliases).
	delete(m.containers, namespacedName)
	for _, alias := range cont.info.Aliases {
		delete(m.containers, namespacedContainerName{
			Namespace: cont.info.Namespace,
			Name:      alias,
		})
	}
	//log.Printf("[Info] Destroyed container: %q (aliases: %v, namespace: %q)", containerName, cont.info.Aliases, cont.info.Namespace)

	contRef, err := cont.handler.ContainerReference()
	if err != nil {
		return err
	}

	newEvent := &info.Event{
		ContainerName: contRef.Name,
		Timestamp:     time.Now(),
		EventType:     info.EventContainerDeletion,
	}
	err = m.eventHandler.AddEvent(newEvent)
	if err != nil {
		return err
	}
	return nil
}

// Detect all containers that have been added or deleted from the specified container.
func (m *manager) getContainersDiff(containerName string) (added []info.ContainerReference, removed []info.ContainerReference, err error) {
	m.containersLock.RLock()
	defer m.containersLock.RUnlock()

	// Get all subcontainers recursively.
	cont, ok := m.containers[namespacedContainerName{
		Name: containerName,
	}]
	if !ok {
		return nil, nil, fmt.Errorf("failed to find container %q while checking for new containers", containerName)
	}
	allContainers, err := cont.handler.ListContainers(container.ListRecursive)
	if err != nil {
		return nil, nil, err
	}
	allContainers = append(allContainers, info.ContainerReference{Name: containerName})

	// Determine which were added and which were removed.
	allContainersSet := make(map[string]*containerData)
	for name, d := range m.containers {
		// Only add the canonical name.
		if d.info.Name == name.Name {
			allContainersSet[name.Name] = d
		}
	}

	// Added containers
	for _, c := range allContainers {
		delete(allContainersSet, c.Name)
		_, ok := m.containers[namespacedContainerName{
			Name: c.Name,
		}]
		if !ok {
			added = append(added, c)
		}
	}

	// Removed ones are no longer in the container listing.
	for _, d := range allContainersSet {
		removed = append(removed, d.info.ContainerReference)
	}

	return
}

// Detect the existing subcontainers and reflect the setup here.
func (m *manager) detectSubcontainers(containerName string) error {
	added, removed, err := m.getContainersDiff(containerName)
	if err != nil {
		return err
	}

	// Add the new containers.
	for _, cont := range added {
		err = m.createContainer(cont.Name)
		if err != nil {
			log.Printf("[Error] Failed to create existing container: %s: %s", cont.Name, err)
		}
	}

	// Remove the old containers.
	for _, cont := range removed {
		err = m.destroyContainer(cont.Name)
		if err != nil {
			log.Printf("[Error] Failed to destroy existing container: %s: %s", cont.Name, err)
		}
	}

	return nil
}

// Watches for new containers started in the system. Runs forever unless there is a setup error.
func (self *manager) watchForNewContainers(quit chan error) error {
	var root *containerData
	var ok bool
	func() {
		self.containersLock.RLock()
		defer self.containersLock.RUnlock()
		root, ok = self.containers[namespacedContainerName{
			Name: "/",
		}]
	}()
	if !ok {
		return fmt.Errorf("[Error] Root container does not exist when watching for new containers")
	}

	// Register for new subcontainers.
	eventsChannel := make(chan container.SubcontainerEvent, 16)
	err := root.handler.WatchSubcontainers(eventsChannel)
	if err != nil {
		return err
	}

	// There is a race between starting the watch and new container creation so we do a detection before we read new containers.
	err = self.detectSubcontainers("/")
	if err != nil {
		return err
	}

	// Listen to events from the container handler.
	go func() {
		for {
			select {
			case event := <-eventsChannel:
				switch {
				case event.EventType == container.SubcontainerAdd:
					err = self.createContainer(event.Name)
				case event.EventType == container.SubcontainerDelete:
					err = self.destroyContainer(event.Name)
				}
				if err != nil {
					log.Printf("[Error] Failed to process watch event: %v", err)
				}
			case <-quit:
				// Stop processing events if asked to quit.
				err := root.handler.StopWatchingSubcontainers()
				quit <- err
				if err == nil {
					log.Printf("[Info] Exiting thread watching subcontainers")
					return
				}
			}
		}
	}()
	return nil
}

func (self *manager) globalHousekeeping(quit chan error) {
	// Long housekeeping is either 100ms or half of the housekeeping interval.
	longHousekeeping := 100 * time.Millisecond
	if globalHousekeepingInterval/2 < longHousekeeping {
		longHousekeeping = globalHousekeepingInterval / 2
	}

	ticker := time.Tick(globalHousekeepingInterval)
	for {
		select {
		case <-ticker:
			start := time.Now()

			// Check for new containers.
			err := self.detectSubcontainers("/")
			if err != nil {
				log.Printf("[Error] Failed to detect containers: %s", err)
			}

			// Log if housekeeping took too long.
			duration := time.Since(start)
			if duration >= longHousekeeping {
				//log.Printf("[Info] Global Housekeeping(%d) took %s", t.Unix(), duration)
			}
		case <-quit:
			// Quit if asked to do so.
			quit <- nil
			log.Printf("[Info] Exiting global housekeeping thread")
			return
		}
	}
}
