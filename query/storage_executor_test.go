package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	"github.com/lindb/lindb/parallel"
	"github.com/lindb/lindb/pkg/timeutil"
	"github.com/lindb/lindb/series"
	"github.com/lindb/lindb/series/field"
	"github.com/lindb/lindb/sql"
	"github.com/lindb/lindb/sql/stmt"
	"github.com/lindb/lindb/tsdb"
	"github.com/lindb/lindb/tsdb/memdb"
	"github.com/lindb/lindb/tsdb/metadb"
)

func TestStorageExecute_validation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exeCtx := parallel.NewMockExecuteContext(ctrl)
	exeCtx.EXPECT().Complete(gomock.Any()).AnyTimes()
	exeCtx.EXPECT().RetainTask(gomock.Any()).AnyTimes()

	mockDatabase := tsdb.NewMockDatabase(ctrl)
	mockDatabase.EXPECT().ExecutorPool().Return(execPool).AnyTimes()
	mockDatabase.EXPECT().Name().Return("mock_tsdb").AnyTimes()
	query := &stmt.Query{Interval: timeutil.OneSecond}

	// query shards is empty
	exec := newStorageExecutor(exeCtx, mockDatabase, nil, query)
	exec.Execute()

	// shards of engine is empty
	mockDatabase.EXPECT().NumOfShards().Return(0)
	exec = newStorageExecutor(exeCtx, mockDatabase, []int32{1, 2, 3}, query)
	exec.Execute()

	// num. of shard not match
	mockDatabase.EXPECT().NumOfShards().Return(2)
	exec = newStorageExecutor(exeCtx, mockDatabase, []int32{1, 2, 3}, query)
	exec.Execute()

	mockDatabase.EXPECT().NumOfShards().Return(3).AnyTimes()
	mockDatabase.EXPECT().GetShard(gomock.Any()).Return(nil, false).MaxTimes(3)
	exec = newStorageExecutor(exeCtx, mockDatabase, []int32{1, 2, 3}, query)
	exec.Execute()

	// normal case
	query, _ = sql.Parse("select f from cpu")
	mockDB1 := newMockDatabase(ctrl)
	mockDB1.EXPECT().ExecutorPool().Return(execPool)

	exec = newStorageExecutor(exeCtx, mockDB1, []int32{1, 2, 3}, query)
	exec.Execute()
}

func TestStorageExecute_Plan_Fail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exeCtx := parallel.NewMockExecuteContext(ctrl)
	exeCtx.EXPECT().Complete(gomock.Any()).AnyTimes()

	mockDatabase := tsdb.NewMockDatabase(ctrl)
	mockDatabase.EXPECT().ExecutorPool().Return(execPool).AnyTimes()
	shard := tsdb.NewMockShard(ctrl)
	mockDatabase.EXPECT().GetShard(gomock.Any()).Return(shard, true).MaxTimes(3)
	mockDatabase.EXPECT().NumOfShards().Return(3)
	idGetter := metadb.NewMockIDGetter(ctrl)
	idGetter.EXPECT().GetMetricID("cpu").Return(uint32(10), fmt.Errorf("err"))
	mockDatabase.EXPECT().IDGetter().Return(idGetter).AnyTimes()

	// find metric name err
	query, _ := sql.Parse("select f from cpu where time>'20190729 11:00:00' and time<'20190729 12:00:00'")
	exec := newStorageExecutor(exeCtx, mockDatabase, []int32{1, 2, 3}, query)
	exec.Execute()
}

