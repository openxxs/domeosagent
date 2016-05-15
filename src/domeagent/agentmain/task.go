// xiaoshengxu@sohu-inc.com
// Process Tasks from server

package agentmain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/fsouza/go-dockerclient"
	"log"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"websocket"
)

var sendFeedBackChannel chan FeedBack = make(chan FeedBack)
var taskClient *websocket.Conn
var aliveContainerSet *Set = NewSet()
var reportLogsSessionSet *Set = NewSet()
var stopSetForFeedBack *Set = NewSet()
var stopWaitGroup sync.WaitGroup

func ProcessTask(goroutineWaitGroup sync.WaitGroup, url string, arguments Arguments) {
	defer goroutineWaitGroup.Done()
	// create socket connection between agent and webserver
	serverOrigin := arguments.Host + ":" + strconv.Itoa(arguments.Port)
	var taskClientError error
	for {
		taskClient, taskClientError = websocket.Dial(url, "", "http://"+serverOrigin)
		if taskClientError != nil {
			log.Println("[Error] Websocket Dial: ", taskClientError.Error())
			for taskClient != nil && taskClient.Close() != nil {
				time.Sleep(time.Duration(AgentConfiguration.WebsocketWait) * time.Second)
			}
			time.Sleep(time.Duration(AgentConfiguration.WebsocketWait) * time.Second)
			// register again because node info in server will be deleted when websocket session lost
			sendClientInfoUrl := "http://" + serverOrigin + "/node/register"
			SendClientInfo(sendClientInfoUrl, arguments)
		} else {
			break
		}
	}

	go SendTaskFeedBack(url, serverOrigin)

	go PingServer()

	go MonitorContainer()

	receiveReader := bufio.NewReader(taskClient)
	for {
		receiveMessage, receiveError := receiveReader.ReadBytes('\n')
		if receiveError != nil {
			// restart websocket connection when reading bytes error, and discard the received incomplete task data
			log.Println("[Error] Receive task data: ", receiveError.Error())
			for taskClient != nil && taskClient.Close() != nil {
				time.Sleep(time.Duration(AgentConfiguration.WebsocketWait) * time.Second)
			}
			var websocketError error
			for {
				sendClientInfoUrl := "http://" + serverOrigin + "/node/register"
				SendClientInfo(sendClientInfoUrl, arguments)
				taskClient, websocketError = websocket.Dial(url, "", "http://"+serverOrigin)
				if websocketError != nil {
					log.Println("[Error] Websocket Dial: ", websocketError.Error())
					time.Sleep(time.Duration(AgentConfiguration.WebsocketWait) * time.Second)
				} else {
					break
				}
			}
			receiveReader.Reset(taskClient)
		} else {
			receiveMessage[len(receiveMessage)-1] = '\x00'
			go Task(bytes.TrimRight(receiveMessage, "\x00"))
		}
	}
}

// send feedback package to server
func SendTaskFeedBack(url string, serverOrigin string) {
	for {
		sendData := <-sendFeedBackChannel
		sendDataJson, sendDataJsonError := json.Marshal(sendData)
		if sendDataJsonError != nil {
			// discard whole package when package format is illegal
			log.Println("[Error] JSON Marshal: ", sendDataJsonError.Error())
		} else {
			sendDataJson = append(sendDataJson, '\n')
			for {
				if taskClient != nil {
					sendCount, feedBackError := taskClient.Write(sendDataJson)
					if feedBackError != nil {
						// ProcessTask function will try to restart websocket, just wait
						time.Sleep(7 * time.Second)
					} else if sendCount != len(sendDataJson) {
						// send whole package again when losing data
					} else {
						break
					}
				}
			}
		}
	}
}

func PingServer() {
	for {
		availablePorts, _ := GetAvailablePorts()
		pingPackage := FeedBack{
			Type: "ping",
			Data: PingFeedBack{
				Ports: availablePorts,
			},
		}
		time.Sleep(time.Duration(AgentConfiguration.PingInterval) * time.Second)
		sendFeedBackChannel <- pingPackage
	}
}

