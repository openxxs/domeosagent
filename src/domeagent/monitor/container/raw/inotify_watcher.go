package raw

import (
	"sync"

	"golang.org/x/exp/inotify"
)

// Watcher for container-related inotify events in the cgroup hierarchy.
//
// Implementation is thread-safe.
type InotifyWatcher struct {
	// Underlying inotify watcher.
	watcher *inotify.Watcher

	// Map of containers being watched to cgroup paths watched for that container.
	containersWatched map[string]map[string]bool

	// Lock for all datastructure access.
	lock sync.Mutex
}

func NewInotifyWatcher() (*InotifyWatcher, error) {
	w, err := inotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &InotifyWatcher{
		watcher:           w,
		containersWatched: make(map[string]map[string]bool),
	}, nil
}

// Add a watch to the specified directory. Returns if the container was already being watched.
func (iw *InotifyWatcher) AddWatch(containerName, dir string) (bool, error) {
	iw.lock.Lock()
	defer iw.lock.Unlock()

	cgroupsWatched, alreadyWatched := iw.containersWatched[containerName]

	// Register an inotify notification.
	if !cgroupsWatched[dir] {
		err := iw.watcher.AddWatch(dir, inotify.IN_CREATE|inotify.IN_DELETE|inotify.IN_MOVE)
		if err != nil {
			return alreadyWatched, err
		}

		if cgroupsWatched == nil {
			cgroupsWatched = make(map[string]bool)
		}
		cgroupsWatched[dir] = true
	}

	// Record our watching of the container.
	if !alreadyWatched {
		iw.containersWatched[containerName] = cgroupsWatched
	}
	return alreadyWatched, nil
}

// Remove watch from the specified directory. Returns if this was the last watch on the specified container.
func (iw *InotifyWatcher) RemoveWatch(containerName, dir string) (bool, error) {
	iw.lock.Lock()
	defer iw.lock.Unlock()

	// If we don't have a watch registed for this, just return.
	cgroupsWatched, ok := iw.containersWatched[containerName]
	if !ok {
		return false, nil
	}

	// Remove the inotify watch if it exists.
	if cgroupsWatched[dir] {
		err := iw.watcher.RemoveWatch(dir)
		if err != nil {
			return false, nil
		}
		delete(cgroupsWatched, dir)
	}

	// Remove the record if this is the last watch.
	if len(cgroupsWatched) == 0 {
		delete(iw.containersWatched, containerName)
		return true, nil
	}

	return false, nil
}

// Errors are returned on this channel.
func (iw *InotifyWatcher) Error() chan error {
	return iw.watcher.Error
}

// Events are returned on this channel.
func (iw *InotifyWatcher) Event() chan *inotify.Event {
	return iw.watcher.Event
}

// Closes the inotify watcher.
func (iw *InotifyWatcher) Close() error {
	return iw.watcher.Close()
}

// Returns a map of containers to the cgroup paths being watched.
func (iw *InotifyWatcher) GetWatches() map[string][]string {
	out := make(map[string][]string, len(iw.containersWatched))
	for k, v := range iw.containersWatched {
		out[k] = mapToSlice(v)
	}
	return out
}

func mapToSlice(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}