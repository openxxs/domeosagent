package influxdb

import (
	"fmt"
    "log"
	"net/url"
	"sync"
	"time"
    "strings"
    "encoding/json"

	info "domeagent/monitor/info"
	influxdb "github.com/influxdb/influxdb/client"
)

// The version of influxdb is 0.9.2
type influxdbStorage struct {
	client         *influxdb.Client
	machineName    string
    tableName      string
    databaseName   string
    bufferDuration time.Duration
    lastWrite      time.Time
    points         []influxdb.Point
    lock           sync.Mutex
    readyToFlush   func() bool
    filterPrefix   string
}

const (
	colMachineName        string = "machine"
	colContainerName      string = "container_name"
	colCpuCumulativeUsage string = "cpu_cumulative_usage"
	colMemoryUsage        string = "memory_usage"
	colRxBytes            string = "rx_bytes"
	colTxBytes            string = "tx_bytes"
    colDiskInfo           string = "disk_info"
)

type influxdbSamplingParams struct {
    retainDuration		string
	retentionPolicy     string
}

var InfluxdbSamplingDatabases map[uint64]influxdbSamplingParams = map[uint64]influxdbSamplingParams{
    1:{"12h", "events_12hour"},
    5:{"2d", "events_2day"},
    20:{"7d", "events_7day"},
    180:{"90d", "events_3month"},
    1440:{"1825d", "events_5year"},
}

type lastSamplingParams struct {
    startSamplingTime       time.Time
    lastSamplingTime        time.Time
    counter                 uint64
}

type FileSystem struct {
    Total  uint64        `json:"total,omitempty"`
    Usage  uint64        `json:"usage,omitempty"`
}

var lastSamplingParamsMap map[string]map[uint64]lastSamplingParams = make(map[string]map[uint64]lastSamplingParams)

func (self *influxdbStorage) getSeriesContainerName(ref info.ContainerReference) (container string){
    if len(ref.Aliases) > 0 {
        container = ref.Aliases[0]
    } else {
        container = ref.Name
    }
    return container
}

func (self *influxdbStorage) getFieldsAndTags(
ref info.ContainerReference,
stats *info.ContainerStats) (fields map[string]interface{},tags map[string]string) {

    machine := self.machineName
    container := self.getSeriesContainerName(ref)

    tags = make(map[string]string)
    tags[colMachineName] = machine
    tags[colContainerName] = container

    columns := make([]string, 0)
    values := make([]interface{}, 0)
    // Cumulative Cpu Usage
    columns = append(columns, colCpuCumulativeUsage)
    values = append(values, stats.Cpu.Usage.Total)
    // Memory Usage
    columns = append(columns, colMemoryUsage)
    values = append(values, stats.Memory.Usage)
    // Network stats.
    columns = append(columns, colRxBytes)
    values = append(values, stats.Network.RxBytes)
    columns = append(columns, colTxBytes)
    values = append(values, stats.Network.TxBytes)

    // Disk stats.
    disk := make(map[string]FileSystem)

    for _, fsStat := range stats.Filesystem {
        if !strings.HasPrefix(fsStat.Device, self.filterPrefix) {
            disk[fsStat.Device] = FileSystem{
                Total:  fsStat.Limit,
                Usage:  fsStat.Usage,
            }
        }
    }
    disk_info, err := json.Marshal(disk)
    if err != nil {
        log.Println("[Error] JSON Marshal:", err.Error())
    }
    columns = append(columns, colDiskInfo)
    values = append(values, string(disk_info))

    fields = make(map[string]interface{})
    for i, _ := range columns {
        fields[columns[i]] = values[i]
    }

    return fields, tags
}

func (self *influxdbStorage) containerStatsToPoint(
ref info.ContainerReference,
stats *info.ContainerStats) (influxdb.Point) {

    fields, tags := self.getFieldsAndTags(ref, stats)
    point := influxdb.Point{
        Measurement:    self.tableName,
        Tags:           tags,
        Time:           stats.Timestamp,
        Fields:         fields,
    }

    return point
}

func (self *influxdbStorage) OverrideReadyToFlush(readyToFlush func() bool) {
	self.readyToFlush = readyToFlush
}

func (self *influxdbStorage) defaultReadyToFlush() bool {
	return time.Since(self.lastWrite) >= self.bufferDuration
}

