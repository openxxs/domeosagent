// xiaoshengxu@sohu-inc.com
// domeagent configuration

package agentmain

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"time"
)

type ConfigResponse struct {
	Result     string `json:"result,omitempty"`
	ResultCode int64  `json:"resultCode,omitempty"`
	ResultMsg  string `json:"resultMsg,omitempty"`
}

type Configuration struct {
	DockerGraph            string   `json:"dockerGraph,omitempty"`
	DockerInsecureRegistry string   `json:"dockerInsecureRegistry,omitempty"`
	DockerRegistryMirror   string   `json:"dockerRegistryMirror,omitempty"`
	DockerServerAddr       string   `json:"dockerServerAddr,omitempty"`
	GcInterval             int      `json:"gcInterval,omitempty"`
	ContainerTimeout       int      `json:"containerTimeout,omitempty"`
	ImageTimeout           int      `json:"imageTimeout,omitempty"`
	HttpWait               int      `json:"httpWait,omitempty"`
	WebsocketWait          int      `json:"websocketWait,omitempty"`
	ReportLogsInterval     int      `json:"reportLogsInterval,omitempty"`
	PingInterval           int      `json:"pingInterval,omitempty"`
	CheckContainerStatus   int      `json:"checkContainerStatus,omitempty"`
	PortScan               []string `json:"portScan,omitempty"`
	SpecialPorts           []int    `json:"specialPorts,omitempty"`
	StartPort              int      `json:"startPort,omitempty"`
	EndPort                int      `json:"endPort,omitempty"`
	ResolvConf             string   `json:"resolvConf,omitempty"`
	InstanceLogInterval    int      `json:"instanceLogInterval,omitempty"`
	InstanceLogTail        int      `json:"instanceLogTail,omitempty"`
	FsFilterPrefix         string   `json:"fsFilterPrefix,omitempty"`
	NodeFsFilter           string   `json:"nodeFsFilter,omitempty"`
}

type MonitorServer struct {
	Table                string `json:"table,omitempty"`
	Database             string `json:"database,omitempty"`
	Username             string `json:"username,omitempty"`
	Password             string `json:"password,omitempty"`
	Host                 string `json:"host,omitempty"`
	BufferDuration       int64  `json:"bufferDuration,omitempty"`
	HousekeepingInterval int64  `json:"housekeepingInterval,omitempty"`
}

var AgentConfiguration Configuration
var monitorServer MonitorServer

func setConfiguration(configUrl string, monitorUrl string) (err error) {
	transport := http.Transport{
		Dial: func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, time.Duration(10*time.Second))
		},
	}
	client := http.Client{
		Transport: &transport,
	}
	var receiveBytes []byte
	var resp *http.Response
	// get agent configuration
	resp, err = client.Get(configUrl)
	if err == nil && resp.StatusCode == http.StatusOK {
		receiveBytes, err = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err != nil {
		log.Println("[Error] Get Agent Configuration: ", err.Error())
		return err
	}
	var configResponse ConfigResponse
	err = json.Unmarshal(receiveBytes, &configResponse)
	if err != nil {
		log.Println("[Error] Agent Configuration Response Unmarshal: ", err.Error())
		return err
	}
	if configResponse.ResultCode != HTTP_RESULT_OK {
		err = fmt.Errorf(configResponse.ResultMsg)
		log.Println("[Error] Get Agent Configuration -- Server Error: ", err.Error())
		return err
	}
	err = json.Unmarshal([]byte(configResponse.Result), &AgentConfiguration)
	if err != nil {
		log.Println("[Error] Agent Configuration Unmarshal: ", err.Error())
		return err
	}
	// get influxdb related parameters
	resp, err = client.Get(monitorUrl)
	if err == nil && resp.StatusCode == http.StatusOK {
		receiveBytes, err = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err != nil {
		log.Println("[Error] Get Monitor Parameters: ", err.Error())
		return err
	}
	err = json.Unmarshal(receiveBytes, &configResponse)
	if err != nil {
		log.Println("[Error] Monitor Parameters Response Unmarshal: ", err.Error())
		return err
	}
	if configResponse.ResultCode != HTTP_RESULT_OK {
		err = fmt.Errorf(configResponse.ResultMsg)
		log.Println("[Error] Get Monitor Parameters -- Server Error: ", err.Error())
		return err
	}
	err = json.Unmarshal([]byte(configResponse.Result), &monitorServer)
	if err != nil {
		log.Println("[Error] Monitor Parameter Unmarshal: ", err.Error())
		return err
	}
	return err
}