func MonitorContainer() {
	client, err := docker.NewClient(AgentConfiguration.DockerServerAddr)
	for err != nil {
		log.Println("[Error] Monitor Container: ", err.Error())
		time.Sleep(10 * time.Second)
		client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	}
	// get the information of all containers (running or stopped) in host, then add them to aliveContainerSet and stopSetForFeedBack
	// it means container stopped before agent restart, just send instance feedback as normal
	// and treat all containers in host before agent start as containers which started by task command
	hostContainers, listHostContainerError := client.ListContainers(docker.ListContainersOptions{All: true})
	if listHostContainerError == nil {
		for _, hostContainer := range hostContainers {
			aliveContainerSet.Add(hostContainer.ID)
			stopSetForFeedBack.Add(hostContainer.ID)
		}
	} else {
		log.Println("[Error] Monitor Container: ", listHostContainerError.Error())
	}
	for {
		aliveApiContainers, listErr := client.ListContainers(docker.ListContainersOptions{All: false})
		if listErr == nil {
			oldAliveContainerIds := aliveContainerSet.List()
			for _, oldAliveId := range oldAliveContainerIds {
				isAlive := false
				for _, aliveApi := range aliveApiContainers {
					if aliveApi.ID == oldAliveId {
						isAlive = true
						break
					}
				}
				if !isAlive {
					// send container stopped information to server
					aliveContainerSet.Remove(oldAliveId)
					stopWaitGroup.Wait()
					if stopSetForFeedBack.Has(oldAliveId) {
						sendFeedBackChannel <- FeedBack{
							Type: "instance",
							Data: InstanceFeedBack{
								Phase:         "stop",
								ContainerId:   oldAliveId.(string),
								ContainerName: GetContainerNameById(client, oldAliveId.(string)),
								Addon: InstanceStopAddon{
									ExitedCode: GetContainerExitedCodeById(client, oldAliveId.(string)),
									ExitedMsg:  "normal",
								},
							},
						}
						stopSetForFeedBack.Remove(oldAliveId)
					} else {
						sendFeedBackChannel <- FeedBack{
							Type: "instance",
							Data: InstanceFeedBack{
								Phase:         "stop",
								ContainerId:   oldAliveId.(string),
								ContainerName: GetContainerNameById(client, oldAliveId.(string)),
								Addon: InstanceStopAddon{
									ExitedCode: GetContainerExitedCodeById(client, oldAliveId.(string)),
									ExitedMsg:  "unexcepted",
								},
							},
						}
					}
				}
			}
		} else {
			// todo: send list error to server
		}
		time.Sleep(time.Duration(AgentConfiguration.CheckContainerStatus) * time.Second)
	}
}

