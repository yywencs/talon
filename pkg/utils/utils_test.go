package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindWorkspaceRoot(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	defer func() {
		if chdirErr := os.Chdir(originalWD); chdirErr != nil {
			t.Fatalf("restore working directory failed: %v", chdirErr)
		}
	}()

	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")
	if mkdirErr := os.MkdirAll(nested, 0755); mkdirErr != nil {
		t.Fatalf("mkdir nested failed: %v", mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0644); writeErr != nil {
		t.Fatalf("write go.mod failed: %v", writeErr)
	}
	if chdirErr := os.Chdir(nested); chdirErr != nil {
		t.Fatalf("chdir failed: %v", chdirErr)
	}

	got, err := FindWorkspaceRoot()
	if err != nil {
		t.Fatalf("FindWorkspaceRoot() error = %v", err)
	}
	gotRealPath, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(got) error = %v", err)
	}
	rootRealPath, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root) error = %v", err)
	}
	if gotRealPath != rootRealPath {
		t.Fatalf("FindWorkspaceRoot() = %q, want %q", gotRealPath, rootRealPath)
	}
}

func TestFindConfigRootUsesEnvOverride(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "custom-config")
	t.Setenv(OpentalonConfigDirEnv, configDir)

	got, err := FindConfigRoot()
	if err != nil {
		t.Fatalf("FindConfigRoot() error = %v", err)
	}
	if got != configDir {
		t.Fatalf("FindConfigRoot() = %q, want %q", got, configDir)
	}
}

func TestFindConfigRootFallsBackToHomeEvenWhenWorkspaceHasGoMod(t *testing.T) {
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

	got, err := FindConfigRoot()
	if err != nil {
		t.Fatalf("FindConfigRoot() error = %v", err)
	}

	want := filepath.Join(homeDir, ".opentalon")
	if got != want {
		t.Fatalf("FindConfigRoot() = %q, want %q", got, want)
	}
}
