// xiaoshengxu@sohu-inc.com
// dome daemon main func, with monitor agent process

package main

import (
	"domeagent/agentmain"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func main() {
	var arguments agentmain.Arguments
	flag.IntVar(&(arguments.Pid), "pid", -1, "daemon pid")
	flag.StringVar(&(arguments.Host), "host", "", "server hostname")
	flag.IntVar(&(arguments.Port), "port", -1, "server hostport")
	flag.StringVar(&(arguments.AgentType), "type", "normal", "agent type")
	flag.BoolVar(&(arguments.Dns), "dns", false, "report dns information")
	flag.Parse()
	daemonExec, _ := filepath.Abs(os.Args[0])
	execDir, _ := filepath.Split(daemonExec)
	agentExec := filepath.Join(execDir, agentmain.DOME_AGENT)
	agentPid := arguments.Pid
	if agentPid == -1 {
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
		cmd := exec.Command(agentExec, parameters...)
		cmd.Start()
		for {
			cmd.Wait()
			if cmd.Process == nil || cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				cmd = exec.Command(agentExec, parameters...)
				cmd.Start()
			}
		}
	} else {
		for {
			agentProcess, _ := os.FindProcess(agentPid)
			err := agentProcess.Signal(syscall.Signal(0))
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
				cmd := exec.Command(agentExec, parameters...)
				cmd.Start()
				if cmd.Process != nil {
					agentPid = cmd.Process.Pid
				}
				cmd.Wait()
			}
		}
	}
}
