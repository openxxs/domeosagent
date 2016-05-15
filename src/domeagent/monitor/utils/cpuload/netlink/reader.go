package netlink

import (
	"fmt"
	"os"

	"domeagent/monitor/info"
)

type NetlinkReader struct {
	familyId uint16
	conn     *Connection
}

func New() (*NetlinkReader, error) {
	conn, err := newConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to create a new connection: %s", err)
	}

	id, err := getFamilyId(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to get netlink family id for task stats: %s", err)
	}
	//log.Printf("Family id for taskstats: %d", id)
	return &NetlinkReader{
		familyId: id,
		conn:     conn,
	}, nil
}

func (self *NetlinkReader) Stop() {
	if self.conn != nil {
		self.conn.Close()
	}
}

func (self *NetlinkReader) Start() error {
	// We do the start setup for netlink in New(). Nothing to do here.
	return nil
}

// Returns instantaneous number of running tasks in a group.
// Caller can use historical data to calculate cpu load.
// path is an absolute filesystem path for a container under the CPU cgroup hierarchy.
// NOTE: non-hierarchical load is returned. It does not include load for subcontainers.
func (self *NetlinkReader) GetCpuLoad(name string, path string) (info.LoadStats, error) {
	if len(path) == 0 {
		return info.LoadStats{}, fmt.Errorf("cgroup path can not be empty!")
	}

	cfd, err := os.Open(path)
	if err != nil {
		return info.LoadStats{}, fmt.Errorf("failed to open cgroup path %s: %q", path, err)
	}

	stats, err := getLoadStats(self.familyId, cfd.Fd(), self.conn)
	if err != nil {
		return info.LoadStats{}, err
	}
	//log.Printf("Task stats for %q: %+v", path, stats)
	return stats, nil
}