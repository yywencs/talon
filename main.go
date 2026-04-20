package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	agentpkg "github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/core"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
	"github.com/wen/opentalon/pkg/logger"
	"github.com/wen/opentalon/pkg/utils"
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

	agentInstance, err := agentpkg.NewAgent(cfg.LLM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化 Agent 失败: %v\n", err)
		os.Exit(1)
	}

	session := core.NewSession(agentInstance, core.NewCallbacks(), filepath.Join(cfg.LogDir, "sessions"))
	renderer := &cliRenderer{}
	session.AddEventCallbacks(renderer.HandleEvent)
	session.AddStreamTextDeltaCallbacks(renderer.HandleTextDelta)

	args := os.Args[1:]
	if len(args) > 0 {
		if err := runTurn(context.Background(), session, renderer, strings.Join(args, " ")); err != nil {
			fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
			os.Exit(1)
		}
		renderer.FinishStream()
		return
	}

	if err := runInteractive(context.Background(), session, renderer); err != nil {
		fmt.Fprintf(os.Stderr, "交互模式失败: %v\n", err)
		os.Exit(1)
	}
}

type cliRenderer struct {
	mu        sync.Mutex
	streaming bool
}

func (r *cliRenderer) HandleTextDelta(text string) {
	if text == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Print(text)
	r.streaming = true
}

func (r *cliRenderer) HandleEvent(event types.Event) {
	if event == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	switch e := event.(type) {
	case *types.MessageEvent:
		if e == nil || e.Source != types.SourceAgent {
			return
		}
		if r.streaming {
			return
		}
		text := utils.FlattenTextContent(e.Content)
		if strings.TrimSpace(text) == "" {
			return
		}
		fmt.Println(text)
	case *types.ActionEvent:
		r.finishStreamLocked()
		label := e.ToolName
		if label == "" {
			label = string(e.ActionType)
		}
		if e.Summary != "" {
			fmt.Printf("[action] %s - %s\n", label, e.Summary)
		} else {
			fmt.Printf("[action] %s\n", label)
		}
	case *types.ObservationEvent:
		r.finishStreamLocked()
		text := ""
		if e.Observation != nil {
			text = utils.FlattenTextContent(e.Observation.GetContent())
		}
		if strings.TrimSpace(text) == "" {
			fmt.Printf("[observation] %s\n", e.ToolName)
		} else {
			fmt.Printf("[observation] %s\n%s\n", e.ToolName, text)
		}
	}
}

func (r *cliRenderer) FinishStream() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishStreamLocked()
}

func (r *cliRenderer) finishStreamLocked() {
	if r.streaming {
		fmt.Println()
	}
	r.streaming = false
}

func runTurn(ctx context.Context, session *core.Session, renderer *cliRenderer, input string) error {
	if err := session.SubmitUserMessage(input); err != nil {
		return err
	}
	if err := session.Run(ctx); err != nil {
		renderer.FinishStream()
		return err
	}
	renderer.FinishStream()
	return nil
}

func runInteractive(ctx context.Context, session *core.Session, renderer *cliRenderer) error {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("进入交互模式，输入 exit 或 quit 退出。")
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			fmt.Println()
			return scanner.Err()
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}

		if err := runTurn(ctx, session, renderer, line); err != nil {
			fmt.Fprintf(os.Stderr, "执行失败: %v\n", err)
		}
	}
}
