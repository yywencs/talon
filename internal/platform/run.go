package platform

import "context"

// RunConnection identifies the original independently running environment.
// EnvironmentID must be a stable instance identity, not just a scenario name.
type RunConnection struct {
	EnvironmentID, IncidentID, RunID string
	Epoch                            uint64
}

// RunConnector reconnects without resetting the environment. Before returning,
// it must atomically activate Epoch for RunID on the platform and return a client
// bound to that epoch. ALL writes, including dry runs and escalation, must reject
// superseded epochs at the server's side-effect boundary. Already accepted
// asynchronous operations remain queryable. Epoch state must survive server restart.
// A client-side lease check alone does not implement this contract.
type RunConnector interface {
	ConnectRun(context.Context, RunConnection) (ToolOpsPlatform, error)
}
