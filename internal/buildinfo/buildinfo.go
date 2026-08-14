// Package buildinfo exposes values fixed when the Talon binary is linked.
package buildinfo

const defaultAgentVersion = "talon-toolops-agent/v1"

// AgentVersion is persisted with every RunArtifact. Release builds override it
// through go build -ldflags; go run and go test retain the development default.
var AgentVersion = defaultAgentVersion

// Commit identifies the source revision of a built binary. It is informational;
// AgentVersion remains the version used by evaluation and persistence.
var Commit = "unknown"
