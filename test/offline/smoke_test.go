package offline

import (
	"errors"
	"testing"

	"github.com/duongle-steven/homelab/test/internal/smoke"
	"github.com/stretchr/testify/assert"
)

func TestOptionalAppState(t *testing.T) {
	skip, failure := smoke.OptionalAppState("paperless", nil, false, nil)
	assert.True(t, skip)
	assert.Empty(t, failure)
	_, failure = smoke.OptionalAppState("paperless", []string{"opencloud", "paperless"}, false, nil)
	assert.Contains(t, failure, "was asked for")
	skip, failure = smoke.OptionalAppState("paperless", []string{"paperless"}, true, nil)
	assert.False(t, skip)
	assert.Empty(t, failure)
	_, failure = smoke.OptionalAppState("paperless", nil, false, errors.New("connection refused"))
	assert.Contains(t, failure, "cannot tell")
}