func ServiceHealthCheck(containerId string, healthCheck HealthCheckInfo) {
	if healthCheck.CheckType == "none" {
		return
	}
	dockerClient, err := docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		log.Println("[Error] Service Health Check: ", err.Error())
		return
	}
	failTimes := 0
	containerStop := false
	if healthCheck.CheckType == "http" {
		transport := http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				return net.DialTimeout(network, addr, time.Duration(int64(healthCheck.TimeoutMs)*int64(time.Millisecond)))
			},
		}
		client := http.Client{
			Transport: &transport,
		}
		url := "http://0.0.0.0:" + healthCheck.Port + "/" + healthCheck.CheckPath
		legalCodes := strings.Split(healthCheck.AllowResponseCode, " ")
		for {
			// container has stopped
			if ContainerIsRunning(dockerClient, containerId) == false {
				containerStop = true
				break
			}
			resp, err := client.Get(url)
			if err != nil {
				failTimes++
				log.Println("[Warn] Health Check: ", err.Error())
			} else {
				statusCode := strconv.Itoa(resp.StatusCode)
				success := false
				for _, code := range legalCodes {
					if len(code) == 3 && statusCode[0] == code[0] {
						if code[1] == 'x' || code[1] == statusCode[1] {
							if code[2] == 'x' || code[2] == statusCode[2] {
								success = true
								break
							}
						}
					}
				}
				if success {
					failTimes = 0
				} else {
					failTimes++
					log.Println("[Warn] Health Check: Response Code is ", statusCode)
				}
			}
			if resp != nil {
				resp.Body.Close()
			}
			// failed too many times consecutively
			if failTimes >= healthCheck.CheckTimes {
				containerStop = false
				break
			}
			time.Sleep(time.Duration(int64(healthCheck.CheckIntervalMs) * int64(time.Millisecond)))
		}
	} else if healthCheck.CheckType == "tcp" {
		url := "localhost:" + healthCheck.Port
		for {
			// container has stopped
			if ContainerIsRunning(dockerClient, containerId) == false {
				containerStop = true
				break
			}
			conn, err := net.DialTimeout("tcp", url, time.Duration(int64(healthCheck.TimeoutMs)*int64(time.Millisecond)))
			if err != nil {
				failTimes++
			} else {
				failTimes = 0
			}
			if conn != nil {
				conn.Close()
			}
			// failed too many times consecutively
			if failTimes >= healthCheck.CheckTimes {
				containerStop = false
				break
			}
			time.Sleep(time.Duration(int64(healthCheck.CheckIntervalMs) * int64(time.Millisecond)))
		}
	} else {
		return
	}
	if !containerStop {
		sendFeedBackChannel <- FeedBack{
			Type: "instance",
			Data: InstanceFeedBack{
				Phase:         "down",
				ContainerId:   containerId,
				ContainerName: GetContainerNameById(dockerClient, containerId),
				Addon: InstanceDownAddon{
					Port:      healthCheck.Port,
					FailTimes: failTimes,
				},
			},
		}
	}
}

func Task(taskBytes []byte) {
	var nodeTask NodeTask
	unMarshalError := json.Unmarshal(taskBytes, &nodeTask)
	if unMarshalError != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: -1,
				ResultCode: 1,
				ResultMsg:  unMarshalError.Error(),
			},
		}
		log.Println("[Error] Task Unmarshal: ", unMarshalError.Error())
		return
	} else {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: nodeTask.NodeTaskId,
				ResultCode: RESULT_CODE_OK,
			},
		}
		log.Println("[Success] Receive task")
	}
	var containerId string
	var containerName string
	var err error
	var container *docker.Container
	switch nodeTask.TaskType {
	case "RunContainer":
		container, err = RunContainer(taskBytes, nodeTask.NodeTaskId)
		if err == nil {
			containerId = container.ID
			containerName = container.Name
		}
	case "PullImage":
		err = PullImage(taskBytes, nodeTask.NodeTaskId)
	case "CreateContainer":
		container, _, err = CreateContainer(taskBytes, nodeTask.NodeTaskId)
		if err == nil {
			containerId = container.ID
			containerName = container.Name
		}
	case "StartContainer":
		containerId, containerName, err = StartContainer(taskBytes, nodeTask.NodeTaskId)
	case "StopContainer":
		containerId, containerName, err = StopContainer(taskBytes, nodeTask.NodeTaskId)
	case "RemoveContainer":
		containerId, containerName, err = RemoveContainer(taskBytes, nodeTask.NodeTaskId)
	case "RestartContainer":
		containerId, containerName, err = RestartContainer(taskBytes, nodeTask.NodeTaskId)
	case "ContainerLogs":
		containerId, containerName, err = ContainerLogs(taskBytes, nodeTask.NodeTaskId)
	default:
		err = fmt.Errorf("Duty Type doesn't exist: " + nodeTask.TaskType)
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: nodeTask.NodeTaskId,
				ResultCode: 7,
				ResultMsg:  err.Error(),
			},
		}
	}
	if err == nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: nodeTask.NodeTaskId,
				ResultCode: RESULT_CODE_OK,
				Result:     map[string]string{"containerId": containerId, "containerName": containerName},
			},
		}
	}
}

