package core

import (
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/wen/opentalon/internal/types"
)

// Runtime 接口定义了"执行 Action 并返回 Observation"的能力。
// 之所以用接口而不是具体类型，是因为后续可以通过 preset_sandbox_spec_service 注入 Docker/Remote 等不同的执行环境。
type Runtime interface {
	Execute(action types.Action) types.Observation
}

// LocalRuntime 是最小实现的本地 shell 执行器。
// 它通过 EventBus 订阅 agent 发出的 action，执行后把 observation 广播回总线。
// 注意：LocalRuntime 和 Controller 共享同一个 EventBus，这是事件闭环的关键。
type LocalRuntime struct {
	bus *EventBus
}

// NewLocalRuntime 将 LocalRuntime 订阅到 EventBus，并返回 Runtime 接口。
// 订阅完成后，LocalRuntime 会在后台 goroutine 监听事件并同步执行。
func NewLocalRuntime(bus *EventBus) Runtime {
	rt := &LocalRuntime{bus: bus}
	bus.Subscribe(rt.onEvent)
	return rt
}

// onEvent 是 EventBus 的订阅回调，只处理 CmdRunAction，其他类型直接忽略。
// CmdRunAction 被执行后，结果通过 bus.Publish() 广播。
//
// 注意：执行结果再次进入 bus，所以 Controller.OnEvent() 也会再次被调用。
// 这形成了完整闭环：agent action -> runtime 执行 -> observation -> controller 收到 -> 下一轮 step
func (rt *LocalRuntime) onEvent(evt types.Event) {
	action, ok := evt.(*types.CmdRunAction)
	if !ok {
		return
	}

	go func() {
		obs := rt.Execute(action)
		rt.bus.Publish(obs)
	}()

}

// Execute 同步执行一条 shell 命令并返回观察结果。
// 命令错误（不存在）和非零退出码都被归入 CmdOutputObservation，不会上报到 Controller。
// 超时使用 action.GetBase().Timeout，如果为 0 则不设超时。
func (rt *LocalRuntime) Execute(action types.Action) types.Observation {
	cmdRun, ok := action.(*types.CmdRunAction)
	if !ok {
		return &types.CmdOutputObservation{
			BaseEvent: types.BaseEvent{Source: types.SourceEnvironment},
			Content:   "unsupported action type",
			ExitCode:  -1,
		}
	}

	cmd := exec.Command("sh", "-c", cmdRun.Command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	timeout := cmdRun.GetBase().Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	stdout, err := execWithTimeout(cmd, timeout)
	content := string(stdout)
	if err != nil {
		content = err.Error()
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	return &types.CmdOutputObservation{
		BaseEvent: types.BaseEvent{
			Source: types.SourceAgent,
			Cause:  cmdRun.GetBase().ID,
		},
		Content:  content,
		ExitCode: exitCode,
	}
}

func execWithTimeout(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	resultCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	go func() {
		out, readErr := io.ReadAll(stdout)
		if readErr != nil {
			errCh <- fmt.Errorf("read stdout: %w", readErr)
			return
		}
		if err := cmd.Wait(); err != nil {
			errOut, _ := io.ReadAll(stderr)
			errCh <- fmt.Errorf("%s: %s", err, string(errOut))
			return
		}
		resultCh <- out
	}()

	select {
	case out := <-resultCh:
		return out, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-resultCh
		return nil, fmt.Errorf("command timed out after %v", timeout)
	}
}
