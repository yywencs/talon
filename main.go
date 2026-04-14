package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/core"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/logger"
)

// main 是 CLI 入口，根据 os.Args 分流到两种模式：
//  1. One-Shot 模式：os.Args 带参数，拼接为一句用户指令，发送一次后等待完成并退出。
//  2. Interactive 模式：无参数则进入 REPL，逐行读取用户输入，直到 exit/quit。
func main() {
	config.Load()
	logger.LogDir = config.Global.LogDir
	logger.SetupLogger()
	cfg := config.Global

	fmt.Println("")
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║      OpenTalon Agent Started        ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Printf("Provider : %s\n", cfg.LLM.Provider)
	fmt.Printf("Endpoint : %s\n", cfg.LLM.Endpoint)
	fmt.Printf("Model    : %s\n\n", cfg.LLM.Model)

	bus := core.NewEventBus()

	agentInstance, err := agent.NewBaseAgent(cfg.LLM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create agent failed: %v\n", err)
		os.Exit(1)
	}

	state := types.NewState()
	controller := core.NewController(bus, agentInstance, state)
	core.NewLocalRuntime(bus)

	bus.Subscribe(controller.OnEvent)
	bus.Subscribe(printHandler(state))
	bus.Start()

	args := os.Args[1:]
	if len(args) > 0 {
		oneShot(bus, state, strings.Join(args, " "))
		return
	}

	repl(bus, state)
}

// oneShot 实现 One-Shot 模式：发送一条用户消息后，等待任务完成或出错并退出。
func oneShot(bus *core.EventBus, state *types.State, content string) {
	if strings.TrimSpace(content) == "" {
		fmt.Fprintln(os.Stderr, "empty input")
		os.Exit(1)
	}

	bus.Publish(&types.MessageAction{
		BaseEvent: types.BaseEvent{Source: types.SourceUser},
		Content:   content,
	})

	for {
		s := state.AgentState
		switch s {
		case types.StateFinished:
			fmt.Println("\n[system] task finished.")
			os.Exit(0)
		case types.StateError:
			fmt.Fprintf(os.Stderr, "\n[system] error: %s\n", state.LastError)
			os.Exit(1)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// repl 实现 Interactive 模式：逐行读取用户输入，支持 exit/quit 退出。
func repl(bus *core.EventBus, state *types.State) {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("👤 你: ")
		if !scanner.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return
		}

		state.AgentState = types.StateRunning

		// 将用户输入封装为 SourceUser 的 MessageAction 并发布到总线
		bus.Publish(&types.MessageAction{
			BaseEvent: types.BaseEvent{Source: types.SourceUser},
			Content:   line,
		})

		// 等待这一轮推理/执行进入可交互阶段或结束
		for {
			s := state.AgentState
			if s == types.StateAwaitingInput || s == types.StateFinished || s == types.StateError {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if state.AgentState == types.StateFinished {
			state.PendingAction = nil
			state.AgentState = types.StateAwaitingInput
		}
		if state.AgentState == types.StateError {
			fmt.Fprintf(os.Stderr, "\n[system] error: %s\n", state.LastError)
			return
		}
	}
}

func printHandler(state *types.State) core.Handler {
	return func(evt types.Event) {
		switch e := evt.(type) {
		case *types.MessageAction:
			if e.GetBase().Source == types.SourceAgent {
				fmt.Printf("\n[agent] %s\n", e.Content)
			}
		case *types.FinishAction:
			result := e.Result
			if result == "" {
				result = "任务圆满完成！"
			}
			fmt.Printf("\n✅ [任务结束]: %s\n", result)
		}
	}
}
