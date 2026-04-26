package tool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	terminalpkg "github.com/wen/opentalon/internal/tool/terminal"
	"github.com/wen/opentalon/internal/types"
)

func fptr(v float64) *float64 {
	return &v
}

func TestBash_SimpleEcho(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hello",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCodeValue())
	}
	if output.OutputText() != "hello\n" {
		t.Fatalf("expected content 'hello\\n', got %q", output.OutputText())
	}
}

func TestBash_WithTimeout(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test with timeout",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 1",
		Timeout: fptr(5),
	})

	start := time.Now()
	obs := tool.Execute(context.Background(), rawArgs)
	elapsed := time.Since(start)

	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCodeValue())
	}
	if elapsed < time.Second {
		t.Fatalf("expected at least 1s elapsed, got %v", elapsed)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test non-zero exit",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "exit 1",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != 1 {
		t.Fatalf("expected exit code 1, got %d", output.ExitCodeValue())
	}
}

func TestBash_EmptyCommand(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test empty command",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "",
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "command is empty") {
		t.Fatalf("expected error message to contain 'command is empty', got %q", output.OutputText())
	}
}

func TestBash_InvalidTimeout_Zero(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test zero timeout",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		Timeout: fptr(0),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "timeout out of range") {
		t.Fatalf("expected error message to contain 'timeout out of range', got %q", output.OutputText())
	}
}

func TestBash_InvalidTimeout_Negative(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test negative timeout",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		Timeout: fptr(-1),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "timeout out of range") {
		t.Fatalf("expected error message to contain 'timeout out of range', got %q", output.OutputText())
	}
}

func TestBash_InvalidTimeout_TooLarge(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test too large timeout",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
		Timeout: fptr(301),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "timeout out of range") {
		t.Fatalf("expected error message to contain 'timeout out of range', got %q", output.OutputText())
	}
}

func TestBash_TimeoutExceeded(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test timeout exceeded",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 10",
		Timeout: fptr(1),
	})

	start := time.Now()
	obs := tool.Execute(context.Background(), rawArgs)
	elapsed := time.Since(start)

	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "timed out") {
		t.Fatalf("expected error message to contain 'timed out', got %q", output.OutputText())
	}
	if elapsed < time.Second && elapsed > 2*time.Second {
		t.Fatalf("expected elapsed time around 1s, got %v", elapsed)
	}
}

func TestBash_NonexistentWorkingDir(t *testing.T) {
	executor := terminalpkg.NewExecutor(terminalpkg.ExecutorConfig{
		WorkingDir: "/nonexistent/path/that/does/not/exist",
	})

	output := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test nonexistent working dir",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo hi",
	})
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "working_dir does not exist") {
		t.Fatalf("expected error message to contain 'working_dir does not exist', got %q", output.OutputText())
	}
}

func TestBash_CtxCancelled(t *testing.T) {
	tool := newBashTool()
	ctx, cancel := context.WithCancel(context.Background())

	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test context cancelled",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "sleep 10",
		Timeout: fptr(30),
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	obs := tool.Execute(ctx, rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != -1 {
		t.Fatalf("expected exit code -1, got %d", output.ExitCodeValue())
	}
	if !strings.Contains(output.OutputText(), "context cancelled") {
		t.Fatalf("expected error message to contain 'context cancelled', got %q", output.OutputText())
	}
}

func TestBash_OutputTruncation(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test output truncation",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "yes | head -c 2000000",
		Timeout: fptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCodeValue())
	}
	maxSize := 1024 * 1024
	if len(output.OutputText()) > maxSize+len("[output truncated]") {
		t.Fatalf("expected content length <= %d, got %d", maxSize+len("[output truncated]"), len(output.OutputText()))
	}
	if !strings.HasSuffix(output.OutputText(), "[output truncated]") {
		t.Fatalf("expected content to end with '[output truncated]', got %q", output.OutputText())
	}
}

