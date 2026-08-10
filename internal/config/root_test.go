package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindRootUsesEnvOverride(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "custom-config")
	t.Setenv(configDirEnv, configDir)

	got, err := FindRoot()
	if err != nil {
		t.Fatalf("FindRoot() error = %v", err)
	}
	if got != configDir {
		t.Fatalf("FindRoot() = %q, want %q", got, configDir)
	}
}

func TestFindRootFallsBackToHome(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore working directory failed: %v", chdirErr)
		}
	}()

	workingDir := t.TempDir()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	if writeErr := os.WriteFile(filepath.Join(workingDir, "go.mod"), []byte("module example.com/workspace\n"), 0644); writeErr != nil {
		t.Fatalf("write go.mod failed: %v", writeErr)
	}

	if chdirErr := os.Chdir(workingDir); chdirErr != nil {
		t.Fatalf("chdir failed: %v", chdirErr)
	}

	got, err := FindRoot()
	if err != nil {
		t.Fatalf("FindRoot() error = %v", err)
	}

	want := filepath.Join(homeDir, ".opentalon")
	if got != want {
		t.Fatalf("FindRoot() = %q, want %q", got, want)
	}
}
