package forwardindex

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/lindb/lindb/kv"
	"github.com/lindb/lindb/pkg/timeutil"
	"github.com/lindb/lindb/series"

	"github.com/RoaringBitmap/roaring"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

func Test_NewFlusher(t *testing.T) {
	flusher := NewFlusher(nil)
	assert.NotNil(t, flusher)
}

func Test_Flush(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockKVFlusher := kv.NewMockFlusher(ctrl)
	mockKVFlusher.EXPECT().Commit().Return(nil).AnyTimes()
	gomock.InOrder(
		mockKVFlusher.EXPECT().Add(gomock.Any(), gomock.Any()).Return(nil).Times(1),
		mockKVFlusher.EXPECT().Add(gomock.Any(), gomock.Any()).Return(fmt.Errorf("error")).Times(1),
	)

	mockFlusher := NewFlusher(mockKVFlusher)
	for v := 0; v < 2; v++ {
		for x := byte(32); x < byte(35); x++ {
			for i := 0; i < 120000; i++ {
				bitmap := roaring.NewBitmap()
				bitmap.Add(uint32(i))
				mockFlusher.FlushTagValue(strconv.Itoa(i), bitmap)
			}
			mockFlusher.FlushTagKey(string(x))
		}
		mockFlusher.FlushVersion(series.Version(v), timeutil.TimeRange{Start: 0, End: 10})
	}
	assert.Nil(t, mockFlusher.FlushMetricID(1))

	assert.NotNil(t, mockFlusher.FlushMetricID(1))

	assert.Nil(t, mockFlusher.Commit())
}
