package replication

import (
	"errors"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	"github.com/lindb/lindb/config"
	"github.com/lindb/lindb/pkg/ltoml"
	"github.com/lindb/lindb/rpc"
	"github.com/lindb/lindb/rpc/proto/field"
	"github.com/lindb/lindb/rpc/proto/storage"
	"github.com/lindb/lindb/service"
)

var replicationConfig = config.ReplicationChannel{
	Dir:                "/tmp/broker/replication",
	SegmentFileSize:    uint16(128),
	RemoveTaskInterval: ltoml.Duration(time.Minute),
	ReportInterval:     ltoml.Duration(time.Second),
	FlushInterval:      ltoml.Duration(0),
	CheckFlushInterval: ltoml.Duration(time.Second),
	BufferSize:         uint16(0),
}

func TestChannelManager_GetChannel(t *testing.T) {
	ctrl := gomock.NewController(t)
	dirPath := path.Join(os.TempDir(), "test_channel_manager")
	defer func() {
		if err := os.RemoveAll(dirPath); err != nil {
			t.Error(err)
		}
		ctrl.Finish()
	}()

	replicatorService := service.NewMockReplicatorService(ctrl)
	replicatorService.EXPECT().Report(gomock.Any()).Return(fmt.Errorf("err")).AnyTimes()

	replicationConfig.Dir = dirPath
	cm := NewChannelManager(replicationConfig, nil, replicatorService)

	_, err := cm.CreateChannel("database", 2, 2)
	if err == nil {
		t.Fatal("should be error")
	}

	ch1, err := cm.CreateChannel("database", 3, 0)
	if err != nil {
		t.Fatal(err)
	}

	_, err = cm.CreateChannel("database", 2, 1)
	if err == nil {
		t.Fatal(" should be error")
	}

	ch111, err := cm.CreateChannel("database", 3, 0)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, ch111, ch1)

	cm.Close()
}

func TestChannelManager_Write(t *testing.T) {
	ctrl := gomock.NewController(t)
	dirPath := path.Join(os.TempDir(), "test_channel_manager")
	defer func() {
		if err := os.RemoveAll(dirPath); err != nil {
			t.Error(err)
		}
		ctrl.Finish()
	}()

	replicatorService := service.NewMockReplicatorService(ctrl)
	replicatorService.EXPECT().Report(gomock.Any()).Return(fmt.Errorf("err")).AnyTimes()

	replicationConfig.Dir = dirPath
	cm := NewChannelManager(replicationConfig, nil, replicatorService)

	_, err := cm.CreateChannel("database", 1, 0)
	if err != nil {
		t.Fatal(err)
	}

	metricList := &field.MetricList{
		Database: "database",
		Metrics: []*field.Metric{
			{
				Name:      "name",
				Timestamp: time.Now().Unix() * 1000,
				Tags:      map[string]string{"tagKey": "tagVal"},
				Fields: []*field.Field{
					{
						Name: "sum",
						Field: &field.Field_Sum{
							Sum: &field.Sum{
								Value: 1.0,
							}},
					},
				},
			},
		},
	}

	err = cm.Write(metricList)
	if err != nil {
		t.Fatal(err)
	}
}

func TestChannel_GetOrCreateReplicator(t *testing.T) {
	dirPath := path.Join(os.TempDir(), "test_channel_manager")
	defer func() {
		if err := os.RemoveAll(dirPath); err != nil {
			t.Error(err)
		}
	}()

	ctl := gomock.NewController(t)
	defer ctl.Finish()

	replicatorService := service.NewMockReplicatorService(ctl)
	replicatorService.EXPECT().Report(gomock.Any()).Return(fmt.Errorf("err")).AnyTimes()

	mockFct := rpc.NewMockClientStreamFactory(ctl)
	mockFct.EXPECT().CreateWriteServiceClient(node).Return(nil, errors.New("get service client error any")).AnyTimes()

	replicationConfig.Dir = dirPath
	cm := NewChannelManager(replicationConfig, mockFct, replicatorService)

	ch, err := cm.CreateChannel("database", 2, 0)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, len(ch.Targets()), 0)

	assert.Equal(t, ch.Database(), "database")
	assert.Equal(t, ch.ShardID(), int32(0))

	rep1, err := ch.GetOrCreateReplicator(node)
	if err != nil {
		t.Fatal(err)
	}

	rep11, err := ch.GetOrCreateReplicator(node)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, rep1, rep11)
	assert.Equal(t, len(ch.Targets()), 1)

	cm.Close()
}

