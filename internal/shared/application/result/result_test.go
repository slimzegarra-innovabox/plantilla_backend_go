package result_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"myapp.v1.0.0/internal/shared/application/result"
)

var errSampleNotFound = result.NewFailure(result.NotFound, "SAMPLE_NOT_FOUND", "No existe.")

func TestOk(t *testing.T) {
	r := result.Ok(42, result.Info(result.Validation, "SAMPLE_INFO", "aviso"))

	assert.True(t, r.IsSuccess())
	assert.Equal(t, 42, r.Data())
	require.Len(t, r.Notifications(), 1)
	assert.Equal(t, result.SeverityInfo, r.Notifications()[0].Severity)
}

func TestOkWithoutNotificationsReturnsEmptySlice(t *testing.T) {
	r := result.Ok("x")

	assert.NotNil(t, r.Notifications())
	assert.Empty(t, r.Notifications())
}

func TestFail(t *testing.T) {
	r := result.Fail[int](
		result.Error(result.Validation, "A", "a").WithField("name"),
		result.Error(result.Validation, "B", "b"),
	)

	assert.False(t, r.IsSuccess())
	assert.Zero(t, r.Data())
	require.Len(t, r.Notifications(), 2)
	assert.Equal(t, "name", r.Notifications()[0].Field)
	assert.Equal(t, "B", r.Notifications()[1].Code)
}

func TestFromErrorConvertsWrappedFailure(t *testing.T) {
	err := fmt.Errorf("buscar sample: %w", errSampleNotFound)

	r, techErr := result.FromError[string](err)

	require.NoError(t, techErr)
	assert.False(t, r.IsSuccess())
	assert.Equal(t, "SAMPLE_NOT_FOUND", r.Notifications()[0].Code)
	assert.Equal(t, result.NotFound, r.Notifications()[0].Category)
	assert.True(t, errors.Is(err, errSampleNotFound))
}

func TestFromErrorPropagatesTechnicalError(t *testing.T) {
	boom := errors.New("connection refused")

	r, techErr := result.FromError[string](boom)

	assert.Same(t, boom, techErr)
	assert.False(t, r.IsSuccess())
	assert.Empty(t, r.Notifications())
}

func TestFailureErrorReturnsCode(t *testing.T) {
	assert.Equal(t, "SAMPLE_NOT_FOUND", errSampleNotFound.Error())
}
