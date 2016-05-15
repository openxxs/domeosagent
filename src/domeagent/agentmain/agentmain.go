// xiaoshengxu@sohu-inc.com
// agent main

package agentmain

import (
	"log"
	"os"
	"strconv"
	"sync"
)

type Arguments struct {
	Pid       int
	Host      string
	Port      int
	AgentType string
	Dns       bool
}

func Main(arguments Arguments) {
	var goroutineWaitGroup sync.WaitGroup

	// set log file
	logFile, logFileError := os.OpenFile(DOME_AGENT_LOG, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if logFileError == nil {
		log.SetOutput(logFile)
	} else {
		log.Fatal("[Fatal] Set Log File: ", logFileError.Error())
	}

	// check configure file
	confFile, confFileError := os.OpenFile(DOME_AGENT_CONF, os.O_RDONLY|os.O_CREATE, 0666)
	if confFileError != nil {
		log.Fatal("[Fatal] Create Configure File: ", confFileError.Error())
	}
	if confFile != nil {
		confFile.Close()
	}

	// check arguments
	if arguments.Host == "" {
		log.Fatal("[Fatal] Arguments Error: lack of Hostname")
		return
	}
	if arguments.Port == -1 {
		log.Fatal("[Fatal] Arguments Error: lack of Hostport")
		return
	}
	serverOrigin := arguments.Host + ":" + strconv.Itoa(arguments.Port)

	// get agent configuration from server
	agentHostname, hostnameError := GetHostname()
	if hostnameError != nil {
		log.Fatal("[Fatal] Get Agent Hostname: ", hostnameError)
		return
	}
	agentConfigUrl := "http://" + serverOrigin + "/node/agent_config/" + agentHostname
	monitorUrl := "http://" + serverOrigin + "/node/monitor_parameter"
	var agentConfigError error
	agentConfigError = setConfiguration(agentConfigUrl, monitorUrl)
	for agentConfigError != nil {
		agentConfigError = setConfiguration(agentConfigUrl, monitorUrl)
	}

	// send client info to server and register agent
	sendClientInfoUrl := "http://" + serverOrigin + "/node/register"
	SendClientInfo(sendClientInfoUrl, arguments)

	// system running environment checks and prepares
	for {
		runningEnvError := prepareRunningEnv()
		if runningEnvError == nil {
			log.Println("[Success] Running Enviornment is OK")
			break
		}
	}

	// start monitor
	goroutineWaitGroup.Add(1)
	go StartMonitor(goroutineWaitGroup)

	// start garbage collection
	goroutineWaitGroup.Add(1)
	go GarbageCollection(goroutineWaitGroup)

	// process duties
	hostname, _ := GetHostname()
	processTaskUrl := "ws://" + serverOrigin + "/noderpc?hostName=" + hostname
	goroutineWaitGroup.Add(1)
	go ProcessTask(goroutineWaitGroup, processTaskUrl, arguments)

	goroutineWaitGroup.Wait()

	if logFile != nil {
		logFile.Close()
	}
}