func TestChannel_WriteFail(t *testing.T) {
	dirPath := path.Join(os.TempDir(), "test_channel_manager")
	if err := os.RemoveAll(dirPath); err != nil {
		t.Fatal(err)
	}

	ctl := gomock.NewController(t)
	defer func() {
		if err := os.RemoveAll(dirPath); err != nil {
			t.Error(err)
		}
		ctl.Finish()
	}()

	replicationConfig.Dir = dirPath

	replicatorService := service.NewMockReplicatorService(ctl)
	replicatorService.EXPECT().Report(gomock.Any()).Return(fmt.Errorf("err")).AnyTimes()

	mockFct := rpc.NewMockClientStreamFactory(ctl)
	mockFct.EXPECT().CreateWriteServiceClient(node).Return(nil, errors.New("get service client error any")).AnyTimes()

	cm := NewChannelManager(replicationConfig, mockFct, replicatorService)

	ch, err := cm.CreateChannel("database", 2, 0)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, len(ch.Targets()), 0)

	rep1, err := ch.GetOrCreateReplicator(node)
	if err != nil {
		t.Fatal(err)
	}

	if err := ch.Write([]byte("123")); err != nil {
		t.Fatal(err)
	}

	// wait for replication
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, rep1.Pending(), int64(1))

	cm.Close()

}

func TestChannel_WriteSuccess(t *testing.T) {
	dirPath := path.Join(os.TempDir(), "test_channel_manager")
	if err := os.RemoveAll(dirPath); err != nil {
		t.Fatal(err)
	}

	ctl := gomock.NewController(t)
	defer func() {
		if err := os.RemoveAll(dirPath); err != nil {
			t.Error(err)
		}
		ctl.Finish()
	}()

	replicationConfig.Dir = dirPath

	replicatorService := service.NewMockReplicatorService(ctl)
	replicatorService.EXPECT().Report(gomock.Any()).Return(fmt.Errorf("err")).AnyTimes()

	mockServiceClient := storage.NewMockWriteServiceClient(ctl)
	mockServiceClient.EXPECT().Next(gomock.Any(), gomock.Any()).Return(&storage.NextSeqResponse{
		Seq: 0,
	}, nil)

	done := make(chan struct{})
	mockClientStream := storage.NewMockWriteService_WriteClient(ctl)
	mockClientStream.EXPECT().Recv().DoAndReturn(func() (*storage.WriteResponse, error) {
		<-done
		return nil, errors.New("recv errors")
	})

	wr, _ := buildWriteRequest(0, 1)
	mockClientStream.EXPECT().Send(wr).Return(nil)

	mockFct := rpc.NewMockClientStreamFactory(ctl)
	mockFct.EXPECT().CreateWriteServiceClient(node).Return(mockServiceClient, nil)
	mockFct.EXPECT().LogicNode().Return(node)
	mockFct.EXPECT().CreateWriteClient(gomock.Any(), gomock.Any(), gomock.Any()).Return(mockClientStream, nil)

	cm := NewChannelManager(replicationConfig, mockFct, replicatorService)

	ch, err := cm.CreateChannel(database, 2, 0)
	if err != nil {
		t.Fatal(err)
	}

	assert.Equal(t, len(ch.Targets()), 0)

	rep1, err := ch.GetOrCreateReplicator(node)
	if err != nil {
		t.Fatal(err)
	}

	if err := ch.Write([]byte("0")); err != nil {
		t.Fatal(err)
	}

	// wait for replication
	time.Sleep(2 * time.Second)
	assert.Equal(t, rep1.Pending(), int64(0))

	cm.Close()
	// cm close pass to replicator is async, wait
	time.Sleep(100 * time.Millisecond)
	close(done)
}
