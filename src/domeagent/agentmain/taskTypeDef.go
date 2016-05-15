// xiaoshengxu@sohu-inc.com
// Process Tasks from server

package agentmain

import (
	"encoding/json"
	"github.com/fsouza/go-dockerclient"
)

type FeedBack struct {
	Type string      `json:"type,omitempty"`
	Data interface{} `json:"data,omitempty"`
}

type PingFeedBack struct {
	Ports []int `json:"ports,omitempty"`
}

type TaskFeedBack struct {
	Phase      string            `json:"phase,omitempty"`
	NodeTaskId int64             `json:"nodeTaskId,omitempty"`
	ResultCode int64             `json:"resultCode,omitempty"`
	ResultMsg  string            `json:"resultMsg,omitempty"`
	Result     map[string]string `json:"result,omitempty"`
}

type InstanceFeedBack struct {
	Phase         string      `json:"phase,omitempty"`
	ContainerId   string      `json:"containerId,omitempty"`
	ContainerName string      `json:"containerName,omitempty"`
	Addon         interface{} `json:"addOn,omitempty"`
}

type InstanceDownAddon struct {
	Port      string `json:"port,omitempty"`
	FailTimes int    `json:"failTimes,omitempty"`
}

type InstanceStopAddon struct {
	ExitedCode int    `json:"exitedCode,omitempty"`
	ExitedMsg  string `json:"exitedMsg,omitempty"`
}

type InstanceLogsFeedBack struct {
	SessionId     string `json:"sessionId,omitempty"`
	ContainerId   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
	Logs          string `json:"logs,omitempty"`
}

type HealthCheckInfo struct {
	CheckType         string `json:"checkType,omitempty"`
	Port              string `json:"port,omitempty"`
	CheckPath         string `json:"checkPath,omitempty"`
	AllowResponseCode string `json:"allowResponseCode,omitempty"`
	TimeoutMs         int    `json:"timeoutMs,omitempty"`
	CheckIntervalMs   int    `json:"checkIntervalMs,omitempty"`
	CheckTimes        int    `json:"checkTimes,omitempty"`
}

type NodeTask struct {
	NodeTaskId int64  `json:"nodeTaskId,omitempty"`
	TaskType   string `json:"taskType,omitempty"`
	Args       *json.RawMessage
}

type CreateTask struct {
	Hostname        string                          `json:"hostname,omitempty"`
	User            string                          `json:"user,omitempty"`
	Name            string                          `json:"name,omitempty"`
	HealthCheckInfo HealthCheckInfo                 `json:"healthCheckInfo,omitempty"`
	Memory          int64                           `json:"memory,omitempty"`
	CpuShares       int64                           `json:"cpuShares,omitempty"`
	Env             []string                        `json:"env,omitempty"`
	Cmd             []string                        `json:"cmd,omitempty"`
	Dns             []string                        `json:"dns,omitempty"`
	DnsSearch       []string                        `json:"dnsSearch,omitempty"`
	Image           string                          `json:"image,omitempty"`
	Volumes         []string                        `json:"volumes,omitempty"`
	VolumesFrom     []string                        `json:"volumesFrom,omitempty"`
	WorkingDir      string                          `json:"workingDir,omitempty"`
	Entrypoint      []string                        `json:"entrypoint,omitempty"`
	Labels          map[string]string               `json:"labels,omitempty"`
	Binds           []string                        `json:"binds,omitempty"`
	ExposedPorts    []string                        `json:"exposedPorts,omitempty"`
	Privileged      bool                            `json:"privileged,omitempty"`
	PortBindings    map[string][]docker.PortBinding `json:"portBindings,omitempty"`
	Links           []string                        `json:"links,omitempty"`
	PublishAllPorts bool                            `json:"publishAllPorts,omitempty"`
	ExtraHosts      []string                        `json:"extraHosts,omitempty"`
	NetworkMode     string                          `json:"networkMode,omitempty"`
	RestartPolicy   docker.RestartPolicy            `json:"restartPolicy,omitempty"`
	CpusetCpus      string                          `json:"cpusetCpus,omitempty"`
	Args            *json.RawMessage
}

type RemoveTask struct {
	ContainerId   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
	RemoveVolumes bool   `json:"removeVolumes,omitempty"`
	Force         bool   `json:"force,omitempty"`
}

type StartTask struct {
	ContainerId     string          `json:"containerId,omitempty"`
	ContainerName   string          `json:"containerName,omitempty"`
	HealthCheckInfo HealthCheckInfo `json:"healthCheckInfo,omitempty"`
}

type StopTask struct {
	ContainerId   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
	TimeWait      uint   `json:"timeWait,omitempty"`
}

type RestartTask struct {
	ContainerId     string          `json:"containerId,omitempty"`
	ContainerName   string          `json:"containerName,omitempty"`
	TimeWait        uint            `json:"timeWait,omitempty"`
	HealthCheckInfo HealthCheckInfo `json:"healthCheckInfo,omitempty"`
}

type PullTask struct {
	Repository string `json:"repository,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Registry   string `json:"registry,omitempty"`
	Args       *json.RawMessage
}

type LogsTask struct {
	StartReport   bool   `json:"startReport,omitempty"`
	SessionId     string `json:"sessionId,omitempty"`
	ContainerId   string `json:"containerId,omitempty"`
	ContainerName string `json:"containerName,omitempty"`
}
