package platform

import "context"

// RunConnection identifies the original independently running environment.
// EnvironmentID must be a stable instance identity, not just a scenario name.
type RunConnection struct {
	EnvironmentID string `json:"environment_id"`
	IncidentID    string `json:"incident_id"`
	RunID         string `json:"run_id"`
	Epoch         uint64 `json:"epoch"`
}

// RunConnector reconnects without resetting the environment. Before returning,
// it must atomically activate Epoch for RunID on the platform and return a client
// bound to that epoch. ALL writes, including dry runs and escalation, must reject
// superseded epochs at the server's side-effect boundary. Already accepted
// asynchronous operations remain queryable. Epoch state must survive reconnects within an environment instance. A server
// that cannot restore its world must generate a new EnvironmentID on restart.
// A client-side lease check alone does not implement this contract.
type RunConnector interface {
	ConnectRun(context.Context, RunConnection) (ToolOpsPlatform, error)
}

// OperationKeyReader is read-only; lookup never submits or retries a write.
type OperationKeyReader interface {
	GetOperationByKey(context.Context, OperationKeyQuery) (Operation, error)
}
type OperationKeyQuery struct {
	IncidentID     string `json:"incident_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// EnvironmentMetadataReader reports authoritative metadata of a loaded environment.
type EnvironmentMetadata struct{ EnvironmentID, IncidentID, DatasetVersion string }
type EnvironmentMetadataReader interface {
	EnvironmentMetadata(context.Context) (EnvironmentMetadata, error)
}