func RunContainer(taskBytes []byte, taskId int64) (container *docker.Container, err error) {
	err = PullImage(taskBytes, taskId)
	if err != nil {
		return container, err
	}
	var healthCheckInfo *HealthCheckInfo
	container, healthCheckInfo, err = CreateContainer(taskBytes, taskId)
	if err != nil {
		return container, err
	}
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return container, err
	}
	err = client.StartContainer(container.ID, nil)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 4,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Container: ", err.Error())
	} else {
		sendFeedBackChannel <- FeedBack{
			Type: "instance",
			Data: InstanceFeedBack{
				Phase:         "start",
				ContainerId:   container.ID,
				ContainerName: container.Name,
			},
		}
		aliveContainerSet.Add(container.ID)
		go ServiceHealthCheck(container.ID, *healthCheckInfo)
		log.Println("[Success] Start Container")
	}
	return container, err
}

func PullImage(taskBytes []byte, taskId int64) (err error) {
	var pullTask PullTask
	err = json.Unmarshal(taskBytes, &pullTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] PullImage Unmarshal: ", err.Error())
		return err
	}
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return err
	}
	opts := docker.PullImageOptions{
		Repository: pullTask.Repository,
		Registry:   pullTask.Registry,
		Tag:        pullTask.Tag,
	}
	err = client.PullImage(opts, docker.AuthConfiguration{})
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 2,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Pull Image: ", err.Error())
	} else {
		log.Println("[Success] Pull Image")
	}
	return err
}

func CreateContainer(taskBytes []byte, taskId int64) (container *docker.Container, healthCheckInfo *HealthCheckInfo, err error) {
	var createTask CreateTask
	err = json.Unmarshal(taskBytes, &createTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] CreateContainer Unmarshal: ", err.Error())
		return container, healthCheckInfo, err
	}
	healthCheckInfo = &(createTask.HealthCheckInfo)
	// check parameters
	var parameterError string
	// check required memory
	if createTask.Memory < 4*1024*1024 {
		parameterError = parameterError + "[Ilegal] memory is less than 4M;"
	}
	// create required directories
	for _, bind := range createTask.Binds {
		directory := strings.Split(bind, ":")[0]
		createDirectoryError := os.MkdirAll(directory, os.ModePerm)
		if createDirectoryError != nil {
			parameterError = parameterError + "[Fail] " + createDirectoryError.Error() + ";"
		}
	}
	if len(parameterError) != 0 {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 2,
				ResultMsg:  parameterError,
			},
		}
		log.Println("[Error] Create Container: Parameter is ilegal", parameterError)
		return container, healthCheckInfo, fmt.Errorf("Parameter ilegal")
	}
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return container, healthCheckInfo, err
	}
	transferExposedPorts := make(map[docker.Port]struct{})
	for _, port := range createTask.ExposedPorts {
		dockerPort := docker.Port(port)
		var tmpStruct struct{}
		transferExposedPorts[dockerPort] = tmpStruct
	}
	transferVolumes := make(map[string]struct{})
	for _, volume := range createTask.Volumes {
		var tmpStruct struct{}
		transferVolumes[volume] = tmpStruct
	}
	config := docker.Config{
		Hostname:        createTask.Hostname,
		Domainname:      "",
		User:            createTask.User,
		Memory:          createTask.Memory,
		CPUShares:       createTask.CpuShares,
		CPUSet:          createTask.CpusetCpus,
		AttachStdin:     false,
		AttachStdout:    false,
		AttachStderr:    false,
		PortSpecs:       []string{},
		ExposedPorts:    transferExposedPorts,
		Tty:             false,
		OpenStdin:       false,
		StdinOnce:       false,
		Env:             createTask.Env,
		Cmd:             createTask.Cmd,
		DNS:             createTask.Dns,
		Image:           createTask.Image,
		Volumes:         transferVolumes,
		VolumesFrom:     "",
		WorkingDir:      createTask.WorkingDir,
		MacAddress:      "",
		Entrypoint:      createTask.Entrypoint,
		NetworkDisabled: false,
		SecurityOpts:    []string{},
		OnBuild:         []string{},
		Labels:          createTask.Labels,
	}
	transferPortBindings := make(map[docker.Port][]docker.PortBinding)
	for port, binds := range createTask.PortBindings {
		var bindInfo []docker.PortBinding
		for _, aBind := range binds {
			// server will not send HostIP
			// portBinding := docker.PortBinding{HostIP: aBind.HostIP, HostPort: aBind.HostPort}
			portBinding := docker.PortBinding{HostIP: "0.0.0.0", HostPort: aBind.HostPort}
			bindInfo = append(bindInfo, portBinding)
		}
		transferPortBindings[docker.Port(port)] = bindInfo
	}
	hostConfig := docker.HostConfig{
		Binds:           createTask.Binds,
		CapAdd:          []string{},
		CapDrop:         []string{},
		ContainerIDFile: "",
		LxcConf:         []docker.KeyValuePair{},
		Privileged:      createTask.Privileged,
		PortBindings:    transferPortBindings,
		Links:           createTask.Links,
		PublishAllPorts: createTask.PublishAllPorts,
		DNS:             createTask.Dns,
		DNSSearch:       createTask.DnsSearch,
		ExtraHosts:      createTask.ExtraHosts,
		VolumesFrom:     createTask.VolumesFrom,
		NetworkMode:     createTask.NetworkMode,
		IpcMode:         "",
		PidMode:         "",
		UTSMode:         "",
		RestartPolicy:   createTask.RestartPolicy,
		Devices:         []docker.Device{},
		LogConfig:       docker.LogConfig{},
		ReadonlyRootfs:  false,
		SecurityOpt:     []string{},
		CgroupParent:    "",
		Memory:          createTask.Memory,
		MemorySwap:      0,
		CPUShares:       createTask.CpuShares,
		CPUSet:          createTask.CpusetCpus,
		CPUQuota:        0,
		CPUPeriod:       0,
		Ulimits:         []docker.ULimit{},
	}
	createOpts := docker.CreateContainerOptions{
		Name:       createTask.Name,
		Config:     &config,
		HostConfig: &hostConfig,
	}
	container, err = client.CreateContainer(createOpts)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 3,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Pull Image: ", err.Error())
	} else if container.Name != createTask.Name {
		log.Println("[Warn] Create Container: Container name in host and server are different")
	} else {
		log.Println("[Success] Create Container")
	}
	return container, healthCheckInfo, err
}

