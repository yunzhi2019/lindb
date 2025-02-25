package memdb

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/lindb/lindb/pkg/logger"
	"github.com/lindb/lindb/pkg/timeutil"
	pb "github.com/lindb/lindb/rpc/proto/field"
	"github.com/lindb/lindb/series"
	"github.com/lindb/lindb/sql/stmt"
	"github.com/lindb/lindb/tsdb/metadb"
	"github.com/lindb/lindb/tsdb/tblstore/forwardindex"
	"github.com/lindb/lindb/tsdb/tblstore/invertedindex"
	"github.com/lindb/lindb/tsdb/tblstore/metricsdata"

	"github.com/RoaringBitmap/roaring"
	"github.com/cespare/xxhash"
	"go.uber.org/atomic"
)

var memDBLogger = logger.GetLogger("tsdb", "MemDB")

//go:generate mockgen -source ./database.go -destination=./database_mock.go -package memdb

// MemoryDatabase is a database-like concept of Shard as memTable in cassandra.
type MemoryDatabase interface {
	// WithMaxTagsLimit spawn a goroutine to receives limitation from this channel
	// The producer shall send the config periodically
	// key: metric-name, value: max-limit
	WithMaxTagsLimit(<-chan map[string]uint32)
	// Write writes metrics to the memory-database,
	// return error on exceeding max count of tagsIdentifier or writing failure
	Write(metric *pb.Metric) error
	// ResetMetricStore reassigns a new version to metricStore
	// This method provides the ability to reset the tsStore in memory for skipping the tsID-limitation
	ResetMetricStore(metricName string) error
	// CountMetrics returns the metrics-count of the memory-database
	CountMetrics() int
	// CountTags returns the tags-count of the metricName, return -1 if not exist
	CountTags(metricName string) int
	// Families returns the families in memory which has not been flushed yet
	Families() []int64
	// FlushInvertedIndexTo flushes the inverted-index of series to the kv builder
	FlushInvertedIndexTo(flusher invertedindex.Flusher) error
	// FlushFamilyTo flushes the corresponded family data to builder.
	// Close is not in the flushing process.
	FlushFamilyTo(flusher metricsdata.Flusher, familyTime int64) error
	// FlushForwardIndexTo flushes the forward-index of series to the kv builder
	FlushForwardIndexTo(flusher forwardindex.Flusher) error
	// MemSize returns the memory-size of this metric-store
	MemSize() int
	// series.Filter contains the methods for filtering seriesIDs from memDB
	series.Filter
	// series.MetaGetter returns tag values by tag keys and spec version for metric level
	series.MetaGetter
	// series.Suggester returns the suggestions from prefix string
	series.MetricMetaSuggester
	series.TagValueSuggester
	// series.Scanner scans metric-data
	series.Scanner
	// series.Storage returns the high level function of storage
	series.Storage
}

// mStoresBucket is a simple rwMutex locked map of metricStore.
type mStoresBucket struct {
	rwLock      sync.RWMutex          // read-write lock of hash2MStore
	hash2MStore map[uint64]mStoreINTF // key: FNV64a(metric-name)
}

func newMStoreBucket() *mStoresBucket {
	return &mStoresBucket{
		hash2MStore: make(map[uint64]mStoreINTF)}
}

// allMetricStores returns a clone of metric-hashes and pointer of mStores in bucket.
func (bkt *mStoresBucket) allMetricStores() (metricHashes []uint64, stores []mStoreINTF) {
	bkt.rwLock.RLock()
	length := len(bkt.hash2MStore)
	metricHashes = make([]uint64, length)
	stores = make([]mStoreINTF, length)
	idx := 0
	for metricHash, mStore := range bkt.hash2MStore {
		// delete tag of tStore which has not been used for a while
		metricHashes[idx] = metricHash
		stores[idx] = mStore
		idx++
	}
	bkt.rwLock.RUnlock()
	return
}

// MemoryDatabaseCfg represents the memory database config
type MemoryDatabaseCfg struct {
	TimeWindow int
	Interval   timeutil.Interval
	Generator  metadb.IDGenerator
}

// memoryDatabase implements MemoryDatabase.
type memoryDatabase struct {
	timeWindow          int                                    // rollup window of memory-database
	interval            timeutil.Interval                      // time interval of rollup
	blockStore          *blockStore                            // reusable pool
	ctx                 context.Context                        // used for exiting goroutines
	evictNotifier       chan struct{}                          // notifying evictor to evict
	once4Syncer         sync.Once                              // once for tags-limitation syncer
	metricID2Hash       sync.Map                               // key: metric-id(uint32), value: hash(uint64)
	mStoresList         [shardingCountOfMStores]*mStoresBucket // metric-name -> *metricStore
	generator           metadb.IDGenerator                     // the generator for generating ID of metric, field
	size                atomic.Int32                           // memdb's size
	lastWroteFamilyTime atomic.Int64                           // prevents familyTime inserting repeatedly
	familyTimes         sync.Map                               // familyTime(int64) -> struct{}
}

