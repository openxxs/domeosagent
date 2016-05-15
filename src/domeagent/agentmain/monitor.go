// xiaoshengxu@sohu-inc.com
// create agent monitor function

package agentmain

import (
	"domeagent/monitor"
	"log"
	"sync"
	"time"
)

func StartMonitor(goroutineWaitGroup sync.WaitGroup) {
	defer goroutineWaitGroup.Done()
	housekeepingInterval := time.Duration(monitorServer.HousekeepingInterval) * time.Millisecond
	monitorConfig := &monitor.InfluxConfig{
		Table:          monitorServer.Table,
		Database:       monitorServer.Database,
		Username:       monitorServer.Username,
		Password:       monitorServer.Password,
		Host:           monitorServer.Host,
		BufferDuration: time.Duration(monitorServer.BufferDuration) * time.Millisecond,
		FilterPrefix:   AgentConfiguration.FsFilterPrefix,
	}
	monitorManager, newMonitorError := monitor.New(housekeepingInterval, monitorConfig)
	if newMonitorError != nil {
		log.Println("[Error] Fail New Monitor: ", newMonitorError.Error())
	} else {
		log.Println("[Success] Create New Monitor")
	}
	startMonitorError := monitorManager.Start()
	if startMonitorError != nil {
		log.Println("[Error] Fail Start Monitor: ", startMonitorError.Error())
	} else {
		log.Println("[Success] Start Monitor")
	}
}
