package container

import (
	"fmt"
	"sync"
)

type ContainerHandlerFactory interface {
	// Create a new ContainerHandler using this factory. CanHandleAndAccept() must have returned true.
	NewContainerHandler(name string, inHostNamespace bool) (c ContainerHandler, err error)

	// Returns whether this factory can handle and accept the specified container.
	CanHandleAndAccept(name string) (handle bool, accept bool, err error)

	// Name of the factory.
	String() string

	// Returns debugging information. Map of lines per category.
	DebugInfo() map[string][]string
}

// Global list of factories.
var (
	factories     []ContainerHandlerFactory
	factoriesLock sync.RWMutex
)

// Register a ContainerHandlerFactory. These should be registered from least general to most general
// as they will be asked in order whether they can handle a particular container.
func RegisterContainerHandlerFactory(factory ContainerHandlerFactory) {
	factoriesLock.Lock()
	defer factoriesLock.Unlock()

	factories = append(factories, factory)
}

// Returns whether there are any container handler factories registered.
func HasFactories() bool {
	factoriesLock.Lock()
	defer factoriesLock.Unlock()

	return len(factories) != 0
}

// Create a new ContainerHandler for the specified container.
func NewContainerHandler(name string, inHostNamespace bool) (ContainerHandler, bool, error) {
	factoriesLock.RLock()
	defer factoriesLock.RUnlock()

	// Create the ContainerHandler with the first factory that supports it.
	for _, factory := range factories {
		canHandle, canAccept, err := factory.CanHandleAndAccept(name)
		if err != nil {
			//log.Printf("[Error] Trying to work out if we can handle %s: %v", name, err)
		}
		if canHandle {
			if !canAccept {
				//log.Printf("[Error] Factory %q can handle container %q, but ignoring.", factory, name)
				return nil, false, nil
			}
			//log.Printf("[Info] Using factory %q for container %q", factory, name)
			handle, err := factory.NewContainerHandler(name, inHostNamespace)
			return handle, canAccept, err
		} else {
			//log.Printf("[Error] Factory %q was unable to handle container %q", factory, name)
		}
	}

	return nil, false, fmt.Errorf("no known factory can handle creation of container")
}