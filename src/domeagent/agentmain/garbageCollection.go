// xiaoshengxu@sohu-inc.com
// garbage collection
// this function will collect the last used time of images and containers.
// image which will be monitor: created after Agent started.
// container which will be monitor: created after Agent started.

package agentmain

import (
	"github.com/fsouza/go-dockerclient"
	"io/ioutil"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"
)

type ImageInfoForGc struct {
	lastUsedTime int64
}

type ContainerInfoForGc struct {
	lastUsedTime int64
}

func GarbageCollection(goroutineWaitGroup sync.WaitGroup) {
	defer goroutineWaitGroup.Done()

	// if confFile is exist, read it and get the information of previous images and containers
	var preImageSet *Set = NewSet()     // images set for Agent re-connection
	var preContainerSet *Set = NewSet() // containers set for Agent re-connection
	_, confFileExistError := os.Stat(DOME_AGENT_CONF)
	if confFileExistError == nil || os.IsExist(confFileExistError) {
		confFileStr, confFileReadError := ioutil.ReadFile(DOME_AGENT_CONF)
		if confFileReadError == nil {
			confFileLines := strings.Split(string(confFileStr), "\n")
			for _, line := range confFileLines {
				words := strings.Split(line, " ")
				switch words[0] {
				case "IMG":
					preImageSet.Add(words[1])
				case "CON":
					preContainerSet.Add(words[1])
				}
			}
		} else {
			log.Println("[Error] Garbage Collection: ", confFileReadError.Error())
		}
	}

	var extraImageSet *Set = NewSet()     // images which are created before starting Agent
	var extraContainerSet *Set = NewSet() // containers which are created before starting Agent
	client, startClientError := docker.NewClient(AgentConfiguration.DockerServerAddr)
	for startClientError != nil {
		log.Println("[Error] Garbage Collection: ", startClientError.Error())
		time.Sleep(10 * time.Second)
		client, startClientError = docker.NewClient(AgentConfiguration.DockerServerAddr)
	}
	extraImages, listExtraImagesError := client.ListImages(docker.ListImagesOptions{All: false}) // include intermediate images if All:true
	if listExtraImagesError == nil {
		for _, extraImage := range extraImages {
			if !preImageSet.Has(extraImage.ID) {
				extraImageSet.Add(extraImage.ID)
			}
		}
	}
	extraContainers, listExtraContainersError := client.ListContainers(docker.ListContainersOptions{All: true})
	if listExtraContainersError == nil {
		for _, extraContainer := range extraContainers {
			if !preContainerSet.Has(extraContainer.ID) {
				extraContainerSet.Add(extraContainer.ID)
			}
		}
	}

	imageMap := make(map[string]ImageInfoForGc)         // images which are created during Agent is running
	containerMap := make(map[string]ContainerInfoForGc) // containers which are created during Agent is running

	for {
		// update last used time
		images, listImagesError := client.ListImages(docker.ListImagesOptions{All: false})
		if listImagesError == nil {
			for _, image := range images {
				if !extraImageSet.Has(image.ID) {
					imageInfo, imageAdded := imageMap[image.ID]
					if !imageAdded {
						imageMap[image.ID] = ImageInfoForGc{
							lastUsedTime: image.Created,
						}
					} else if imageInfo.lastUsedTime < image.Created {
						imageMap[image.ID] = ImageInfoForGc{
							lastUsedTime: image.Created,
						}
					}
				}
			}
		}
		containers, listContainersError := client.ListContainers(docker.ListContainersOptions{All: true})
		if listContainersError == nil {
			for _, container := range containers {
				containerDetail, inspectError := client.InspectContainer(container.ID)
				if inspectError == nil {
					var containerLastRunning int64 // get container last used time
					if containerDetail.State.FinishedAt.Unix() < 0 {
						containerLastRunning = time.Now().Unix() // container is running
					} else {
						containerLastRunning = containerDetail.State.FinishedAt.Unix()
					}
					imageInfo, imageAdded := imageMap[containerDetail.Image] // update related image last used time
					if imageAdded && imageInfo.lastUsedTime < containerLastRunning {
						imageMap[containerDetail.Image] = ImageInfoForGc{
							lastUsedTime: containerLastRunning,
						}
					}
					if !extraContainerSet.Has(container.ID) {
						containerInfo, containerAdded := containerMap[container.ID] // update container last used time
						if !containerAdded {
							containerMap[container.ID] = ContainerInfoForGc{
								lastUsedTime: containerLastRunning,
							}
						} else if containerInfo.lastUsedTime < containerLastRunning {
							containerMap[container.ID] = ContainerInfoForGc{
								lastUsedTime: containerLastRunning,
							}
						}
					}
				}
			}
		}
		// delete timeout containers and images (should delete containers first)
		confFileContent := ""
		containerTmpMap := make(map[string]ContainerInfoForGc)
		for containerK, containerV := range containerMap {
			if time.Now().Sub(time.Unix(containerV.lastUsedTime, 0)) < time.Duration(AgentConfiguration.ContainerTimeout)*time.Second {
				containerTmpMap[containerK] = containerV
			} else {
				removeError := client.RemoveContainer(docker.RemoveContainerOptions{ID: containerK})
				if removeError != nil {
					log.Println("[Warn] Garbage Collection: ", removeError.Error())
					if !reflect.DeepEqual(removeError, &docker.NoSuchContainer{ID: containerK}) {
						containerTmpMap[containerK] = containerV
					}
				}
			}
		}
		containerMap = make(map[string]ContainerInfoForGc)
		for containerK, containerV := range containerTmpMap {
			containerMap[containerK] = containerV
			confFileContent = confFileContent + "CON " + containerK + "\n"
		}
		imageTmpMap := make(map[string]ImageInfoForGc)
		for imageK, imageV := range imageMap {
			if time.Now().Sub(time.Unix(imageV.lastUsedTime, 0)) < time.Duration(AgentConfiguration.ImageTimeout)*time.Second {
				imageTmpMap[imageK] = imageV
			} else {
				removeError := client.RemoveImage(imageK)
				if removeError != nil {
					log.Println("[Warn] Garbage Collection: ", removeError.Error())
					if removeError != docker.ErrNoSuchImage {
						imageTmpMap[imageK] = imageV
					}
				}
			}
		}
		imageMap = make(map[string]ImageInfoForGc)
		for imageK, imageV := range imageTmpMap {
			imageMap[imageK] = imageV
			confFileContent = confFileContent + "IMG " + imageK + "\n"
		}
		if len(confFileContent) > 0 {
			confFileContent = confFileContent[0 : len(confFileContent)-1] // delete last "\n"
		}
		// update confFile
		confFile, confFileError := os.OpenFile(DOME_AGENT_CONF, os.O_RDONLY|os.O_CREATE, 0666)
		if confFileError != nil {
			log.Println("[Error] Garbage Collection: ", confFileError.Error())
		}
		if confFile != nil {
			confFile.Close()
		}
		writeFileError := ioutil.WriteFile(DOME_AGENT_CONF, []byte(confFileContent), os.ModeAppend)
		if writeFileError != nil {
			log.Println("[Error] Garbage Collection: ", writeFileError.Error())
		}
		time.Sleep(time.Duration(AgentConfiguration.GcInterval) * time.Second)
	}
}
