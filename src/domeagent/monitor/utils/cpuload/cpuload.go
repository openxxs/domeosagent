package cpuload

import (
	"fmt"

	"domeagent/monitor/info"
	"domeagent/monitor/utils/cpuload/netlink"
)

type CpuLoadReader interface {
	// Start the reader.
	Start() error

	// Stop the reader and clean up internal state.
	Stop()

	// Retarieve Cpu load for  given group.
	// name is the full hierarchical name of the container.
	// Path is an absolute filesystem path for a container under CPU cgroup hierarchy.
	GetCpuLoad(name string, path string) (info.LoadStats, error)
}

func New() (CpuLoadReader, error) {
	reader, err := netlink.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create a netlink based cpuload reader: %v", err)
	}
	//log.Printf("Using a netlink-based load reader")
	return reader, nil
}
