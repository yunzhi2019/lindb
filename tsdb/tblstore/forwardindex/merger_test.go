package forwardindex

import (
	"fmt"
	"testing"
	"time"

	"github.com/lindb/lindb/kv"
	"github.com/lindb/lindb/pkg/timeutil"
	"github.com/lindb/lindb/series"
	"github.com/lindb/lindb/tsdb/tblstore"

	"github.com/RoaringBitmap/roaring"
	"github.com/stretchr/testify/assert"
)

func buildBlockToCompact() (data [][]byte) {
	nopKVFlusher := kv.NewNopFlusher()
	flusher := NewFlusher(nopKVFlusher)
	now := timeutil.Now()

	flushVersion := func(count int) {
		for i := 0; i < count; i++ {
			flusher.FlushTagValue(
				fmt.Sprintf("192.168.1.%d", i), roaring.BitmapOf(uint32(i)))
		}
		flusher.FlushTagKey("ip")
	}
	flushVersion(10)
	flusher.FlushVersion(series.Version(now-3600*1000*24*60), timeutil.TimeRange{Start: 1, End: 2})
	flushVersion(10)
	flusher.FlushVersion(series.Version(now-3600*1000*24*20), timeutil.TimeRange{Start: 1, End: 2})
	_ = flusher.FlushMetricID(1)
	data = append(data, append([]byte{}, nopKVFlusher.Bytes()...))

	flushVersion(12)
	flusher.FlushVersion(series.Version(now-3600*1000*24*35), timeutil.TimeRange{Start: 1, End: 2})
	flushVersion(12)
	flusher.FlushVersion(series.Version(now-3600*1000*24*20), timeutil.TimeRange{Start: 1, End: 2})
	_ = flusher.FlushMetricID(1)
	data = append(data, append([]byte{}, nopKVFlusher.Bytes()...))

	return data
}

func Test_Merger(t *testing.T) {
	m := NewMerger(time.Hour * 24 * 30).(*merger)
	assert.NotNil(t, m)

	// merge invalid data
	data, err := m.Merge(0, [][]byte{{1, 2}})
	assert.Nil(t, data)
	assert.NotNil(t, err)
	// merge nil
	data, err = m.Merge(0, nil)
	assert.Nil(t, data)
	assert.NotNil(t, err)
	// merge normal
	block := buildBlockToCompact()
	data, err = m.Merge(1, block)
	assert.Nil(t, err)
	assert.NotNil(t, data)

	itr, err := tblstore.NewVersionBlockIterator(data)
	assert.Nil(t, err)
	assert.True(t, itr.HasNext())
	_, versionBlock := itr.Next()
	assert.NotNil(t, versionBlock)
	assert.False(t, itr.HasNext())

	// keep the last one ttl all
	m.ttl = time.Hour
	data, err = m.Merge(1, block)
	assert.NotNil(t, data)
	assert.Nil(t, err)
}
