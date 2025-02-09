package query

import (
	"context"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	"github.com/lindb/lindb/parallel"
	"github.com/lindb/lindb/tsdb"
)

func TestExecutorFactory_NewExecutor(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := NewExecutorFactory()
	mockDatabase := tsdb.NewMockDatabase(ctrl)
	mockDatabase.EXPECT().ExecutorPool().Return(nil)
	assert.NotNil(t, factory.NewStorageExecutor(
		parallel.NewMockExecuteContext(ctrl), mockDatabase, nil, nil))
	assert.NotNil(t, factory.NewBrokerExecutor(
		context.TODO(), "db", "sql", nil, nil, nil))
}
