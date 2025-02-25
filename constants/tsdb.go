package constants

const (
	// use this limit of metric-store when maxTagsLimit is not set
	DefaultMStoreMaxTagsCount = 10000000
	// max tag keys limitation of a metric-store
	MStoreMaxTagKeysCount = 512
	// max fields limitation of a tsStore.
	TStoreMaxFieldsCount = 1024
	// the max number of suggestions count
	MaxSuggestions = 10000

	// Check if the global memory usage is greater than the limit,
	// If so, engine will flush the biggest shard's memdb until we are down to the lower mark.
	MemoryHighWaterMark = 80
	MemoryLowWaterMark  = 60
	// Check if shard's memory usage is greater than this limit,
	// If so, engine will flush this shard to disk
	ShardMemoryUsedThreshold = 500 * 1024 * 1024
	// FlushConcurrency controls the concurrent number of flushers
	FlushConcurrency = 4
)
