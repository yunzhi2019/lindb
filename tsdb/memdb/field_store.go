package memdb

import (
	"sort"

	"github.com/lindb/lindb/aggregation"
	"github.com/lindb/lindb/pkg/logger"
	"github.com/lindb/lindb/pkg/timeutil"
	pb "github.com/lindb/lindb/rpc/proto/field"
	"github.com/lindb/lindb/series/field"
	"github.com/lindb/lindb/tsdb/tblstore/metricsdata"
)

//go:generate mockgen -source ./field_store.go -destination=./field_store_mock_test.go -package memdb

const emptyFieldStoreSize = 2 + // fieldID
	24 // sStoreNodes

// fStoreINTF abstracts a field-store
type fStoreINTF interface {
	// GetSStore gets the sStore from list by familyTime.
	GetSStore(familyTime int64) (sStoreINTF, bool)

	// GetFieldID returns the fieldID
	GetFieldID() uint16

	// Write writes the metric's field with writeContext
	Write(
		f *pb.Field,
		writeCtx writeContext,
	) (
		writtenSize int)

	// FlushFieldTo flushes field data of the specific familyTime
	// return false if there is no data related of familyTime
	FlushFieldTo(
		tableFlusher metricsdata.Flusher,
		familyTime int64,
	) (flushedSize int)

	// TimeRange returns the start-time and end-time of fStore's data
	// ok means data is available
	TimeRange(
		interval int64,
	) (
		timeRange timeutil.TimeRange,
		ok bool)

	// SegmentsCount returns the count of segments
	SegmentsCount() int

	MemSize() int

	// scan scans the field store's data
	scan(agg aggregation.SeriesAggregator, memScanCtx *memScanContext)
}

// sStoreNodes implements the sort.Interface
type sStoreNodes []sStoreINTF

func (s sStoreNodes) Len() int           { return len(s) }
func (s sStoreNodes) Less(i, j int) bool { return s[i].GetFamilyTime() < s[j].GetFamilyTime() }
func (s sStoreNodes) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// fieldStore holds the relation of familyStartTime and segmentStore.
// there are only a few familyTimes in the segments,
// add delete operation occurs every one hour
// so slice is more cheaper than the map
type fieldStore struct {
	fieldID     uint16      // generated by id generator
	sStoreNodes sStoreNodes // sorted sStore list by family-time
}

// newFieldStore returns a new fieldStore.
func newFieldStore(fieldID uint16) fStoreINTF { return &fieldStore{fieldID: fieldID} }

// getFieldID returns the fieldID
func (fs *fieldStore) GetFieldID() uint16 { return fs.fieldID }

// SegmentsCount returns the count of segments
func (fs *fieldStore) SegmentsCount() int { return len(fs.sStoreNodes) }

// GetSStore gets the sStore from list by familyTime.
func (fs *fieldStore) GetSStore(familyTime int64) (sStoreINTF, bool) {
	idx := sort.Search(len(fs.sStoreNodes), func(i int) bool {
		return fs.sStoreNodes[i].GetFamilyTime() >= familyTime
	})
	if idx >= len(fs.sStoreNodes) || fs.sStoreNodes[idx].GetFamilyTime() != familyTime {
		return nil, false
	}
	return fs.sStoreNodes[idx], true
}

// removeSStore removes the sStore by familyTime.
func (fs *fieldStore) removeSStore(familyTime int64) {
	idx := sort.Search(len(fs.sStoreNodes), func(i int) bool {
		return fs.sStoreNodes[i].GetFamilyTime() >= familyTime
	})
	// familyTime greater than existed
	if idx == len(fs.sStoreNodes) {
		return
	}
	// not match
	if fs.sStoreNodes[idx].GetFamilyTime() != familyTime {
		return
	}
	copy(fs.sStoreNodes[idx:], fs.sStoreNodes[idx+1:])
	// fills the tail with nil
	fs.sStoreNodes[len(fs.sStoreNodes)-1] = nil
	fs.sStoreNodes = fs.sStoreNodes[:len(fs.sStoreNodes)-1]
}

// insertSStore inserts a new sStore to segments.
func (fs *fieldStore) insertSStore(sStore sStoreINTF) {
	fs.sStoreNodes = append(fs.sStoreNodes, sStore)
	sort.Sort(fs.sStoreNodes)
}

func (fs *fieldStore) Write(
	f *pb.Field,
	writeCtx writeContext,
) (
	writtenSize int,
) {
	sStore, ok := fs.GetSStore(writeCtx.familyTime)

	switch fields := f.Field.(type) {
	case *pb.Field_Sum:
		if !ok {
			//TODO ???
			oldCap := cap(fs.sStoreNodes)
			sStore = newSimpleFieldStore(writeCtx.familyTime, field.Sum.AggFunc())
			fs.insertSStore(sStore)
			writtenSize += (cap(fs.sStoreNodes)-oldCap)*8 + sStore.MemSize()
		}
		writtenSize += sStore.WriteFloat(fields.Sum.Value, writeCtx)
	default:
		memDBLogger.Warn("convert field error, unknown field type")
	}
	return writtenSize
}

// FlushFieldTo flushes segments' data to writer and reset the segments-map.
func (fs *fieldStore) FlushFieldTo(
	tableFlusher metricsdata.Flusher,
	familyTime int64,
) (
	flushedSize int,
) {
	sStore, ok := fs.GetSStore(familyTime)

	if !ok {
		return 0
	}

	fs.removeSStore(familyTime)
	data, _, _, err := sStore.Bytes(true)

	if err != nil {
		memDBLogger.Error("read segment data error:", logger.Error(err))
		return 0
	}
	tableFlusher.FlushField(fs.fieldID, data)
	return sStore.MemSize()
}

func (fs *fieldStore) TimeRange(interval int64) (timeRange timeutil.TimeRange, ok bool) {
	for _, sStore := range fs.sStoreNodes {
		startSlot, endSlot, err := sStore.SlotRange()
		if err != nil {
			continue
		}
		ok = true
		startTime := sStore.GetFamilyTime() + int64(startSlot)*interval
		endTime := sStore.GetFamilyTime() + int64(endSlot)*interval
		if timeRange.Start == 0 || startTime < timeRange.Start {
			timeRange.Start = startTime
		}
		if timeRange.End == 0 || timeRange.End < endTime {
			timeRange.End = endTime
		}
	}
	return
}

func (fs *fieldStore) MemSize() int {
	size := emptyFieldStoreSize + 8*cap(fs.sStoreNodes)
	for _, sStore := range fs.sStoreNodes {
		size += sStore.MemSize()
	}
	return size
}