func TestBash_ConcurrentExec(t *testing.T) {
	tool := newBashTool()
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
	results := make([]*TerminalObservation, len(commands))
	var mu sync.Mutex

	for i, cmd := range commands {
		wg.Add(1)
		go func(idx int, command string) {
			defer wg.Done()
			rawArgs, _ := json.Marshal(BashTool{
				ToolMetadata: types.ToolMetadata{
					Summary:      "concurrent test",
					SecurityRisk: types.SecurityRisk_HIGH,
				},
				Command: command,
			})
			obs := tool.Execute(context.Background(), rawArgs)
			output, ok := obs.(*TerminalObservation)
			if !ok {
				t.Errorf("expected *TerminalObservation, got %T", obs)
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
		if output.ExitCodeValue() != 0 {
			t.Errorf("result %d: expected exit code 0, got %d", i, output.ExitCodeValue())
		}
		expected := strings.TrimPrefix(commands[i], "echo ") + "\n"
		if output.OutputText() != expected {
			t.Errorf("result %d: expected %q, got %q", i, expected, output.OutputText())
		}
	}
}

func TestBash_NameAndDescription(t *testing.T) {
	tool := newBashTool()
	if tool.Name() != "bash" {
		t.Fatalf("expected name 'bash', got %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Fatal("expected non-empty description")
	}
}

func TestBash_ActionSchema(t *testing.T) {
	converted, err := ToOpenAITool(newBashTool())
	if err != nil {
		t.Fatalf("ToOpenAITool failed: %v", err)
	}

	functionValue, ok := converted["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected function map, got %T", converted["function"])
	}
	parameters, ok := functionValue["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected parameters map, got %T", functionValue["parameters"])
	}
	properties, ok := parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", parameters["properties"])
	}

	for _, name := range []string{"command", "is_input", "timeout", "reset"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("expected action field %q in schema", name)
		}
	}
	if _, ok := properties["working_dir"]; ok {
		t.Fatal("working_dir should not appear in action schema")
	}
	if _, ok := properties["timeout_secs"]; ok {
		t.Fatal("timeout_secs should not appear in action schema")
	}
}

func TestBash_ValidWorkingDir(t *testing.T) {
	executor := terminalpkg.NewExecutor(terminalpkg.ExecutorConfig{
		WorkingDir: "/tmp",
	})

	output := executor.Execute(context.Background(), BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test valid working dir",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "pwd",
	})
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d, content: %q", output.ExitCodeValue(), output.OutputText())
	}
	if !strings.Contains(output.OutputText(), "/tmp") {
		t.Fatalf("expected content to contain '/tmp', got %q", output.OutputText())
	}
}

func TestBash_PipeCommand(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test pipe command",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "echo 'hello world' | tr 'a-z' 'A-Z'",
		Timeout: fptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() != 0 {
		t.Fatalf("expected exit code 0, got %d", output.ExitCodeValue())
	}
	expected := "HELLO WORLD\n"
	if output.OutputText() != expected {
		t.Fatalf("expected %q, got %q", expected, output.OutputText())
	}
}

func TestBash_CommandNotFound(t *testing.T) {
	tool := newBashTool()
	rawArgs, _ := json.Marshal(BashTool{
		ToolMetadata: types.ToolMetadata{
			Summary:      "test command not found",
			SecurityRisk: types.SecurityRisk_HIGH,
		},
		Command: "nonexistent_command_12345",
		Timeout: fptr(30),
	})

	obs := tool.Execute(context.Background(), rawArgs)
	output, ok := obs.(*TerminalObservation)
	if !ok {
		t.Fatalf("expected *TerminalObservation, got %T", obs)
	}
	if output.ExitCodeValue() == 0 {
		t.Fatalf("expected non-zero exit code, got 0")
	}
	if !strings.Contains(output.OutputText(), "未找到") && !strings.Contains(output.OutputText(), "not found") && !strings.Contains(output.OutputText(), "executable file not found") {
		t.Fatalf("expected error about command not found, got %q", output.OutputText())
	}
}
