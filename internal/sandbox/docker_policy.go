package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const outputTruncationMarker = "\n[sandbox output truncated]\n"

func truncateOutput(output string, limit int) (string, bool) {
	if limit <= 0 {
		return output, false
	}
	if len(output) <= limit {
		return output, false
	}
	marker := outputTruncationMarker
	if limit <= len(marker) {
		return marker, true
	}
	return output[:limit-len(marker)] + marker, true
}

func classifyExecError(err error) string {
	switch {
	case err == nil:
		return ""
	case err == ErrSandboxTimedOut:
		return "timeout"
	case err == ErrSandboxCancelled:
		return "cancelled"
	case err == ErrSandboxResourceLimitExceeded:
		return "resource_limit"
	case err == ErrSandboxNotRunning:
		return "sandbox_unavailable"
	default:
		return fmt.Sprintf("%T", err)
	}
}

func validateDockerConfig(config Config) error {
	if err := ensureDir(config.WorkingDir); err != nil {
		return fmt.Errorf("ensure workspace root: %w", err)
	}
	for _, mount := range config.ReadOnlyMounts {
		if mount.HostPath == "" || mount.ContainerPath == "" {
			return fmt.Errorf("readonly mount is incomplete")
		}
		if err := validateReadOnlyMount(mount.HostPath); err != nil {
			return err
		}
	}
	return nil
}

func validateReadOnlyMount(hostPath string) error {
	cleanPath := filepath.Clean(hostPath)
	if cleanPath == "/var/run/docker.sock" {
		return fmt.Errorf("readonly mount %q is not allowed", cleanPath)
	}
	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		cleanHome := filepath.Clean(homeDir)
		if cleanPath == cleanHome || strings.HasPrefix(cleanPath, cleanHome+string(os.PathSeparator)) {
			return fmt.Errorf("readonly mount %q is not allowed", cleanPath)
		}
	}
	return nil
}
