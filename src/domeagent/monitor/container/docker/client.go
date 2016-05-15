package docker

import (
    "sync"

    dclient "github.com/fsouza/go-dockerclient"
)

var (
    dockerClient    *dclient.Client
    dockerClientErr error
    once            sync.Once
)

func Client() (*dclient.Client, error) {
    once.Do(func() {
        dockerClient, dockerClientErr = dclient.NewClient(*ArgDockerEndpoint)
    })
    return dockerClient, dockerClientErr
}