func StartContainer(taskBytes []byte, taskId int64) (containerId string, containerName string, err error) {
	var startTask StartTask
	err = json.Unmarshal(taskBytes, &startTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] StartContainer Unmarshal: ", err.Error())
		return containerId, containerName, err
	}
	containerId = startTask.ContainerId
	containerName = startTask.ContainerName
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return containerId, containerName, err
	}
	if len(containerId) == 0 {
		err = fmt.Errorf("Container ID is empty")
	} else {
		err = client.StartContainer(containerId, nil)
	}
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 4,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Container: ", err.Error())
	} else {
		containerName = GetContainerNameById(client, containerId)
		if startTask.ContainerName != containerName {
			log.Println("[Warn] StartContainer: Container name in host and server are different")
		}
		sendFeedBackChannel <- FeedBack{
			Type: "instance",
			Data: InstanceFeedBack{
				Phase:         "start",
				ContainerId:   containerId,
				ContainerName: containerName,
			},
		}
		aliveContainerSet.Add(containerId)
		go ServiceHealthCheck(containerId, startTask.HealthCheckInfo)
		log.Println("[Success] Start Container")
	}
	return containerId, containerName, err
}

func StopContainer(taskBytes []byte, taskId int64) (containerId string, containerName string, err error) {
	var stopTask StopTask
	err = json.Unmarshal(taskBytes, &stopTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] StopContainer Unmarshal: ", err.Error())
		return containerId, containerName, err
	}
	containerId = stopTask.ContainerId
	containerName = stopTask.ContainerName
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return containerId, containerName, err
	}
	stopWaitGroup.Add(1)
	if len(stopTask.ContainerId) == 0 {
		err = fmt.Errorf("Container ID is empty")
	} else {
		err = client.StopContainer(stopTask.ContainerId, stopTask.TimeWait)
	}
	if err != nil {
		if ContainerIsRunning(client, containerId) {
			sendFeedBackChannel <- FeedBack{
				Type: "task",
				Data: TaskFeedBack{
					Phase:      "finish",
					NodeTaskId: taskId,
					ResultCode: 10,
					ResultMsg:  err.Error(),
				},
			}
		} else {
			sendFeedBackChannel <- FeedBack{
				Type: "task",
				Data: TaskFeedBack{
					Phase:      "finish",
					NodeTaskId: taskId,
					ResultCode: 5,
					ResultMsg:  err.Error(),
				},
			}
		}
		log.Println("[Error] Stop Container: ", err.Error())
	} else {
		stopSetForFeedBack.Add(containerId)
		log.Println("[Success] Stop Container")
	}
	stopWaitGroup.Done()
	return containerId, containerName, err
}

