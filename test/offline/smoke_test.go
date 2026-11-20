package offline

import (
	"errors"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/smoke"
	"github.com/stretchr/testify/assert"
)

func TestGroupState(t *testing.T) {
	skip, failure := smoke.GroupState("paperless", nil, false, nil)
	assert.True(t, skip)
	assert.Empty(t, failure)
	_, failure = smoke.GroupState("paperless", []string{"opencloud", "paperless"}, false, nil)
	assert.Contains(t, failure, "was asked for")
	skip, failure = smoke.GroupState("paperless", []string{"paperless"}, true, nil)
	assert.False(t, skip)
	assert.Empty(t, failure)
	_, failure = smoke.GroupState("paperless", nil, false, errors.New("connection refused"))
	assert.Contains(t, failure, "cannot tell")
}
