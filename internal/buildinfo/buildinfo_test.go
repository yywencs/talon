package buildinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDevelopmentBuildInfoHasStableDefaults(t *testing.T) {
	assert.Equal(t, "talon-toolops-agent/v1", AgentVersion)
	assert.Equal(t, "unknown", Commit)
}
