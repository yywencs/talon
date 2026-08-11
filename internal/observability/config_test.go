package observability

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv(envEnabled, "true")
	t.Setenv(envAPIBaseURL, "http://localhost:8888/")
	t.Setenv(envWorkspaceID, "workspace-1")
	t.Setenv(envAPIToken, "token-1")
	t.Setenv(envServiceName, " toolops-test ")
	t.Setenv(envDeploymentEnv, " test ")
	t.Setenv(envInputOutputMaxBytes, "8192")
	t.Setenv(envNormalMaxBytes, "1024")
	t.Setenv(envAggregateOutput, "false")

	cfg := LoadConfigFromEnv()

	assert.True(t, cfg.Enabled)
	assert.Equal(t, "http://localhost:8888", cfg.APIBaseURL)
	assert.Equal(t, "workspace-1", cfg.WorkspaceID)
	assert.Equal(t, "token-1", cfg.APIToken)
	assert.Equal(t, "toolops-test", cfg.ServiceName)
	assert.Equal(t, "test", cfg.DeploymentEnv)
	assert.Equal(t, 8192, cfg.InputOutputMaxBytes)
	assert.Equal(t, 1024, cfg.NormalMaxBytes)
	assert.False(t, cfg.AggregateOutput)
}

func TestInitValidatesEnabledConfig(t *testing.T) {
	require.NoError(t, Shutdown(context.Background()))
	t.Cleanup(func() { require.NoError(t, Shutdown(context.Background())) })

	cfg := DefaultConfig()
	cfg.Enabled = true
	err := Init(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), envWorkspaceID)

	cfg.WorkspaceID = "workspace-1"
	err = Init(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), envAPIToken)
}

func TestInitDisabledDoesNotCreateHandler(t *testing.T) {
	require.NoError(t, Shutdown(context.Background()))
	t.Cleanup(func() { require.NoError(t, Shutdown(context.Background())) })

	require.NoError(t, Init(context.Background(), DefaultConfig()))
	assert.Nil(t, EinoHandler())
}
