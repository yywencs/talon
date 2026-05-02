package tool

import "testing"

func TestPathMapperMapsRootsBothWays(t *testing.T) {
	mapper := NewPathMapper("/Users/wen/project", "/workspace")

	runtimePath, ok := mapper.HostToRuntime("/Users/wen/project")
	if !ok {
		t.Fatal("expected host root to map into runtime root")
	}
	if runtimePath != "/workspace" {
		t.Fatalf("runtime path = %q, want %q", runtimePath, "/workspace")
	}

	hostPath, ok := mapper.RuntimeToHost("/workspace")
	if !ok {
		t.Fatal("expected runtime root to map into host root")
	}
	if hostPath != "/Users/wen/project" {
		t.Fatalf("host path = %q, want %q", hostPath, "/Users/wen/project")
	}
}

func TestPathMapperMapsSubPathsBothWays(t *testing.T) {
	mapper := NewPathMapper("/Users/wen/project", "/workspace")

	runtimePath, ok := mapper.HostToRuntime("/Users/wen/project/internal/tool")
	if !ok {
		t.Fatal("expected host subpath to map into runtime subpath")
	}
	if runtimePath != "/workspace/internal/tool" {
		t.Fatalf("runtime path = %q, want %q", runtimePath, "/workspace/internal/tool")
	}

	hostPath, ok := mapper.RuntimeToHost("/workspace/internal/tool")
	if !ok {
		t.Fatal("expected runtime subpath to map into host subpath")
	}
	if hostPath != "/Users/wen/project/internal/tool" {
		t.Fatalf("host path = %q, want %q", hostPath, "/Users/wen/project/internal/tool")
	}
}

func TestPathMapperRejectsPathsOutsideWorkspaceRoots(t *testing.T) {
	mapper := NewPathMapper("/Users/wen/project", "/workspace")

	if _, ok := mapper.HostToRuntime("/Users/wen/other/file.go"); ok {
		t.Fatal("expected host path outside workspace root to be rejected")
	}
	if _, ok := mapper.RuntimeToHost("/tmp/file.go"); ok {
		t.Fatal("expected runtime path outside workspace root to be rejected")
	}
	if mapper.IsHostPath("/Users/wen/other/file.go") {
		t.Fatal("expected IsHostPath to reject path outside host root")
	}
	if mapper.IsRuntimePath("/tmp/file.go") {
		t.Fatal("expected IsRuntimePath to reject path outside runtime root")
	}
}

func TestPathMapperDegeneratesToNoOpWhenRootsAreEqual(t *testing.T) {
	mapper := NewPathMapper("/Users/wen/project", "/Users/wen/project")

	hostPath, ok := mapper.RuntimeToHost("/Users/wen/project/internal/workspace")
	if !ok {
		t.Fatal("expected equal roots to keep runtime-to-host mapping available")
	}
	if hostPath != "/Users/wen/project/internal/workspace" {
		t.Fatalf("host path = %q, want %q", hostPath, "/Users/wen/project/internal/workspace")
	}

	runtimePath, ok := mapper.HostToRuntime("/Users/wen/project/internal/workspace")
	if !ok {
		t.Fatal("expected equal roots to keep host-to-runtime mapping available")
	}
	if runtimePath != "/Users/wen/project/internal/workspace" {
		t.Fatalf("runtime path = %q, want %q", runtimePath, "/Users/wen/project/internal/workspace")
	}
}
