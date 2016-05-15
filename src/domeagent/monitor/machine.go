package monitor

import (
	"io/ioutil"
	"log"
	"strings"

	"domeagent/monitor/container/docker"
	"domeagent/monitor/fs"
	"domeagent/monitor/info"
	"domeagent/monitor/utils/machine"
	"domeagent/monitor/utils/sysfs"
	"domeagent/monitor/utils/sysinfo"
)

var machineIdFilePath = "/etc/machine-id,/var/lib/dbus/machine-id"
var bootIdFilePath = "/proc/sys/kernel/random/boot_id"

func getInfoFromFiles(filePaths string) string {
	if len(filePaths) == 0 {
		return ""
	}
	for _, file := range strings.Split(filePaths, ",") {
		id, err := ioutil.ReadFile(file)
		if err == nil {
			return strings.TrimSpace(string(id))
		}
	}
	log.Printf("[Error] Couldn't collect info from any of the files in %q", filePaths)
	return ""
}

func getMachineInfo(sysFs sysfs.SysFs, fsInfo fs.FsInfo) (*info.MachineInfo, error) {
	cpuinfo, err := ioutil.ReadFile("/proc/cpuinfo")
	clockSpeed, err := machine.GetClockSpeed(cpuinfo)
	if err != nil {
		return nil, err
	}

	memoryCapacity, err := machine.GetMachineMemoryCapacity()
	if err != nil {
		return nil, err
	}

	filesystems, err := fsInfo.GetGlobalFsInfo()
	if err != nil {
		log.Printf("[Error] Failed to get global filesystem information: %v", err)
		return nil, err
	}

	diskMap, err := sysinfo.GetBlockDeviceInfo(sysFs)
	if err != nil {
		log.Printf("[Error] Failed to get disk map: %v", err)
		return nil, err
	}

	netDevices, err := sysinfo.GetNetworkDevices(sysFs)
	if err != nil {
		log.Printf("[Error] Failed to get network devices: %v", err)
		return nil, err
	}

	topology, numCores, err := machine.GetTopology(sysFs, string(cpuinfo))
	if err != nil {
		log.Printf("[Error] Failed to get topology information: %v", err)
		return nil, err
	}

	systemUUID, err := sysinfo.GetSystemUUID(sysFs)
	if err != nil {
		log.Printf("[Error] Failed to get system UUID: %v", err)
		return nil, err
	}

	machineInfo := &info.MachineInfo{
		NumCores:       numCores,
		CpuFrequency:   clockSpeed,
		MemoryCapacity: memoryCapacity,
		DiskMap:        diskMap,
		NetworkDevices: netDevices,
		Topology:       topology,
		MachineID:      getInfoFromFiles(machineIdFilePath),
		SystemUUID:     systemUUID,
		BootID:         getInfoFromFiles(bootIdFilePath),
	}

	for _, fs := range filesystems {
		machineInfo.Filesystems = append(machineInfo.Filesystems, info.FsInfo{Device: fs.Device, Capacity: fs.Capacity})
	}

	return machineInfo, nil
}

func getVersionInfo() (*info.VersionInfo, error) {

	container_os := getContainerOsVersion()
	docker_version := getDockerVersion()

	return &info.VersionInfo{
		ContainerOsVersion: container_os,
		DockerVersion:      docker_version,
	}, nil
}

func getContainerOsVersion() string {
	container_os := "Unknown"
	os_release, err := ioutil.ReadFile("/etc/os-release")
	if err == nil {
		// We might be running in a busybox or some hand-crafted image.
		// It's useful to know why cadvisor didn't come up.
		for _, line := range strings.Split(string(os_release), "\n") {
			parsed := strings.Split(line, "\"")
			if len(parsed) == 3 && parsed[0] == "PRETTY_NAME=" {
				container_os = parsed[1]
				break
			}
		}
	}
	return container_os
}

func getDockerVersion() string {
	docker_version := "Unknown"
	client, err := docker.Client()
	if err == nil {
		version, err := client.Version()
		if err == nil {
			docker_version = version.Get("Version")
		}
	}
	return docker_version
}
