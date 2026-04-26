package terminal

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/wen/opentalon/internal/types"
)

func testFloatPtr(v float64) *float64 {
	return &v
}

func TestValidateAction(t *testing.T) {
	tests := []struct {
		name    string
		action  BashTool
		wantErr string
	}{
		{
			name: "empty command",
			action: BashTool{
				Command: "",
			},
			wantErr: "command is empty",
		},
		{
			name: "invalid timeout",
			action: BashTool{
				Command: "echo hi",
				Timeout: testFloatPtr(301),
			},
			wantErr: "timeout out of range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAction(&tt.action)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestValidateAction_InputModeAllowsEmptyCommand(t *testing.T) {
	err := validateAction(&BashTool{IsInput: true})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestValidateWorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := tmpDir + "/file.txt"
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	tests := []struct {
		name       string
		workingDir string
		wantErr    string
	}{
		{
			name:       "missing working dir",
			workingDir: tmpDir + "/missing",
			wantErr:    "working_dir does not exist",
		},
		{
			name:       "working dir is file",
			workingDir: tmpFile,
			wantErr:    "working_dir is not a directory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateWorkingDir(tt.workingDir)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestBashToolActionFields(t *testing.T) {
	typ := reflect.TypeOf(BashTool{})
	fields := make(map[string]struct{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == ",inline" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		fields[name] = struct{}{}
	}

	for _, name := range []string{"command", "is_input", "timeout", "reset"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("expected field %q to exist", name)
		}
	}
	if _, ok := fields["working_dir"]; ok {
		t.Fatal("working_dir should not appear in BashTool action fields")
	}
	if _, ok := fields["timeout_secs"]; ok {
		t.Fatal("timeout_secs should not appear in BashTool action fields")
	}
}

func TestNewTerminalObservation(t *testing.T) {
	pid := 123
	obs := NewTerminalObservation("echo hi", "/tmp", &pid, true, -1, "command timed out")

	if !obs.ErrorStatus {
		t.Fatal("expected ErrorStatus=true")
	}
	if !obs.Timeout {
		t.Fatal("expected Timeout=true")
	}
	if obs.ExitCode == nil || *obs.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %#v", obs.ExitCode)
	}
	if obs.Metadata.PID == nil || *obs.Metadata.PID != pid {
		t.Fatalf("expected pid %d, got %#v", pid, obs.Metadata.PID)
	}
	if obs.Metadata.WorkingDir != "/tmp" {
		t.Fatalf("expected working dir /tmp, got %q", obs.Metadata.WorkingDir)
	}
	if obs.OutputText() != "command timed out" {
		t.Fatalf("unexpected output text: %q", obs.OutputText())
	}
}

func TestAuditHelpers(t *testing.T) {
	if got := auditCommandName("  echo hello  "); got != "echo" {
		t.Fatalf("expected command name echo, got %q", got)
	}

	hash1 := auditCommandHash("echo hello")
	hash2 := auditCommandHash("echo hello")
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}
	if hash1 != hash2 {
		t.Fatalf("expected stable hash, got %q and %q", hash1, hash2)
	}
}

func TestTruncateIfNeeded(t *testing.T) {
	output := strings.Repeat("a", maxOutputSize+10)
	got, truncated := truncateIfNeeded(output)
	if !truncated {
		t.Fatal("expected output to be truncated")
	}
	if !strings.HasSuffix(got, "[output truncated]") {
		t.Fatalf("expected truncation suffix, got %q", got[len(got)-20:])
	}
}

func TestBashExecutorSimpleEcho(t *testing.T) {
	obs := BashExecutor(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hello",
	})

	if obs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", obs.ExitCodeValue())
	}
	if obs.OutputText() != "hello\n" {
		t.Fatalf("expected output hello\\n, got %q", obs.OutputText())
	}
}

func TestExecutorUsesWorkingDir(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		WorkingDir: "/tmp",
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "pwd",
	})

	if obs.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", obs.ExitCodeValue())
	}
	if obs.Metadata.WorkingDir != "/tmp" {
		t.Fatalf("expected working dir /tmp, got %q", obs.Metadata.WorkingDir)
	}
	if !strings.Contains(obs.OutputText(), "/tmp") {
		t.Fatalf("expected output to contain /tmp, got %q", obs.OutputText())
	}
}

func TestExecutorRejectsInvalidWorkingDir(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{
		WorkingDir: "/nonexistent/path/that/does/not/exist",
	})

	obs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hello",
	})

	if obs.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", obs.ExitCodeValue())
	}
	if !strings.Contains(obs.OutputText(), "working_dir does not exist") {
		t.Fatalf("expected invalid working_dir error, got %q", obs.OutputText())
	}
}

func TestExecutorRejectsUnsupportedModes(t *testing.T) {
	executor := NewExecutor(ExecutorConfig{})

	inputObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		IsInput: true,
	})
	if !strings.Contains(inputObs.OutputText(), "is_input is not implemented yet") {
		t.Fatalf("expected is_input unsupported message, got %q", inputObs.OutputText())
	}

	resetObs := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Reset: true,
	})
	if !strings.Contains(resetObs.OutputText(), "reset is not implemented yet") {
		t.Fatalf("expected reset unsupported message, got %q", resetObs.OutputText())
	}
}
