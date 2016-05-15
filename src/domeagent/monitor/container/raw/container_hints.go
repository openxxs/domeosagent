package raw

import (
	"encoding/json"
	"io/ioutil"
	"os"
)

var argContainerHints = "/etc/cadvisor/container_hints.json"

type containerHints struct {
	AllHosts []containerHint `json:"all_hosts,omitempty"`
}

type containerHint struct {
	FullName         string            `json:"full_path,omitempty"`
	NetworkInterface *networkInterface `json:"network_interface,omitempty"`
	Mounts           []mount           `json:"mounts,omitempty"`
}

type mount struct {
	HostDir      string `json:"host_dir,omitempty"`
	ContainerDir string `json:"container_dir,omitempty"`
}

type networkInterface struct {
	VethHost  string `json:"veth_host,omitempty"`
	VethChild string `json:"veth_child,omitempty"`
}

func getContainerHintsFromFile(containerHintsFile string) (containerHints, error) {
	dat, err := ioutil.ReadFile(containerHintsFile)
	if os.IsNotExist(err) {
		return containerHints{}, nil
	}
	var cHints containerHints
	if err == nil {
		err = json.Unmarshal(dat, &cHints)
	}

	return cHints, err
}