// NewMemoryDatabase returns a new MemoryDatabase.
func NewMemoryDatabase(ctx context.Context, cfg MemoryDatabaseCfg) MemoryDatabase {
	md := memoryDatabase{
		timeWindow:          cfg.TimeWindow,
		interval:            cfg.Interval,
		generator:           cfg.Generator,
		blockStore:          newBlockStore(cfg.TimeWindow),
		ctx:                 ctx,
		evictNotifier:       make(chan struct{}),
		size:                *atomic.NewInt32(0),
		lastWroteFamilyTime: *atomic.NewInt64(0),
	}
	for i := range md.mStoresList {
		md.mStoresList[i] = newMStoreBucket()
	}
	go md.evictor(ctx)
	return &md
}

// getBucket returns the mStoresBucket by metric-hash.
func (md *memoryDatabase) getBucket(metricHash uint64) *mStoresBucket {
	return md.mStoresList[shardingCountMask&metricHash]
}

// getMStore returns the mStore by metric-name.
func (md *memoryDatabase) getMStore(metricName string) (mStore mStoreINTF, ok bool) {
	return md.getMStoreByMetricHash(xxhash.Sum64String(metricName))
}

// getMStoreByMetricHash returns the mStore by metric-hash.
func (md *memoryDatabase) getMStoreByMetricHash(hash uint64) (mStore mStoreINTF, ok bool) {
	bkt := md.getBucket(hash)
	bkt.rwLock.RLock()
	mStore, ok = bkt.hash2MStore[hash]
	bkt.rwLock.RUnlock()
	return
}

// getMStoreByMetricID returns the mStore by metricID.
func (md *memoryDatabase) getMStoreByMetricID(metricID uint32) (mStore mStoreINTF, ok bool) {
	item, ok := md.metricID2Hash.Load(metricID)
	if !ok {
		return nil, false
	}
	return md.getMStoreByMetricHash(item.(uint64))
}

// getOrCreateMStore returns the mStore by metricHash.
func (md *memoryDatabase) getOrCreateMStore(metricName string, hash uint64) mStoreINTF {
	var mStore mStoreINTF
	mStore, ok := md.getMStoreByMetricHash(hash)
	if !ok {
		metricID := md.generator.GenMetricID(metricName)

		bucket := md.getBucket(hash)
		bucket.rwLock.Lock()
		mStore, ok = bucket.hash2MStore[hash]
		if !ok {
			mStore = newMetricStore(metricID)
			md.size.Add(int32(mStore.MemSize()))
			bucket.hash2MStore[hash] = mStore
			md.metricID2Hash.Store(metricID, hash)
		}
		bucket.rwLock.Unlock()
	}
	return mStore
}

// WithMaxTagsLimit syncs the limitation for different metrics.
func (md *memoryDatabase) WithMaxTagsLimit(limitationCh <-chan map[string]uint32) {
	md.once4Syncer.Do(func() {
		go func() {
			for {
				select {
				case <-md.ctx.Done():
					return
				case limitations, ok := <-limitationCh:
					if !ok {
						return
					}
					if limitations == nil {
						continue
					}
					md.setLimitations(limitations)
				}
			}
		}()
	})
}

// setLimitations set max-count limitation of tagID.
func (md *memoryDatabase) setLimitations(limitations map[string]uint32) {
	for metricName, limit := range limitations {
		mStore, ok := md.getMStore(metricName)
		if !ok {
			continue
		}
		mStore.SetMaxTagsLimit(limit)
	}
}

// writeContext holds the context for writing
type writeContext struct {
	blockStore   *blockStore
	generator    metadb.IDGenerator
	metricID     uint32
	familyTime   int64
	slotIndex    int
	timeInterval int64
	mStoreFieldIDGetter
}

// PointTime returns the point time
func (writeCtx writeContext) PointTime() int64 {
	return writeCtx.familyTime + writeCtx.timeInterval*int64(writeCtx.slotIndex)
}

func (md *memoryDatabase) addFamilyTime(familyTime int64) {
	if md.lastWroteFamilyTime.Swap(familyTime) == familyTime {
		return
	}
	md.familyTimes.Store(familyTime, struct{}{})
}

