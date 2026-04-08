package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wen/opentalon/internal/agent"
	"github.com/wen/opentalon/internal/core"
	"github.com/wen/opentalon/internal/types"
	"github.com/wen/opentalon/pkg/config"
)

func main() {
	config.Load()
	cfg := config.Global

	fmt.Println("")
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║      OpenTalon Agent Started        ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Printf("Provider : %s\n", cfg.LLM.Provider)
	fmt.Printf("Endpoint : %s\n", cfg.LLM.Endpoint)
	fmt.Printf("Model    : %s\n\n", cfg.LLM.Model)

	bus := core.NewEventBus()

	agentInstance, err := agent.NewThinkingAgent(cfg.LLM)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create agent failed: %v\n", err)
		os.Exit(1)
	}

	state := types.NewState()
	controller := core.NewController(bus, agentInstance, state)
	core.NewLocalRuntime(bus)

	bus.Subscribe(controller.OnEvent)
	bus.Subscribe(printHandler(state))

	go runMainLoop(bus, state)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\n[main] interrupted, exiting.")
}

func runMainLoop(bus *core.EventBus, state *types.State) {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		currentState := state.AgentState
		fmt.Printf("Current State: %s\n", currentState)

		switch currentState {
		case types.StateLoading:
			fmt.Print("\n[user] ")
			if !scanner.Scan() {
				return
			}
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			bus.Publish(&types.MessageAction{
				BaseEvent: types.BaseEvent{Source: types.SourceUser},
				Content:   line,
			})

		case types.StateRunning:
			if state.PendingAction != nil {
				fmt.Printf("[agent] running command...\n")
			}
			return

		case types.StateAwaitingInput:
			fmt.Print("\n[user] ")
			if !scanner.Scan() {
				return
			}
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			bus.Publish(&types.MessageAction{
				BaseEvent: types.BaseEvent{Source: types.SourceUser},
				Content:   line,
			})

		case types.StateFinished:
			fmt.Printf("\n[system] task finished.\n")
			os.Exit(0)

		case types.StateError:
			fmt.Fprintf(os.Stderr, "\n[system] error: %s\n", state.LastError)
			os.Exit(1)
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
			if e.Result != "" {
				fmt.Printf("\n[result] %s\n", e.Result)
			}
		}
	}
}