func RestartContainer(taskBytes []byte, taskId int64) (containerId string, containerName string, err error) {
	var restartTask RestartTask
	err = json.Unmarshal(taskBytes, &restartTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Restart Container Unmarshal: ", err.Error())
		return containerId, containerName, err
	}
	containerId = restartTask.ContainerId
	containerName = restartTask.ContainerName
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return containerId, containerName, err
	}
	if len(restartTask.ContainerId) == 0 {
		err = fmt.Errorf("Container ID is empty")
	} else {
		err = client.RestartContainer(restartTask.ContainerId, restartTask.TimeWait)
	}
	isRunning := ContainerIsRunning(client, containerId)
	if err != nil {
		if isRunning {
			sendFeedBackChannel <- FeedBack{
				Type: "task",
				Data: TaskFeedBack{
					Phase:      "finish",
					NodeTaskId: taskId,
					ResultCode: 11,
					ResultMsg:  err.Error(),
				},
			}
			log.Println("[Error] Restart Container (now container is running): ", err.Error())
		} else {
			sendFeedBackChannel <- FeedBack{
				Type: "task",
				Data: TaskFeedBack{
					Phase:      "finish",
					NodeTaskId: taskId,
					ResultCode: 8,
					ResultMsg:  err.Error(),
				},
			}
			log.Println("[Error] Restart Container (now container is stopped): ", err.Error())
		}
	} else {
		containerName = GetContainerNameById(client, containerId)
		if restartTask.ContainerName != containerName {
			log.Println("[Warn] Restart Container: Container name in host and server are different")
		}
		sendFeedBackChannel <- FeedBack{
			Type: "instance",
			Data: InstanceFeedBack{
				Phase:         "restart",
				ContainerId:   containerId,
				ContainerName: containerName,
			},
		}
		log.Println("[Success] Restart Container")
	}
	if isRunning && !aliveContainerSet.Has(containerId) {
		go ServiceHealthCheck(containerId, restartTask.HealthCheckInfo)
		aliveContainerSet.Add(containerId)
	}
	return containerId, containerName, err
}

func RemoveContainer(taskBytes []byte, taskId int64) (containerId string, containerName string, err error) {
	var removeTask RemoveTask
	err = json.Unmarshal(taskBytes, &removeTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Remove Container Unmarshal: ", err.Error())
		return containerId, containerName, err
	}
	containerId = removeTask.ContainerId
	containerName = removeTask.ContainerName
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return containerId, containerName, err
	}
	opts := docker.RemoveContainerOptions{
		ID:            removeTask.ContainerId,
		RemoveVolumes: removeTask.RemoveVolumes,
		Force:         removeTask.Force,
	}
	if len(removeTask.ContainerId) == 0 {
		err = fmt.Errorf("Container ID is empty")
	} else {
		err = client.RemoveContainer(opts)
	}
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 6,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Remove Container: ", err.Error())
	} else {
		log.Println("[Success] Remove Container")
	}
	return containerId, containerName, err
}

