package simulator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wen/opentalon/internal/platform"
	"github.com/wen/opentalon/internal/scenario"
)

type scheduledOperation struct {
	dueAt     time.Time
	behavior  map[string]any
	arguments map[string]any
}

// ExecuteRemediation 执行场景预先注册的高层修复动作，不允许任意代码执行。
func (s *Simulator) ExecuteRemediation(ctx context.Context, request platform.RemediationRequest) (platform.Operation, error) {
	if err := contextError(ctx); err != nil {
		return platform.Operation{}, err
	}
	w, err := s.mutableWorld()
	if err != nil {
		return platform.Operation{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if operation, ok, err := w.existingOperationLocked(request.IncidentID, request.IdempotencyKey, platform.OperationRemediation, request.ToolName); ok || err != nil {
		return operation, err
	}
	tool, ok := w.remediationToolLocked(request.ToolName)
	if !ok {
		return platform.Operation{}, fmt.Errorf("%w: remediation tool %q", platform.ErrNotFound, request.ToolName)
	}
	operation := w.newOperationLocked(request.IncidentID, platform.OperationRemediation, request.ToolName, request.IdempotencyKey)
	if tool.AgentAuthorized != nil && !*tool.AgentAuthorized {
		return w.rejectOperationLocked(operation, "agent is not authorized for this remediation", platform.ErrUnauthorized)
	}
	if err := w.validateRemediationArgumentsLocked(tool, request); err != nil {
		return w.rejectOperationLocked(operation, err.Error(), err)
	}
	if err := w.validateRemediationPreconditionsLocked(tool, request); err != nil {
		return w.rejectOperationLocked(operation, err.Error(), err)
	}

	behavior := cloneAnyMap(w.actionBehavior[request.ToolName])
	if result := asString(behavior["result"]); result == "rejected" {
		reason := asString(behavior["reason"])
		return w.rejectOperationLocked(operation, reason, platform.ErrUnauthorized)
	} else if result == "failed" {
		return w.failOperationLocked(operation, asString(behavior["reason"])), nil
	}
	if request.DryRun {
		operation.Status = platform.OperationSucceeded
		operation.UpdatedAt = w.now
		operation.Message = "dry run completed; world state was not changed"
		operation.Result = map[string]any{"dry_run": true}
		w.storeOperationLocked(operation)
		return cloneOperation(operation), nil
	}

	delay, err := optionalDuration(behavior["completion_delay"])
	if err != nil {
		return w.failOperationLocked(operation, err.Error()), err
	}
	w.pending[operation.ID] = scheduledOperation{
		dueAt: w.now.Add(delay), behavior: behavior, arguments: cloneAnyMap(request.Arguments),
	}
	w.storeOperationLocked(operation)
	if delay == 0 {
		w.completeOperationLocked(operation.ID)
		operation = w.operations[operation.ID]
	}
	return cloneOperation(operation), nil
}

// RequestProbe 请求控制器执行小流量验证；Agent 本身不能直接修改流量权重。
func (s *Simulator) RequestProbe(ctx context.Context, request platform.ProbeRequest) (platform.Operation, error) {
	if err := contextError(ctx); err != nil {
		return platform.Operation{}, err
	}
	w, err := s.mutableWorld()
	if err != nil {
		return platform.Operation{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if operation, ok, err := w.existingOperationLocked(request.IncidentID, request.IdempotencyKey, platform.OperationProbe, w.probeTool.Name); ok || err != nil {
		return operation, err
	}
	operation := w.newOperationLocked(request.IncidentID, platform.OperationProbe, w.probeTool.Name, request.IdempotencyKey)
	if request.PolicyID != w.controller.RecoveryPolicy.ID {
		return w.rejectOperationLocked(operation, "unknown recovery policy", platform.ErrNotFound)
	}
	route, ok := w.routes[request.RouteID]
	if !ok {
		return w.rejectOperationLocked(operation, "unknown probe route", platform.ErrNotFound)
	}
	if required := asString(w.probeTool.Preconditions["credential_status_must_be"]); required != "" {
		if credential, exists := w.credentials[route.ProviderID]; !exists || credential.Status != required {
			return w.rejectOperationLocked(operation, "credential status does not satisfy probe policy", platform.ErrPreconditionFailed)
		}
	}

	behavior := w.actionBehavior[w.probeTool.Name]
	if asString(behavior["result"]) == "rejected" {
		return w.rejectOperationLocked(operation, asString(behavior["reason"]), platform.ErrPreconditionFailed)
	}
	if len(w.probes) > 0 {
		return w.rejectOperationLocked(operation, "another probe is already running", platform.ErrConflict)
	}
	session, err := w.newProbeSessionLocked(operation.ID, request.RouteID, behavior)
	if err != nil {
		failed := w.failOperationLocked(operation, err.Error())
		return failed, err
	}
	w.probeAttempt++
	w.lastProbeOutcome = ""
	w.probes[operation.ID] = session
	operation.Status = platform.OperationPending
	operation.Message = "probe accepted; waiting for the first observation window"
	operation.Result = w.probeResultLocked(session, "pending", "")
	w.storeOperationLocked(operation)
	return cloneOperation(operation), nil
}

// RequestRecovery 在探测健康后将路由交还给控制器逐级恢复。
func (s *Simulator) RequestRecovery(ctx context.Context, request platform.RecoveryRequest) (platform.Operation, error) {
	if err := contextError(ctx); err != nil {
		return platform.Operation{}, err
	}
	w, err := s.mutableWorld()
	if err != nil {
		return platform.Operation{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if operation, ok, err := w.existingOperationLocked(request.IncidentID, request.IdempotencyKey, platform.OperationRecovery, "request_recovery"); ok || err != nil {
		return operation, err
	}
	operation := w.newOperationLocked(request.IncidentID, platform.OperationRecovery, "request_recovery", request.IdempotencyKey)
	operation.Result = map[string]any{"route_id": request.RouteID, "policy_id": request.PolicyID}
	if request.PolicyID != w.controller.RecoveryPolicy.ID {
		return w.rejectOperationLocked(operation, "unknown recovery policy", platform.ErrNotFound)
	}
	if w.lastProbeOutcome != "healthy" {
		return w.rejectOperationLocked(operation, "a healthy probe is required before recovery", platform.ErrPreconditionFailed)
	}
	if request.RouteID == "" || request.RouteID != w.affectedRouteID {
		return w.rejectOperationLocked(operation, "recovery route does not match the protected route", platform.ErrPreconditionFailed)
	}
	if len(w.recoveries) > 0 {
		return w.rejectOperationLocked(operation, "another recovery is already running", platform.ErrConflict)
	}
	behavior := w.actionBehavior["request_recovery"]
	if asString(behavior["result"]) == "rejected" {
		return w.rejectOperationLocked(operation, asString(behavior["reason"]), platform.ErrPreconditionFailed)
	}
	session, err := w.newRecoverySessionLocked(operation.ID, request.RouteID, behavior)
	if err != nil {
		failed := w.failOperationLocked(operation, err.Error())
		return failed, err
	}
	w.recoveries[operation.ID] = session
	w.applyRecoveryWeightLocked(session, session.policy.RecoverySteps[0])
	operation.Status = platform.OperationPending
	operation.Message = "recovery accepted; waiting for the first health window"
	operation.Result = w.recoveryResultLocked(session, "pending", "")
	w.storeOperationLocked(operation)
	return cloneOperation(operation), nil
}

// GetOperation 查询异步动作的当前状态和安全输出。
func (s *Simulator) GetOperation(ctx context.Context, query platform.OperationQuery) (platform.Operation, error) {
	if err := contextError(ctx); err != nil {
		return platform.Operation{}, err
	}
	w, err := s.mutableWorld()
	if err != nil {
		return platform.Operation{}, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	operation, ok := w.operations[query.OperationID]
	if !ok || (query.IncidentID != "" && operation.IncidentID != query.IncidentID) {
		return platform.Operation{}, platform.ErrNotFound
	}
	return cloneOperation(operation), nil
}

// EscalateIncident 生成结构化人工交接记录，不执行外部通知。
func (s *Simulator) EscalateIncident(ctx context.Context, request platform.EscalationRequest) (platform.Operation, error) {
	if err := contextError(ctx); err != nil {
		return platform.Operation{}, err
	}
	w, err := s.mutableWorld()
	if err != nil {
		return platform.Operation{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if operation, ok, err := w.existingOperationLocked(request.IncidentID, request.IdempotencyKey, platform.OperationEscalation, w.escalationTool.Name); ok || err != nil {
		return operation, err
	}
	operation := w.newOperationLocked(request.IncidentID, platform.OperationEscalation, w.escalationTool.Name, request.IdempotencyKey)
	if !request.ReasonCode.Valid() {
		return w.rejectOperationLocked(operation, "valid escalation reason_code is required", platform.ErrPreconditionFailed)
	}
	if strings.TrimSpace(request.Reason) == "" {
		return w.rejectOperationLocked(operation, "escalation reason is required", platform.ErrPreconditionFailed)
	}
	if strings.TrimSpace(request.Handoff.AffectedService) == "" || len(request.Handoff.CurrentProtectionState) == 0 ||
		strings.TrimSpace(request.Handoff.RecommendedHumanAction) == "" {
		return w.rejectOperationLocked(operation, "structured escalation handoff is required", platform.ErrPreconditionFailed)
	}
	behavior := w.actionBehavior[w.escalationTool.Name]
	operation.Status = platform.OperationSucceeded
	operation.UpdatedAt = w.now
	operation.Message = "incident escalation accepted"
	operation.Result = map[string]any{
		"reason_code": string(request.ReasonCode), "reason": request.Reason,
		"evidence_refs":           append([]string(nil), request.EvidenceRefs...),
		"attempted_operation_ids": append([]string(nil), request.AttemptedOperationIDs...),
		"handoff":                 request.Handoff,
	}
	if destination := asString(behavior["destination"]); destination != "" {
		operation.Result["destination"] = destination
	}
	w.storeOperationLocked(operation)
	return cloneOperation(operation), nil
}

func (s *Simulator) mutableWorld() (*World, error) {
	if s == nil || s.world == nil {
		return nil, fmt.Errorf("simulator world is not initialized")
	}
	return s.world, nil
}

func (w *World) remediationToolLocked(name string) (scenario.ToolDefinition, bool) {
	for _, tool := range w.remediationTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return scenario.ToolDefinition{}, false
}

func (w *World) existingOperationLocked(incidentID, key string, kind platform.OperationKind, name string) (platform.Operation, bool, error) {
	if strings.TrimSpace(incidentID) == "" {
		return platform.Operation{}, false, fmt.Errorf("%w: incident ID is required", platform.ErrPreconditionFailed)
	}
	if incidentID != w.scenarioID {
		return platform.Operation{}, false, platform.ErrNotFound
	}
	if strings.TrimSpace(key) == "" {
		return platform.Operation{}, false, fmt.Errorf("%w: idempotency key is required", platform.ErrPreconditionFailed)
	}
	if id, ok := w.idempotency[key]; ok {
		operation := w.operations[id]
		if operation.Kind != kind || operation.Name != name {
			return platform.Operation{}, false, fmt.Errorf("%w: idempotency key is already used by another operation", platform.ErrConflict)
		}
		return cloneOperation(operation), true, nil
	}
	return platform.Operation{}, false, nil
}

func (w *World) newOperationLocked(incidentID string, kind platform.OperationKind, name, key string) platform.Operation {
	w.operationSequence++
	return platform.Operation{
		ID: fmt.Sprintf("operation-%03d", w.operationSequence), IncidentID: incidentID,
		Kind: kind, Name: name, Status: platform.OperationPending, IdempotencyKey: key,
		CreatedAt: w.now, UpdatedAt: w.now,
	}
}

func (w *World) storeOperationLocked(operation platform.Operation) {
	w.operations[operation.ID] = cloneOperation(operation)
	w.idempotency[operation.IdempotencyKey] = operation.ID
	w.syncOperationTaskLocked(operation)
}

func (w *World) syncOperationTaskLocked(operation platform.Operation) {
	status := platform.TaskProcessing
	switch operation.Status {
	case platform.OperationSucceeded:
		status = platform.TaskFinished
	case platform.OperationFailed, platform.OperationRejected, platform.OperationCancelled:
		status = platform.TaskFailed
	}
	task, exists := w.tasks[operation.ID]
	if !exists {
		task = platform.ManagedTask{
			ID: operation.ID, Type: string(operation.Kind), Name: operation.Name,
			Idempotent: true, CreatedAt: operation.CreatedAt,
		}
	}
	if pending, ok := w.pending[operation.ID]; ok {
		task.ProviderID = asString(pending.arguments["provider_id"])
	}
	task.Status = status
	task.UpdatedAt = operation.UpdatedAt
	if status == platform.TaskFailed {
		task.LastError = operation.Message
	}
	w.tasks[task.ID] = task
}

func (w *World) rejectOperationLocked(operation platform.Operation, message string, cause error) (platform.Operation, error) {
	operation.Status = platform.OperationRejected
	operation.UpdatedAt = w.now
	operation.Message = message
	w.storeOperationLocked(operation)
	if cause == nil {
		return cloneOperation(operation), nil
	}
	return cloneOperation(operation), fmt.Errorf("%w: %s", cause, message)
}

func (w *World) failOperationLocked(operation platform.Operation, message string) platform.Operation {
	operation.Status = platform.OperationFailed
	operation.UpdatedAt = w.now
	operation.Message = message
	w.storeOperationLocked(operation)
	return cloneOperation(operation)
}

func (w *World) validateRemediationArgumentsLocked(tool scenario.ToolDefinition, request platform.RemediationRequest) error {
	for _, name := range tool.Arguments {
		if name == "idempotency_key" {
			continue
		}
		if name == "expected_version" && request.ExpectedVersion != "" {
			continue
		}
		value, exists := request.Arguments[name]
		if !exists || value == nil {
			return fmt.Errorf("%w: argument %q is required", platform.ErrPreconditionFailed, name)
		}
		if text, isString := value.(string); isString && strings.TrimSpace(text) == "" {
			return fmt.Errorf("%w: argument %q is required", platform.ErrPreconditionFailed, name)
		}
	}
	if request.ExpectedVersion != "" && request.ExpectedVersion != w.activeConfigVersionLocked() {
		return fmt.Errorf("%w: active config version changed", platform.ErrConflict)
	}
	if expected, ok := asInt(request.Arguments["expected_pool_generation"]); ok {
		providerID := asString(request.Arguments["provider_id"])
		if connection, exists := w.connections[providerID]; !exists || connection.PoolGeneration != expected {
			return fmt.Errorf("%w: connection pool generation changed", platform.ErrConflict)
		}
	}
	return nil
}

func (w *World) validateRemediationPreconditionsLocked(tool scenario.ToolDefinition, request platform.RemediationRequest) error {
	if required, _ := asBool(tool.Preconditions["target_version_must_be_known_healthy"]); required {
		target := asString(request.Arguments["target_version"])
		config, ok := w.configs[target]
		if !ok || !config.KnownHealthy {
			return fmt.Errorf("%w: target config is not known healthy", platform.ErrPreconditionFailed)
		}
	}
	if required, _ := asBool(tool.Preconditions["refresh_failed_probe_required"]); required && w.lastProbeOutcome != "hard_stop" {
		return fmt.Errorf("%w: failed refresh probe evidence is required", platform.ErrPreconditionFailed)
	}
	return nil
}

func (w *World) activeConfigVersionLocked() string {
	for id, config := range w.configs {
		if config.Active {
			return id
		}
	}
	return ""
}

func (w *World) completeOperationLocked(operationID string) {
	pending, ok := w.pending[operationID]
	if !ok {
		return
	}
	delete(w.pending, operationID)
	operation := w.operations[operationID]
	if asString(pending.behavior["result"]) == "failed" {
		operation.Status = platform.OperationFailed
		operation.Message = asString(pending.behavior["reason"])
	} else {
		w.applyWorldEffectLocked(asMap(pending.behavior["world_effect"]), pending.arguments)
		operation.Status = platform.OperationSucceeded
		operation.Message = "remediation completed"
		operation.Result = map[string]any{"applied": true}
	}
	operation.UpdatedAt = w.now
	w.storeOperationLocked(operation)
}

func (w *World) applyWorldEffectLocked(effect, arguments map[string]any) {
	if version := asString(effect["config_version"]); version != "" {
		w.publishConfigLocked(version)
	}
	providerID := asString(arguments["provider_id"])
	if providerID != "" {
		connection := w.connections[providerID]
		connection.ProviderID = providerID
		if value, ok := asInt(effect["pool_generation"]); ok {
			connection.PoolGeneration = value
		}
		if value, ok := asInt(effect["resolver_cache_generation"]); ok {
			connection.ResolverCacheGeneration = value
		}
		if value := asString(effect["resolved_ip"]); value != "" {
			connection.ResolvedIP = value
		}
		connection.LastPingAt = cloneTimePointer(&w.now)
		w.connections[providerID] = connection
	}
	w.applyTrafficEffectLocked(effect, providerID)
	serviceID, toolName := w.initialServiceAndTool()
	w.changes = append(w.changes, platform.ChangeRecord{
		ID: fmt.Sprintf("change-operation-%03d", len(w.changes)+1), At: w.now, Kind: "remediation",
		Scope: platform.Scope{IncidentID: w.scenarioID, ServiceID: serviceID, ToolName: toolName, RouteID: w.affectedRouteID, ProviderID: providerID},
		Actor: "toolops-agent",
	})
}

func optionalDuration(value any) (time.Duration, error) {
	text := asString(value)
	if text == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(text)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("invalid completion delay %q", text)
	}
	return duration, nil
}

func cloneOperation(operation platform.Operation) platform.Operation {
	operation.Result = cloneAnyMap(operation.Result)
	return operation
}