// Check if sampling is needed, insert sampling records into influxdb if needed
func (self *influxdbStorage) AddStatsSampling(ref info.ContainerReference, stats *info.ContainerStats) error {
    if stats == nil {
        return nil
    }
    container := self.getSeriesContainerName(ref)
    if _, ok := lastSamplingParamsMap[container]; !ok {
        lastSamplingParamsMap[container] = make(map[uint64]lastSamplingParams)
        for interval, _ := range InfluxdbSamplingDatabases {
            lastSamplingParamsMap[container][interval] = lastSamplingParams{
                startSamplingTime:      stats.Timestamp,
                lastSamplingTime:       stats.Timestamp,
                counter:                1,
            }
        }
    } else {
        for interval, _ := range InfluxdbSamplingDatabases {
            if stats.Timestamp.Sub(lastSamplingParamsMap[container][interval].startSamplingTime) >=
                time.Duration(lastSamplingParamsMap[container][interval].counter*interval)*time.Minute {
                retentionPolicy := InfluxdbSamplingDatabases[interval].retentionPolicy
                // Sampling at current time
                points := make([]influxdb.Point, 0)
                points = append(points, self.containerStatsToPoint(ref, stats))
                batchPoints := &influxdb.BatchPoints{
                    Points:     points,
                    Database:   self.databaseName,
                    RetentionPolicy:    retentionPolicy,
                }
                _, err := self.client.Write(*batchPoints)
                if err != nil {
                    return fmt.Errorf(err.Error())
                }
                modifiedLastSamplingParams := lastSamplingParams {
                    startSamplingTime:      lastSamplingParamsMap[container][interval].startSamplingTime,
                    lastSamplingTime:       stats.Timestamp,
                    counter:                lastSamplingParamsMap[container][interval].counter + 1,
                }
                lastSamplingParamsMap[container][interval] = modifiedLastSamplingParams
            }
        }
    }
    return nil
}

// Insert raw records into influxdb
func (self *influxdbStorage) AddStats(ref info.ContainerReference, stats *info.ContainerStats) error {
    if stats == nil {
        return nil
    }
    // Check if sampling is needed
    if err := self.AddStatsSampling(ref, stats); err != nil {
        return fmt.Errorf("failed to write sampling stats to influxDb - %s", err)
    }
    var pointsToFlush []influxdb.Point
    func() {
        // AddStats will be invoked simultaneously from multiple threads and only one of them will perform a write.
        self.lock.Lock()
        defer self.lock.Unlock()

        self.points = append(self.points, self.containerStatsToPoint(ref, stats))
        if self.readyToFlush() {
            pointsToFlush = self.points
            self.points = make([]influxdb.Point, 0)
            self.lastWrite = time.Now()
        }
    }()
    if len(pointsToFlush) > 0 {
        batchPoints := &influxdb.BatchPoints{
            Points:     pointsToFlush,
            Database:   self.databaseName,
            RetentionPolicy:    "default",
        }
        _, err := self.client.Write(*batchPoints)
        if err != nil {
            log.Println("failed to write raw stats1 to influxDb - %s", err.Error())
        }
    }
    return nil
}

func (self *influxdbStorage) Close() error {
	self.client = nil
	return nil
}

// machineName: A unique identifier to identify the host that the monitor is running on.
// tablename: The measurement keeping raw monitor data.
// database: The database used for all monitor data.
// influxdbHost: The host which runs influxdb.
func New(machineName, tablename, database, username, password, influxdbHost string, bufferDuration time.Duration, filterPrefix string) (*influxdbStorage, error) {
    u, err := url.Parse(fmt.Sprintf("http://%s", influxdbHost))
    if err != nil {
        return nil, err
    }

    // Connect to influxdb server
    config := &influxdb.Config{
        URL:      *u,
        Username: username,
        Password: password,
    }
    client, err := influxdb.NewClient(*config)
    if err != nil {
        return nil, err
    }

    // Create database if not existed
    existed := false
    res, err := queryDB(client, "SHOW DATABASES", database)
    if err != nil {
        return nil, err
    }
    for _, names := range res[0].Series[0].Values {
        name := names[0].(string)
        if name == database {
            existed = true
            break
        }
    }
    if existed == false {
        _, err = queryDB(client, fmt.Sprintf("CREATE DATABASE %s", database), database)
        if err != nil && err.Error() != "database already exists" {
            return nil, err
        }
    }

    // Delete all existed rps and create new rps for sampling data
    // 'rp' stands for retention policy
    retentionPolicyList := make([]string, 0)
    res, err = queryDB(client, fmt.Sprintf("SHOW RETENTION POLICIES ON %s", database), database)
    if err != nil {
        return nil, err
    }
    for _, names := range res[0].Series[0].Values {
        name := names[0].(string)
        if name != "default" {
            retentionPolicyList = append(retentionPolicyList, name)
        }
    }
    for _, name := range retentionPolicyList {
        _, err = queryDB(client, fmt.Sprintf("DROP RETENTION POLICY %s ON %s", name, database), database)
        if err != nil {
            return nil, err
        }
    }
    for _, value := range InfluxdbSamplingDatabases{
        _, err := queryDB(client, fmt.Sprintf("CREATE RETENTION POLICY %s ON %s DURATION %s REPLICATION 1", value.retentionPolicy, database, value.retainDuration), database)
        if err != nil {
            return nil, err
        }
    }

    ret := &influxdbStorage{
        client:         client,
        machineName:    machineName,
        tableName:      tablename,
        databaseName:   database,
        filterPrefix:   filterPrefix,
        bufferDuration: bufferDuration,
        lastWrite:      time.Now(),
        points:         make([]influxdb.Point, 0),
    }
    ret.readyToFlush = ret.defaultReadyToFlush
    return ret, nil
}

func queryDB(con *influxdb.Client, cmd, database string) (res []influxdb.Result, err error) {
	q := influxdb.Query{
		Command:  cmd,
		Database: database,
	}
	if response, err := con.Query(q); err == nil {
		if response.Error() != nil {
			return res, response.Error()
		}
		res = response.Results
	}
	return
}