func ContainerLogs(taskBytes []byte, taskId int64) (containerId string, containerName string, err error) {
	var logsTask LogsTask
	err = json.Unmarshal(taskBytes, &logsTask)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "ack",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Container Logs Unmarshal: ", err.Error())
		return containerId, containerName, err
	}
	containerId = logsTask.ContainerId
	containerName = logsTask.ContainerName
	var client *docker.Client
	client, err = docker.NewClient(AgentConfiguration.DockerServerAddr)
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 1,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Start Docker Client: ", err.Error())
		return containerId, containerName, err
	}
	if len(logsTask.ContainerId) == 0 {
		err = fmt.Errorf("Container ID is empty")
	} else {
		if logsTask.StartReport {
			if reportLogsSessionSet.Has(logsTask.SessionId) {
				err = fmt.Errorf("Container logs report has been started")
			} else {
				reportLogsSessionSet.Add(logsTask.SessionId)
				var logsBuffer bytes.Buffer
				logsOpts := docker.LogsOptions{
					Container:    containerId,
					OutputStream: &logsBuffer,
					ErrorStream:  &logsBuffer,
					Follow:       true,
					Stdout:       true,
					Stderr:       true,
					Timestamps:   false,
					Tail:         strconv.Itoa(AgentConfiguration.InstanceLogTail),
				}

				// client.Logs just like "tail -f", so we need to make it as a goroutine
				go func() {
					client.Logs(logsOpts)
					for ContainerExist(client, containerId) && reportLogsSessionSet.Has(logsTask.SessionId) {
						client.Logs(logsOpts)
						time.Sleep(time.Duration(AgentConfiguration.InstanceLogInterval) * time.Millisecond)
					}
					if !ContainerExist(client, containerId) && reportLogsSessionSet.Has(logsTask.SessionId) {
						log.Println("[Warn] Report Container Logs: container has been removed -- ", containerId)
						reportLogsSessionSet.Remove(logsTask.SessionId)
					}
				}()

				time.Sleep(time.Duration(AgentConfiguration.InstanceLogInterval) * time.Millisecond) // give docker some time to get logs
				log.Println("[Success] Start Report Container Logs")
				for reportLogsSessionSet.Has(logsTask.SessionId) {
					dataLine, readLineError := logsBuffer.ReadString('\n')
					if readLineError == nil {
						sendFeedBackChannel <- FeedBack{
							Type: "instanceLogs",
							Data: InstanceLogsFeedBack{
								SessionId:     logsTask.SessionId,
								ContainerId:   containerId,
								ContainerName: containerName,
								Logs:          dataLine,
							},
						}
					} else if readLineError.Error() != "EOF" {
						reportLogsSessionSet.Remove(logsTask.SessionId)
					}
				}
				log.Println("[Success] Stop Report Container Logs")
			}
		} else {
			if reportLogsSessionSet.Has(logsTask.SessionId) {
				reportLogsSessionSet.Remove(logsTask.SessionId)
			} else {
				err = fmt.Errorf("Container logs report is not running")
			}
		}
	}
	if err != nil {
		sendFeedBackChannel <- FeedBack{
			Type: "task",
			Data: TaskFeedBack{
				Phase:      "finish",
				NodeTaskId: taskId,
				ResultCode: 9,
				ResultMsg:  err.Error(),
			},
		}
		log.Println("[Error] Container Logs Report: ", err.Error())
	}
	return containerId, containerName, err
}

func ContainerExist(client *docker.Client, id string) (exist bool) {
	_, err := client.InspectContainer(id)
	if reflect.DeepEqual(err, &docker.NoSuchContainer{ID: id}) {
		return false
	} else {
		return true
	}
}

func ContainerIsRunning(client *docker.Client, id string) (isRunning bool) {
	container, err := client.InspectContainer(id)
	if err != nil {
		return false
	} else {
		return container.State.Running
	}
}

func GetContainerNameById(client *docker.Client, id string) (name string) {
	container, err := client.InspectContainer(id)
	if err != nil {
		log.Println("[Warn] Get Container Name By Id: ", err.Error())
	} else {
		name = container.Name
		if name[0] == '/' {
			name = name[1:len(name)]
		}
	}
	return name
}

func GetContainerExitedCodeById(client *docker.Client, id string) (code int) {
	container, err := client.InspectContainer(id)
	if err != nil {
		log.Println("[Warn] Get Container Exited Code By Id: ", err.Error())
	} else {
		code = container.State.ExitCode
	}
	return code
}
