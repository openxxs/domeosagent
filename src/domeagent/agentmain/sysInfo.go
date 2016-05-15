// xiaoshengxu@sohu-inc.com
// get client system information and send it to server

package agentmain

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func prepareRunningEnv() (err error) {
	_, err = exec.Command("/bin/sh", "-c", `docker version`).Output()
	if err != nil {
		// start docker server
		startCommand := "docker -g " + AgentConfiguration.DockerGraph + " --insecure-registry " + AgentConfiguration.DockerInsecureRegistry + " --registry-mirror=" + AgentConfiguration.DockerRegistryMirror + " -d &"
		_, err = exec.Command("/bin/sh", "-c", startCommand).Output()
		if err != nil {
			log.Println("[Error] Start Docker Server: ", err.Error())
		}
	}
	return err
}

func GetHostname() (hostname string, err error) {
	return os.Hostname()
}

func GetCpu() (modelName string, number int, err error) {
	cpuinfo, cpuError := ioutil.ReadFile("/proc/cpuinfo")
	modelNameReg := regexp.MustCompile("model name\t: .*\n")
	modelName = modelNameReg.FindAllString(string(cpuinfo), 1)[0]
	modelName = modelName[strings.Index(modelName, ":")+2 : len(modelName)-2]
	numberReg := regexp.MustCompile("processor\t:")
	number = len(numberReg.FindAllString(string(cpuinfo), -1))
	return modelName, number, cpuError
}

// unit 1B
func GetMemory() (amount int64, err error) {
	meminfo, memError := ioutil.ReadFile("/proc/meminfo")
	meminfoReg := regexp.MustCompile("MemTotal:.*kB\n")
	memTotal := meminfoReg.FindAllString(string(meminfo), 1)[0]
	memNumberReg := regexp.MustCompile("[0-9]+")
	amount, _ = strconv.ParseInt(memNumberReg.FindAllString(memTotal, 1)[0], 10, 64)
	amount = amount * 1024
	return amount, memError
}

type DiskInfo struct {
	MountPoint string `json:"mountPoint,omitempty"`
	Device     string `json:"device,omitempty"`
	Capacity   int64  `json:"capacity,omitempty"`
}

// unit 1B
func GetDisk() (diskinfos []DiskInfo, err error) {
	getDiskStdout, diskError := exec.Command("/bin/sh", "-c", `df -B1 | grep "/" | awk '{print $1, $2, $6}'`).Output()
	if diskError != nil {
		return diskinfos, diskError
	}
	diskinfoLines := strings.Split(string(getDiskStdout), "\n")
	diskinfos = make([]DiskInfo, 0)
	for _, diskinfoLine := range diskinfoLines[:len(diskinfoLines)-1] {
		diskinfoParts := strings.Split(diskinfoLine, " ")
		if !strings.Contains(diskinfoParts[2], AgentConfiguration.NodeFsFilter) {
			var diskinfo DiskInfo
			diskinfo.Device = diskinfoParts[0]
			diskinfo.Capacity, err = strconv.ParseInt(diskinfoParts[1], 10, 64)
			diskinfo.MountPoint = diskinfoParts[2]
			diskinfos = append(diskinfos, diskinfo)
		}
	}
	return diskinfos, err
}

type NetworkCardInfo struct {
	Name  string `json:"name,omitempty"`  // name of network card
	Ip    string `json:"ip,omitempty"`    // ip address of network card
	Speed int32  `json:"speed,omitempty"` // transport speed of network card
}

func GetNetworkCard() (netCards []NetworkCardInfo, err error) {
	return netCards, err
}

func GetAvailablePorts() (availablePorts []int, err error) {
	var portSet *Set = NewSet()
	for _, file := range AgentConfiguration.PortScan {
		portInfoStr, readFileError := ioutil.ReadFile(file)
		if readFileError != nil {
			log.Println("[Warn] Get Available Ports: ", readFileError.Error())
		}
		portInfo := strings.Split(string(portInfoStr), "\n")
		for idx, line := range portInfo {
			if idx > 0 && len(line) > 3 {
				port16 := strings.Split(strings.Split(line, ":")[2], " ")[0]
				portTmp, convErr := strconv.ParseInt(port16, 16, 32)
				port := int(portTmp)
				if convErr != nil {
					log.Println("[Warn] Get Available Ports: ", convErr.Error())
				} else if !portSet.Has(port) {
					portSet.Add(port)
				}
			}
		}
	}
	for aport := AgentConfiguration.StartPort; aport <= AgentConfiguration.EndPort; aport++ {
		if !portSet.Has(aport) {
			availablePorts = append(availablePorts, aport)
		}
	}
	for _, bport := range AgentConfiguration.SpecialPorts {
		if !portSet.Has(bport) {
			availablePorts = append(availablePorts, bport)
		}
	}
	return availablePorts, err
}

type DnsInfo struct {
	Nameserver []string `json:"nameserver,omitempty"`
	Domain     []string `json:"domain,omitempty"`
	Search     []string `json:"search,omitempty"`
	Sortlist   []string `json:"sortlist,omitempty"`
}

