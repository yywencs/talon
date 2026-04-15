package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wen/opentalon/internal/types"
)

func iptr(i int) *int {
	return &i
}

func TestBash_SimpleEcho(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command: "echo hello",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCode)
	}
	if output.Content != "hello\n" {
		t.Fatalf("expected content 'hello\\n', got %q", output.Content)
	}
}

func TestBash_WithTimeout(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test with timeout",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "sleep 1",
		TimeoutSecs: iptr(5),
	})

	start := time.Now()
	obs := tool.Execute(context.Background(), rawArgs)
	elapsed := time.Since(start)

	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCode)
	}
	if elapsed < time.Second {
		t.Fatalf("expected at least 1s elapsed, got %v", elapsed)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test non-zero exit",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command: "exit 1",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", output.ExitCode)
	}
}

func TestBash_EmptyCommand(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test empty command",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command: "",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "command is empty") {
		t.Fatalf("expected error message to contain 'command is empty', got %q", output.Content)
	}
}

func TestBash_InvalidTimeout_Zero(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test zero timeout",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "echo hi",
		TimeoutSecs: iptr(0),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "timeout_secs out of range") {
		t.Fatalf("expected error message to contain 'timeout_secs out of range', got %q", output.Content)
	}
}

func TestBash_InvalidTimeout_Negative(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test negative timeout",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "echo hi",
		TimeoutSecs: iptr(-1),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "timeout_secs out of range") {
		t.Fatalf("expected error message to contain 'timeout_secs out of range', got %q", output.Content)
	}
}

func TestBash_InvalidTimeout_TooLarge(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test too large timeout",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "echo hi",
		TimeoutSecs: iptr(301),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "timeout_secs out of range") {
		t.Fatalf("expected error message to contain 'timeout_secs out of range', got %q", output.Content)
	}
}

func TestBash_TimeoutExceeded(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test timeout exceeded",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "sleep 10",
		TimeoutSecs: iptr(1),
	})

	start := time.Now()
	obs := tool.Execute(context.Background(), rawArgs)
	elapsed := time.Since(start)

	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "timed out") {
		t.Fatalf("expected error message to contain 'timed out', got %q", output.Content)
	}
	if elapsed < time.Second && elapsed > 2*time.Second {
		t.Fatalf("expected elapsed time around 1s, got %v", elapsed)
	}
}

func TestBash_NonexistentWorkingDir(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test nonexistent working dir",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "echo hi",
		WorkingDir:  "/nonexistent/path/that/does/not/exist",
		TimeoutSecs: iptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "working_dir does not exist") {
		t.Fatalf("expected error message to contain 'working_dir does not exist', got %q", output.Content)
	}
}

func TestBash_CtxCancelled(t *testing.T) {
	tool := NewBashTool()
	ctx, cancel := context.WithCancel(context.Background())

	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test context cancelled",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "sleep 10",
		TimeoutSecs: iptr(30),
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	obs := tool.Execute(ctx, rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCode)
	}
	if !strings.Contains(output.Content, "context cancelled") {
		t.Fatalf("expected error message to contain 'context cancelled', got %q", output.Content)
	}
}

func TestBash_OutputTruncation(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test output truncation",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "yes | head -c 2000000",
		TimeoutSecs: iptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCode)
	}
	maxSize := 1024 * 1024
	if len(output.Content) > maxSize+len("[output truncated]") {
		t.Fatalf("expected content length <= %d, got %d", maxSize+len("[output truncated]"), len(output.Content))
	}
	if !strings.HasSuffix(output.Content, "[output truncated]") {
		t.Fatalf("expected content to end with '[output truncated]', got %q", output.Content)
	}
}

func TestBash_ConcurrentExec(t *testing.T) {
	tool := NewBashTool()
	commands := []string{
		"echo 1",
		"echo 2",
		"echo 3",
		"echo 4",
		"echo 5",
		"echo 6",
		"echo 7",
		"echo 8",
		"echo 9",
		"echo 10",
	}

	var wg sync.WaitGroup
	results := make([]*types.CmdOutputObservation, len(commands))
	var mu sync.Mutex

	for i, cmd := range commands {
		wg.Add(1)
		go func(idx int, command string) {
			defer wg.Done()
			rawArgs, _ := json.Marshal(BashAction{
				ActionMetadata: ActionMetadata{
					Summary:      "concurrent test",
					SecurityRisk: SecurityRisk_HIGH,
				},
				Command: command,
			})
			obs := tool.Execute(context.Background(), rawArgs)
			output, ok := obs.(*types.CmdOutputObservation)
			if !ok {
				t.Errorf("expected *CmdOutputObservation, got %T", obs)
				return
			}
			mu.Lock()
			results[idx] = output
			mu.Unlock()
		}(i, cmd)
	}

	wg.Wait()

	for i, output := range results {
		if output == nil {
			t.Errorf("result %d is nil", i)
			continue
		}
		if output.ExitCode != 0 {
			t.Errorf("result %d: expected exit code 0, got %d", i, output.ExitCode)
		}
		expected := commands[i] + "\n"
		if output.Content != expected {
			t.Errorf("result %d: expected %q, got %q", i, expected, output.Content)
		}
	}
}

func TestBash_NameAndDescription(t *testing.T) {
	tool := NewBashTool()
	if tool.Name() != "bash" {
		t.Fatalf("expected name 'bash', got %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Fatal("expected non-empty description")
	}
}

func TestBash_ValidWorkingDir(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test valid working dir",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "pwd",
		WorkingDir:  "/tmp",
		TimeoutSecs: iptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d, content: %q", output.ExitCode, output.Content)
	}
	if !strings.Contains(output.Content, "/tmp") {
		t.Fatalf("expected content to contain '/tmp', got %q", output.Content)
	}
}

func TestBash_PipeCommand(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test pipe command",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "echo 'hello world' | tr 'a-z' 'A-Z'",
		TimeoutSecs: iptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCode)
	}
	expected := "HELLO WORLD\n"
	if output.Content != expected {
		t.Fatalf("expected %q, got %q", expected, output.Content)
	}
}

func TestBash_CommandNotFound(t *testing.T) {
	tool := NewBashTool()
	rawArgs, _ := json.Marshal(BashAction{
		ActionMetadata: ActionMetadata{
			Summary:      "test command not found",
			SecurityRisk: SecurityRisk_HIGH,
		},
		Command:     "nonexistent_command_12345",
		TimeoutSecs: iptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*types.CmdOutputObservation)
	if !ok {
		t.Fatalf("expected *CmdOutputObservation, got %T", obs)
	}
	if output.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code, got 0")
	}
	if !strings.Contains(output.Content, "未找到") && !strings.Contains(output.Content, "not found") && !strings.Contains(output.Content, "executable file not found") {
		t.Fatalf("expected error about command not found, got %q", output.Content)
	}
}
