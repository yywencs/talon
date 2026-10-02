package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/wen/opentalon/internal/controller"
	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/runartifact"
	"github.com/wen/opentalon/internal/simulator"
	"github.com/wen/opentalon/internal/workflow"
)

func artifactOperations(snapshot simulator.Snapshot) []platform.Operation {
	result := make([]platform.Operation, 0, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		result = append(result, operation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

func artifactFinalState(state workflow.State, snapshot simulator.Snapshot) runartifact.FinalState {
	result := runartifact.FinalState{
		WorkflowState: state,
		VirtualTime:   snapshot.Now,
		Routes:        make([]platform.Route, 0, len(snapshot.Routes)),
		Providers:     make([]runartifact.ProviderState, 0, len(snapshot.Providers)),
		Configs:       make([]runartifact.ConfigState, 0, len(snapshot.Configs)),
		Connections:   make([]runartifact.ConnectionState, 0, len(snapshot.Connections)),
		Tasks:         make([]runartifact.TaskState, 0, len(snapshot.Tasks)),
		Traffic:       snapshot.Traffic,
	}
	for _, route := range snapshot.Routes {
		result.Routes = append(result.Routes, route)
	}
	for _, provider := range snapshot.Providers {
		result.Providers = append(result.Providers, runartifact.ProviderState{
			ID: provider.ID, Health: provider.Health, SchemaCompatible: provider.SchemaCompatible,
		})
	}
	for _, config := range snapshot.Configs {
		result.Configs = append(result.Configs, runartifact.ConfigState{
			ID: config.ID, Active: config.Active, KnownHealthy: config.KnownHealthy,
		})
	}
	for _, connection := range snapshot.Connections {
		result.Connections = append(result.Connections, runartifact.ConnectionState{
			ProviderID: connection.ProviderID, PoolGeneration: connection.PoolGeneration,
			ResolverCacheGeneration: connection.ResolverCacheGeneration, ResolvedIP: connection.ResolvedIP,
			ActiveConnections: connection.ActiveConnections, TargetConnections: connection.TargetConnections,
			ConfigFingerprint: connection.ConfigFingerprint, LastPingAt: connection.LastPingAt,
		})
	}
	for _, task := range snapshot.Tasks {
		result.Tasks = append(result.Tasks, runartifact.TaskState{
			ID: task.ID, Type: task.Type, Name: task.Name, Status: task.Status,
			ProviderID: task.ProviderID, Attempts: task.Attempts, Idempotent: task.Idempotent,
			LastError: task.LastError,
		})
	}
	sort.Slice(result.Routes, func(i, j int) bool { return result.Routes[i].ID < result.Routes[j].ID })
	sort.Slice(result.Providers, func(i, j int) bool { return result.Providers[i].ID < result.Providers[j].ID })
	sort.Slice(result.Configs, func(i, j int) bool { return result.Configs[i].ID < result.Configs[j].ID })
	sort.Slice(result.Connections, func(i, j int) bool { return result.Connections[i].ProviderID < result.Connections[j].ProviderID })
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	return result
}

type recordingInvestigator struct {
	next     controller.Investigator
	workflow *workflow.IncidentWorkflow
	recorder *runartifact.Recorder
	store    runartifact.Store
}

func (r *recordingInvestigator) IncidentID() string { return r.next.IncidentID() }

func (r *recordingInvestigator) Investigate(ctx context.Context, instruction string) (err error) {
	r.recorder.BeginAgentRun(instruction, r.workflow.Snapshot())
	err = r.next.Investigate(ctx, instruction)
	r.recorder.EndAgentRun(r.workflow.Snapshot(), err)
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if persistErr := r.store.Upsert(persistCtx, r.recorder.Snapshot()); persistErr != nil {
		return errors.Join(err, fmt.Errorf("persist Agent run artifact: %w", persistErr))
	}
	return err
}

var _ controller.Investigator = (*recordingInvestigator)(nil)

// finishRunAudit runs after the simulator clock has stopped, including on errors.
func finishRunAudit(ctx context.Context, store runartifact.Store, recorder *runartifact.Recorder, flow *workflow.IncidentWorkflow, service *simulator.Simulator, result *Result, runErr error) error {
	snapshot := workflow.Snapshot{}
	if flow != nil {
		snapshot = flow.Snapshot()
	}
	if service != nil {
		result.World = service.Snapshot()
		recorder.RecordFinalState(artifactOperations(result.World), artifactFinalState(snapshot.State, result.World))
	}
	artifact := recorder.Finish(string(result.Controller.Reason), snapshot, runErr)
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := store.Upsert(persistCtx, artifact); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("persist final run artifact: %w", err))
		artifact = recorder.Finish(string(result.Controller.Reason), snapshot, runErr)
	}
	result.Artifact = artifact
	return runErr
}
