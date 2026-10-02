// Package runmeta defines immutable, serializable run identity and inputs shared by audit and checkpoints.
package runmeta

// Provenance identifies the code and dataset that produced a run.
type Provenance struct {
	CodeVersion    string `json:"code_version"`
	DatasetVersion string `json:"dataset_version"`
	PromptVersion  string `json:"prompt_version,omitempty"`
	PromptDigest   string `json:"prompt_digest,omitempty"`
}

// Config records the inputs that can materially change Agent behavior.
// Secrets and endpoints must never be stored here.
type Config struct {
	ModelProvider  string `json:"model_provider,omitempty"`
	Model          string `json:"model,omitempty"`
	AgentMaxSteps  int    `json:"agent_max_steps"`
	MaxModelCalls  int    `json:"max_model_calls"`
	AutoApprove    bool   `json:"auto_approve"`
	ContextVersion string `json:"context_version,omitempty"`
}

// Metadata is captured once after run initialization; it contains no audit history.
type Metadata struct {
	RunID      string
	Provenance Provenance
	Config     Config
}