// Write writes metric-point to database.
func (md *memoryDatabase) Write(metric *pb.Metric) error {
	timestamp := metric.Timestamp
	// calculate family start time and slot index
	intervalCalc := md.interval.Calculator()
	segmentTime := intervalCalc.CalcSegmentTime(timestamp)                         // day
	family := intervalCalc.CalcFamily(timestamp, segmentTime)                      // hours
	familyTime := intervalCalc.CalcFamilyStartTime(segmentTime, family)            // family timestamp
	slotIndex := intervalCalc.CalcSlot(timestamp, familyTime, md.interval.Int64()) // slot offset of family

	hash := xxhash.Sum64String(metric.Name)
	mStore := md.getOrCreateMStore(metric.Name, hash)

	writtenSize, err := mStore.Write(metric, writeContext{
		metricID:            mStore.GetMetricID(),
		blockStore:          md.blockStore,
		generator:           md.generator,
		familyTime:          familyTime,
		slotIndex:           slotIndex,
		timeInterval:        md.interval.Int64(),
		mStoreFieldIDGetter: mStore})
	if err == nil {
		md.addFamilyTime(familyTime)
	}
	md.size.Add(int32(writtenSize))
	return err
}

// evictor do evict periodically.
func (md *memoryDatabase) evictor(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-md.evictNotifier:
			for i := 0; i < shardingCountOfMStores; i++ {
				md.evict(md.mStoresList[i&shardingCountMask])
			}
		}
	}
}

// evict evicts tsStore of mStore concurrently,
// and delete metricStore whose timeSeriesMap is empty.
func (md *memoryDatabase) evict(bucket *mStoresBucket) {
	// get all allMStores
	metricHashes, allMStores := bucket.allMetricStores()

	for idx, mStore := range allMStores {
		// delete tag of tStore which has not been used for a while
		evictedSize := mStore.Evict()
		// reduce evicted size
		md.size.Sub(int32(evictedSize))
		// delete mStore whose tags is empty now.
		if mStore.IsEmpty() {
			bucket.rwLock.Lock()
			if mStore.IsEmpty() {
				delete(bucket.hash2MStore, metricHashes[idx])
				md.metricID2Hash.Delete(mStore.GetMetricID())
			}
			// reduce empty mstore size
			md.size.Sub(int32(mStore.MemSize()))
			bucket.rwLock.Unlock()
		}
	}
}

// ResetMetricStore assigns a new version to the specified metric.
func (md *memoryDatabase) ResetMetricStore(metricName string) error {
	mStore, ok := md.getMStore(metricName)
	if !ok {
		return fmt.Errorf("metric: %s doesn't exist", metricName)
	}
	createdSize, err := mStore.ResetVersion()
	md.size.Add(int32(createdSize))
	return err
}

// CountMetrics returns count of metrics in all buckets.
func (md *memoryDatabase) CountMetrics() int {
	var counter = 0
	for bucketIndex := 0; bucketIndex < shardingCountOfMStores; bucketIndex++ {
		md.mStoresList[bucketIndex].rwLock.RLock()
		counter += len(md.mStoresList[bucketIndex].hash2MStore)
		md.mStoresList[bucketIndex].rwLock.RUnlock()
	}
	return counter
}

// CountTags returns count of tags of a specified metricName, return -1 when metric not exist.
func (md *memoryDatabase) CountTags(metricName string) int {
	mStore, ok := md.getMStore(metricName)
	if !ok {
		return -1
	}
	return mStore.GetTagsUsed()
}

// Families returns the families in memory which has not been flushed yet.
func (md *memoryDatabase) Families() []int64 {
	var families []int64
	md.familyTimes.Range(func(key, value interface{}) bool {
		familyTime := key.(int64)
		families = append(families, familyTime)
		return true
	})
	sort.Slice(families, func(i, j int) bool {
		return families[i] < families[j]
	})
	return families
}

// flushContext holds the context for flushing
type flushContext struct {
	metricID     uint32
	familyTime   int64
	timeInterval int64
}

