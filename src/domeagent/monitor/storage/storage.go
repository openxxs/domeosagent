package storage

import "domeagent/monitor/info"

type StorageDriver interface {
	AddStats(ref info.ContainerReference, stats *info.ContainerStats) error

	// Close will clear the state of the storage driver. The elements
	// stored in the underlying storage may or may not be deleted depending
	// on the implementation of the storage driver.
	Close() error
}