func GetDnsInfo() (dnsInfo DnsInfo, err error) {
	dnsInfoStr, readFileError := ioutil.ReadFile(AgentConfiguration.ResolvConf)
	if readFileError != nil {
		return dnsInfo, readFileError
	}
	dnsInfoLines := strings.Split(string(dnsInfoStr), "\n")
	for _, line := range dnsInfoLines {
		line = strings.TrimLeft(line, " ")
		line = strings.TrimLeft(line, "\t")
		if len(line) < 1 || line[0] == '#' {
			continue
		}
		typeName := strings.Split(line, " ")[0]
		value := strings.TrimLeft(line, typeName)
		value = strings.TrimLeft(value, " ")
		value = strings.TrimLeft(value, "\t")
		switch typeName {
		case "nameserver":
			dnsInfo.Nameserver = append(dnsInfo.Nameserver, value)
		case "domain":
			dnsInfo.Domain = append(dnsInfo.Domain, value)
		case "search":
			dnsInfo.Search = append(dnsInfo.Search, value)
		case "sortlist":
			dnsInfo.Sortlist = append(dnsInfo.Sortlist, value)
		}
	}
	return dnsInfo, err
}

type ClientInfo struct {
	Hostname     string                 `json:"hostname,omitempty"`     // hostname
	CpuModelName string                 `json:"cpuModelName,omitempty"` // CPU mode name
	CpuNumber    int                    `json:"cpuNumber,omitempty"`    // number of CPU
	MemoryAmount int64                  `json:"memoryAmount,omitempty"` // amount of memory
	DiskInfos    []DiskInfo             `json:"diskInfos,omitempty"`    // information of disk
	Ports        []int                  `json:"ports,omitempty"`        // information of available net ports
	NetworkCard  []NetworkCardInfo      `json:"networkCard,omitempty"`  // information of network card
	Appendix     map[string]interface{} `json:"appendix,omitempty"`
}

// get the system information of client host machine
func GetClientInfo(arguments Arguments) (clientInfo ClientInfo, err error) {
	var hostnameError error
	clientInfo.Hostname, hostnameError = GetHostname()
	if hostnameError != nil {
		log.Fatal("[Fatal] Get Hostname: ", hostnameError.Error())
	}
	var cpuError error
	clientInfo.CpuModelName, clientInfo.CpuNumber, cpuError = GetCpu()
	if cpuError != nil {
		log.Fatal("[Fatal] Get Cpu Info: ", cpuError.Error())
	}
	var memError error
	clientInfo.MemoryAmount, memError = GetMemory()
	if memError != nil {
		log.Fatal("[Fatal] Get Memory Info: ", memError.Error())
	}
	var diskError error
	clientInfo.DiskInfos, diskError = GetDisk()
	if diskError != nil {
		log.Fatal("[Fatal] Get Disk Info: ", diskError.Error())
	}
	var networkError error
	clientInfo.NetworkCard, networkError = GetNetworkCard()
	if networkError != nil {
		log.Fatal("[Fatal] Get NetworkCard Info: ", networkError.Error())
	}
	var portsError error
	clientInfo.Ports, portsError = GetAvailablePorts()
	if portsError != nil {
		log.Println("[Warn] Get Available Ports: ", portsError)
	}
	appendix := make(map[string]interface{})
	appendix["agentType"] = arguments.AgentType
	if arguments.Dns {
		clientDnsInfo, dnsError := GetDnsInfo()
		if dnsError != nil {
			log.Println("[Error] Get DNS Info: ", dnsError.Error())
		} else {
			appendix["DNS"] = clientDnsInfo
		}
	}
	clientInfo.Appendix = appendix
	return clientInfo, err
}

type ClientInfoResponse struct {
	Result     string `json:"result,omitempty"`
	ResultCode int64  `json:"resultCode,omitempty"`
	ResultMsg  string `json:"resultMsg,omitempty"`
}

func SendClientInfo(url string, arguments Arguments) (err error) {
	transport := http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, time.Duration(10*time.Second))
		},
	}
	client := http.Client{
		Transport: &transport,
	}
	clientInfo, _ := GetClientInfo(arguments)
	clientInfoJson, clientJsonError := json.Marshal(clientInfo)
	if clientJsonError != nil {
		log.Println("[Error] JSON Marshal: ", clientJsonError.Error())
		return clientJsonError
	}
	var resp *http.Response
	for {
		resp, err = client.Post(url, "application/json", bytes.NewReader(clientInfoJson))
		if err == nil {
			receiveBytes, _ := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			var clientInfoResponse ClientInfoResponse
			responseUnmarshalError := json.Unmarshal(receiveBytes, &clientInfoResponse)
			if responseUnmarshalError != nil {
				log.Println("[Error] Register Response Unmarshal Error")
			} else if clientInfoResponse.ResultCode != HTTP_RESULT_OK {
				log.Println("[Error] Register Response Server Error, Result Code: ", clientInfoResponse.ResultCode)
			} else {
				log.Println("[Success] Register")
				break
			}
		} else {
			log.Println("[Warn] Register")
			if resp != nil {
				resp.Body.Close()
			}
		}
		time.Sleep(time.Duration(AgentConfiguration.HttpWait) * time.Second)
	}
	return err
}