// FlushFamilyTo flushes all data related to the family from metric-stores to builder,
func (md *memoryDatabase) FlushFamilyTo(flusher metricsdata.Flusher, familyTime int64) error {
	defer func() {
		// non-block notifying evictor
		select {
		case md.evictNotifier <- struct{}{}:
		default:
			memDBLogger.Warn("flusher is working, concurrently flushing is not allowed")
		}
	}()

	md.familyTimes.Delete(familyTime)
	md.lastWroteFamilyTime.Store(0)

	for bucketIndex := 0; bucketIndex < shardingCountOfMStores; bucketIndex++ {
		bkt := md.mStoresList[bucketIndex]

		_, allMetricStores := bkt.allMetricStores()
		for _, mStore := range allMetricStores {
			flushedSize, err := mStore.FlushMetricsDataTo(flusher, flushContext{
				metricID:     mStore.GetMetricID(),
				familyTime:   familyTime,
				timeInterval: md.interval.Int64(),
			})
			md.size.Sub(int32(flushedSize))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// FlushInvertedIndexTo flushes the series data to a inverted-index file.
func (md *memoryDatabase) FlushInvertedIndexTo(flusher invertedindex.Flusher) error {
	var err error
	for bucketIndex := 0; bucketIndex < shardingCountOfMStores; bucketIndex++ {
		bkt := md.mStoresList[bucketIndex]
		_, allMetricStores := bkt.allMetricStores()
		for _, mStore := range allMetricStores {
			if err = mStore.FlushInvertedIndexTo(flusher, md.generator); err != nil {
				return err
			}
		}
	}
	return nil
}

// FlushForwardIndexTo flushes the forward-index of series to a forward-index file
func (md *memoryDatabase) FlushForwardIndexTo(flusher forwardindex.Flusher) error {
	var err error
	for bucketIndex := 0; bucketIndex < shardingCountOfMStores; bucketIndex++ {
		bkt := md.mStoresList[bucketIndex]
		_, allMetricStores := bkt.allMetricStores()
		for _, mStore := range allMetricStores {
			if err = mStore.FlushForwardIndexTo(flusher); err != nil {
				return err
			}
		}
	}
	return nil
}

// FindSeriesIDsByExpr finds series ids by tag filter expr for metric id from mStore.
func (md *memoryDatabase) FindSeriesIDsByExpr(
	metricID uint32,
	expr stmt.TagFilter,
	timeRange timeutil.TimeRange,
) (
	*series.MultiVerSeriesIDSet,
	error,
) {

	mStore, ok := md.getMStoreByMetricID(metricID)
	if !ok {
		return nil, series.ErrNotFound
	}
	return mStore.FindSeriesIDsByExpr(expr)
}

// GetSeriesIDsForTag get series ids for spec metric's tag key from mStore.
func (md *memoryDatabase) GetSeriesIDsForTag(
	metricID uint32,
	tagKey string,
	timeRange timeutil.TimeRange,
) (
	*series.MultiVerSeriesIDSet,
	error,
) {

	mStore, ok := md.getMStoreByMetricID(metricID)
	if !ok {
		return nil, series.ErrNotFound
	}
	return mStore.GetSeriesIDsForTag(tagKey)
}

// GetTagValues returns tag values by tag keys and spec version for metric level from memory-database
func (md *memoryDatabase) GetTagValues(
	metricID uint32,
	tagKeys []string,
	version series.Version,
	seriesIDs *roaring.Bitmap,
) (
	seriesID2TagValues map[uint32][]string,
	err error,
) {
	// get hash of metricId
	mStore, ok := md.getMStoreByMetricID(metricID)
	if !ok {
		return nil, series.ErrNotFound
	}
	return mStore.GetTagValues(tagKeys, version, seriesIDs)
}

// SuggestMetrics returns nil, as the index-db contains all metricNames
func (md *memoryDatabase) SuggestMetrics(prefix string, limit int) (suggestions []string) {
	return nil
}

// SuggestTagKeys returns suggestions from given metricName and prefix of tagKey
func (md *memoryDatabase) SuggestTagKeys(metricName, tagKeyPrefix string, limit int) []string {
	mStore, ok := md.getMStore(metricName)
	if !ok {
		return nil
	}
	return mStore.SuggestTagKeys(tagKeyPrefix, limit)
}

// SuggestTagValues returns suggestions from given metricName, tagKey and prefix of tagValue
func (md *memoryDatabase) SuggestTagValues(metricName, tagKey, tagValuePrefix string, limit int) []string {
	mStore, ok := md.getMStore(metricName)
	if !ok {
		return nil
	}
	return mStore.SuggestTagValues(tagKey, tagValuePrefix, limit)
}

// Scan scans data from memory by scan-context
func (md *memoryDatabase) Scan(sCtx *series.ScanContext) {
	mStore, ok := md.getMStoreByMetricID(sCtx.MetricID)
	if ok {
		sCtx.IntervalCalc = md.interval.Calculator()
		mStore.Scan(sCtx)
	}
}

// Interval return the interval of memory database
func (md *memoryDatabase) Interval() int64 {
	return md.interval.Int64()
}

func (md *memoryDatabase) MemSize() int {
	return int(md.size.Load())
}
