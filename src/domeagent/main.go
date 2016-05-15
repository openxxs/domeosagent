// xiaoshengxu@sohu-inc.com
// dome agent main func, with monitor daemon process

package main

import (
	"domeagent/agentmain"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

func main() {
	var waitGroup sync.WaitGroup
	var arguments agentmain.Arguments
	flag.IntVar(&(arguments.Pid), "pid", -1, "daemon pid")
	flag.StringVar(&(arguments.Host), "host", "", "server hostname")
	flag.IntVar(&(arguments.Port), "port", -1, "server hostport")
	flag.StringVar(&(arguments.AgentType), "type", "normal", "agent type")
	flag.BoolVar(&(arguments.Dns), "dns", false, "report dns information")
	flag.Parse()
	waitGroup.Add(1)
	go keepDaemon(waitGroup, arguments)
	agentmain.Main(arguments)
	waitGroup.Wait()
}

func keepDaemon(waitGroup sync.WaitGroup, arguments agentmain.Arguments) {
	defer waitGroup.Done()
	agentExec, _ := filepath.Abs(os.Args[0])
	execDir, _ := filepath.Split(agentExec)
	daemonExec := filepath.Join(execDir, agentmain.DOME_DAEMON)
	daemonPid := arguments.Pid
	for {
		daemonProcess, _ := os.FindProcess(daemonPid)
		err := daemonProcess.Signal(syscall.Signal(0))
		if err != nil {
			parameters := make([]string, 0)
			parameters = append(parameters, "--pid="+strconv.Itoa(os.Getpid()))
			parameters = append(parameters, "--host="+arguments.Host)
			parameters = append(parameters, "--port="+strconv.Itoa(arguments.Port))
			parameters = append(parameters, "--type="+arguments.AgentType)
			if arguments.Dns {
				parameters = append(parameters, "--dns=true")
			} else {
				parameters = append(parameters, "--dns=false")
			}
			cmd := exec.Command(daemonExec, parameters...)
			cmd.Start()
			if cmd.Process != nil {
				daemonPid = cmd.Process.Pid
			}
			cmd.Wait()
		}
	}
}
