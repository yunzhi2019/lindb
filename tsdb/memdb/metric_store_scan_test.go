package memdb

import (
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	"github.com/lindb/lindb/pkg/timeutil"
	pb "github.com/lindb/lindb/rpc/proto/field"
	"github.com/lindb/lindb/series"
	"github.com/lindb/lindb/series/field"
	"github.com/lindb/lindb/tsdb/metadb"
)

func Test_MetricStore_scan(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	calc := timeutil.Interval(timeutil.OneDay).Calculator()

	now, _ := timeutil.ParseTimestamp("20190702 19:10:48", "20060102 15:04:05")
	familyTime, _ := timeutil.ParseTimestamp("20190702 19:00:00", "20060102 15:04:05")

	mStoreInterface := newMetricStore(100)
	mStore := mStoreInterface.(*metricStore)
	mStore.fieldsMetas.Store(field.Metas{
		{ID: 3, Type: field.SumField, Name: "sum3"},
		{ID: 4, Type: field.SumField, Name: "sum4"},
		{ID: 5, Type: field.SumField, Name: "sum5"},
		{ID: 6, Type: field.SumField, Name: "sum6"}})
	// v1:
	ti1 := newTagIndex().(*tagIndex)
	ti1.version = 1
	ti1.earliestTimeDelta.Store(100)
	ti1.latestTimeDelta.Store(200)
	// v2
	ti2 := newTagIndex().(*tagIndex)
	ti2.version = 2
	ti2.earliestTimeDelta.Store(200)
	ti2.latestTimeDelta.Store(300)
	ts5 := newTimeSeriesStore()
	ts6 := newTimeSeriesStore()
	ts7 := newTimeSeriesStore()
	ts8 := newTimeSeriesStore()
	ts5.(*timeSeriesStore).insertFStore(newFieldStore(1))
	ts5.(*timeSeriesStore).insertFStore(newFieldStore(2))
	ts5.(*timeSeriesStore).insertFStore(newFieldStore(3))
	ts5.(*timeSeriesStore).insertFStore(newFieldStore(4))

	ti2.seriesID2TStore = newMetricMap()
	ti2.seriesID2TStore.put(5, ts5)
	ti2.seriesID2TStore.put(6, ts6)
	ti2.seriesID2TStore.put(7, ts7)
	ti2.seriesID2TStore.put(8, ts8)
	// build id-set
	idset := series.NewMultiVerSeriesIDSet()
	idset.Add(0, roaring.New())
	bitmap := roaring.New()
	bitmap.AddMany([]uint32{1, 2, 3, 4, 5, 7})
	idset.Add(2, bitmap)

	// build mStore
	mStore.immutable.Store(ti1)
	mStore.mutable = ti2
	metric := &pb.Metric{
		Name:      "cpu",
		Timestamp: now,
		Fields: []*pb.Field{
			{Name: "sum3", Field: &pb.Field_Sum{Sum: &pb.Sum{
				Value: 1.0,
			}}},
			{Name: "sum4", Field: &pb.Field_Sum{Sum: &pb.Sum{
				Value: 1.0,
			}}},
		},
		Tags: map[string]string{"host": "1.1.1.1", "disk": "/tmp"},
	}

	generator := metadb.NewMockIDGenerator(ctrl)
	generator.EXPECT().GenTagKeyID(gomock.Any(), gomock.Any()).Return(uint32(1)).AnyTimes()

	idGet := NewMockmStoreFieldIDGetter(ctrl)
	idGet.EXPECT().GetFieldIDOrGenerate("sum3", gomock.Any(), gomock.Any()).Return(uint16(3), nil)
	idGet.EXPECT().GetFieldIDOrGenerate("sum4", gomock.Any(), gomock.Any()).Return(uint16(4), nil)
	bs := newBlockStore(10)
	writtenSize, err := mStore.Write(metric,
		writeContext{
			generator:           generator,
			blockStore:          bs,
			familyTime:          familyTime,
			slotIndex:           20,
			metricID:            uint32(10),
			mStoreFieldIDGetter: idGet,
		})
	assert.NotZero(t, writtenSize)
	if err != nil {
		t.Fatal(err)
	}

	worker := &mockScanWorker{}
	mStore.Scan(&series.ScanContext{
		SeriesIDSet:  idset,
		FieldIDs:     []uint16{3, 4, 5},
		IntervalCalc: calc,
		Worker:       worker,
	})
	assert.Equal(t, 1, len(worker.events))
	// field not found
	mStore.Scan(&series.ScanContext{
		SeriesIDSet: idset,
		FieldIDs:    []uint16{1, 2},
	})
	// field not match
	mStore.Scan(&series.ScanContext{
		SeriesIDSet: idset,
		FieldIDs:    []uint16{1, 2, 3, 4},
	})
}
