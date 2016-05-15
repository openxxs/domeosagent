package docker

import (
    "sync"
    "time"

    "domeagent/monitor/fs"
)

type fsHandler interface {
    start()
    usage() uint64
    stop()
}

type realFsHandler struct {
    sync.RWMutex
    lastUpdate  time.Time
    usageBytes  uint64
    period      time.Duration
    storageDirs []string
    fsInfo      fs.FsInfo
    // Tells the container to stop.
    stopChan chan struct{}
}

const longDu = time.Second

var _ fsHandler = &realFsHandler{}

func newFsHandler(period time.Duration, storageDirs []string, fsInfo fs.FsInfo) fsHandler {
    return &realFsHandler{
        lastUpdate:  time.Time{},
        usageBytes:  0,
        period:      period,
        storageDirs: storageDirs,
        fsInfo:      fsInfo,
        stopChan:    make(chan struct{}, 1),
    }
}

func (fh *realFsHandler) needsUpdate() bool {
    return time.Now().After(fh.lastUpdate.Add(fh.period))
}

func (fh *realFsHandler) update() error {
    var usage uint64
    for _, dir := range fh.storageDirs {
        dirUsage, err := fh.fsInfo.GetDirUsage(dir)
        if err != nil {
            return err
        }
        usage += dirUsage
    }
    fh.Lock()
    defer fh.Unlock()
    fh.lastUpdate = time.Now()
    fh.usageBytes = usage
    return nil
}

func (fh *realFsHandler) trackUsage() {
    for {
        select {
        case <-fh.stopChan:
            return
        case <-time.After(fh.period):
            start := time.Now()
            if err := fh.update(); err != nil {
                //Infof("failed to collect filesystem stats - %v", err)
            }
            duration := time.Since(start)
            if duration > longDu {
                //Infof("`du` on following dirs took %v: %v", duration, fh.storageDirs)
            }
        }
    }
}

func (fh *realFsHandler) start() {
    go fh.trackUsage()
}

func (fh *realFsHandler) stop() {
    close(fh.stopChan)
}

func (fh *realFsHandler) usage() uint64 {
    fh.RLock()
    defer fh.RUnlock()
    return fh.usageBytes
}