package raw

import (
	"fmt"

	"domeagent/monitor/container/libcontainer"
	"domeagent/monitor/fs"
	"domeagent/monitor/info"
	"domeagent/monitor/container"
)

var dockerOnly = false

type rawFactory struct {
	// Factory for machine information.
	machineInfoFactory info.MachineInfoFactory

	// Information about the cgroup subsystems.
	cgroupSubsystems *libcontainer.CgroupSubsystems

	// Information about mounted filesystems.
	fsInfo fs.FsInfo

	// Watcher for inotify events.
	watcher *InotifyWatcher
}

func (self *rawFactory) String() string {
	return "raw"
}

func (self *rawFactory) NewContainerHandler(name string, inHostNamespace bool) (container.ContainerHandler, error) {
	rootFs := "/"
	if !inHostNamespace {
		rootFs = "/rootfs"
	}
	return newRawContainerHandler(name, self.cgroupSubsystems, self.machineInfoFactory, self.fsInfo, self.watcher, rootFs)
}

// The raw factory can handle any container. If --docker_only is set to false, non-docker containers are ignored.
func (self *rawFactory) CanHandleAndAccept(name string) (bool, bool, error) {
	accept := name == "/" || !dockerOnly
	return true, accept, nil
}

func (self *rawFactory) DebugInfo() map[string][]string {
	out := make(map[string][]string)

	// Get information about inotify watches.
	watches := self.watcher.GetWatches()
	lines := make([]string, 0, len(watches))
	for containerName, cgroupWatches := range watches {
		lines = append(lines, fmt.Sprintf("%s:", containerName))
		for _, cg := range cgroupWatches {
			lines = append(lines, fmt.Sprintf("\t%s", cg))
		}
	}
	out["Inotify watches"] = lines

	return out
}

func Register(machineInfoFactory info.MachineInfoFactory, fsInfo fs.FsInfo) error {
	cgroupSubsystems, err := libcontainer.GetCgroupSubsystems()
	if err != nil {
		return fmt.Errorf("failed to get cgroup subsystems: %v", err)
	}
	if len(cgroupSubsystems.Mounts) == 0 {
		return fmt.Errorf("failed to find supported cgroup mounts for the raw factory")
	}

	watcher, err := NewInotifyWatcher()
	if err != nil {
		return err
	}

	factory := &rawFactory{
		machineInfoFactory: machineInfoFactory,
		fsInfo:             fsInfo,
		cgroupSubsystems:   &cgroupSubsystems,
		watcher:            watcher,
	}
	container.RegisterContainerHandlerFactory(factory)
	return nil
}