func TestStorageExecute_Execute(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exeCtx := parallel.NewMockExecuteContext(ctrl)
	exeCtx.EXPECT().Complete(gomock.Any()).AnyTimes()
	exeCtx.EXPECT().RetainTask(gomock.Any()).AnyTimes()

	mockDatabase := tsdb.NewMockDatabase(ctrl)
	mockDatabase.EXPECT().ExecutorPool().Return(execPool).AnyTimes()
	shard := tsdb.NewMockShard(ctrl)
	idGetter := metadb.NewMockIDGetter(ctrl)
	family := tsdb.NewMockDataFamily(ctrl)
	filter := series.NewMockFilter(ctrl)
	memDB := memdb.NewMockMemoryDatabase(ctrl)
	memDB.EXPECT().Interval().Return(int64(10)).AnyTimes()

	// mock data
	mockDatabase.EXPECT().NumOfShards().Return(3)
	mockDatabase.EXPECT().GetShard(int32(1)).Return(shard, true)
	mockDatabase.EXPECT().GetShard(int32(2)).Return(shard, true)
	mockDatabase.EXPECT().GetShard(int32(3)).Return(shard, true)
	mockDatabase.EXPECT().IDGetter().Return(idGetter)
	idGetter.EXPECT().GetMetricID("cpu").Return(uint32(10), nil)
	idGetter.EXPECT().GetFieldID(uint32(10), "f").Return(uint16(10), field.SumField, nil)
	shard.EXPECT().GetDataFamilies(gomock.Any(), gomock.Any()).Return([]tsdb.DataFamily{family, family}).MaxTimes(3)
	shard.EXPECT().MemoryDatabase().Return(memDB).MaxTimes(3)
	shard.EXPECT().IndexFilter().Return(filter).MaxTimes(3)
	shard.EXPECT().IndexMetaGetter().Return(nil).MaxTimes(3)
	filter.EXPECT().FindSeriesIDsByExpr(uint32(10), gomock.Any(), gomock.Any()).
		Return(mockSeriesIDSet(series.Version(11), roaring.BitmapOf(1, 2, 4)), nil)
	filter.EXPECT().FindSeriesIDsByExpr(uint32(10), gomock.Any(), gomock.Any()).
		Return(mockSeriesIDSet(series.Version(11), roaring.BitmapOf()), nil)
	filter.EXPECT().FindSeriesIDsByExpr(uint32(10), gomock.Any(), gomock.Any()).Return(nil, nil)
	memDB.EXPECT().FindSeriesIDsByExpr(uint32(10), gomock.Any(), gomock.Any()).
		Return(mockSeriesIDSet(series.Version(11), roaring.BitmapOf(1, 2, 4)), nil).MaxTimes(3)
	memDB.EXPECT().Scan(gomock.Any()).MaxTimes(3)
	family.EXPECT().Scan(gomock.Any()).MaxTimes(2 * 3)

	// normal case
	query, _ := sql.Parse("select f from cpu where host='1.1.1.1' and time>'20190729 11:00:00' and time<'20190729 12:00:00'")
	exec := newStorageExecutor(exeCtx, mockDatabase, []int32{1, 2, 3}, query)
	exec.Execute()
	time.Sleep(100 * time.Millisecond)
	e := exec.(*storageExecutor)
	pool := e.getAggregatorPool(10, 1, query.TimeRange)
	assert.NotNil(t, pool.Get())

	// find series err
	// mock data
	mockDatabase.EXPECT().NumOfShards().Return(1)
	mockDatabase.EXPECT().GetShard(int32(1)).Return(shard, true)
	mockDatabase.EXPECT().IDGetter().Return(idGetter)
	idGetter.EXPECT().GetMetricID("cpu").Return(uint32(10), nil)
	idGetter.EXPECT().GetFieldID(uint32(10), "f").Return(uint16(10), field.SumField, nil)
	shard.EXPECT().GetDataFamilies(gomock.Any(), gomock.Any()).Return([]tsdb.DataFamily{family, family})
	shard.EXPECT().MemoryDatabase().Return(memDB)
	shard.EXPECT().IndexFilter().Return(filter)
	filter.EXPECT().FindSeriesIDsByExpr(uint32(10), gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("err"))
	memDB.EXPECT().FindSeriesIDsByExpr(uint32(10), gomock.Any(), gomock.Any()).
		Return(nil, series.ErrNotFound)
	exec = newStorageExecutor(exeCtx, mockDatabase, []int32{1}, query)
	exec.Execute()
	time.Sleep(100 * time.Millisecond)
}

func TestStorageExecutor_checkShards(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	exeCtx := parallel.NewMockExecuteContext(ctrl)
	exeCtx.EXPECT().Complete(gomock.Any()).AnyTimes()
	exeCtx.EXPECT().RetainTask(gomock.Any()).AnyTimes()

	mockDatabase := newMockDatabase(ctrl)
	mockDatabase.EXPECT().ExecutorPool().Return(execPool).AnyTimes()
	query, _ := sql.Parse("select f from cpu where time>'20190729 11:00:00' and time<'20190729 12:00:00'")
	exec := newStorageExecutor(exeCtx, mockDatabase, []int32{1, 2, 3}, query)
	exec.Execute()

	execImpl := exec.(*storageExecutor)
	// check shards error
	execImpl.shardIDs = nil
	assert.NotNil(t, execImpl.checkShards())
}
