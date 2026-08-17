package skill

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionProgressivelyLoadsAndUnloadsSkills(t *testing.T) {
	registry, err := LoadDirectory("../../skills")
	require.NoError(t, err)
	session, err := NewSession(registry, 2, func(refs []string) error {
		if refs[0] != "call-query-logs" {
			return fmt.Errorf("unknown evidence")
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, session.Active())

	loaded, err := session.Activate("mapping-diagnosis", "参数类型与近期变更相关", []string{"call-query-logs"})
	require.NoError(t, err)
	assert.Equal(t, "loaded", loaded.Action)
	assert.Equal(t, []string{"mapping-diagnosis"}, loaded.ActiveSkills)
	_, err = session.Activate("credential-diagnosis", "同时存在鉴权失败", []string{"call-query-logs"})
	require.NoError(t, err)
	_, err = session.Activate("connection-diagnosis", "还可能存在连接问题", []string{"call-query-logs"})
	require.ErrorContains(t, err, "limit 2 reached")

	unloaded, err := session.Deactivate("mapping-diagnosis", "新证据否定 mapping 假设", []string{"call-query-logs"})
	require.NoError(t, err)
	assert.Equal(t, "unloaded", unloaded.Action)
	assert.Equal(t, []string{"credential-diagnosis"}, unloaded.ActiveSkills)
	_, err = session.Activate("connection-diagnosis", "连接证据成立", []string{"call-query-logs"})
	require.NoError(t, err)
	assert.Equal(t, []string{"credential-diagnosis", "connection-diagnosis"}, activeNames(session.Active()))
}

func TestSessionRejectsInvalidSkillChanges(t *testing.T) {
	registry, err := LoadDirectory("../../skills")
	require.NoError(t, err)
	session, err := NewSession(registry, 2, nil)
	require.NoError(t, err)

	_, err = session.Activate("unknown-skill", "test", []string{"call-1"})
	require.ErrorContains(t, err, "is not installed")
	_, err = session.Activate("mapping-diagnosis", "", []string{"call-1"})
	require.ErrorContains(t, err, "reason is required")
	_, err = session.Activate("mapping-diagnosis", "test", nil)
	require.ErrorContains(t, err, "evidence reference is required")
}

func activeNames(values []Definition) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = values[index].Name
	}
	return result
}
