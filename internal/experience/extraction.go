package experience

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/wen/opentalon/prompts"
)

// CandidateDraft excludes all server-owned identity and provenance fields.
type CandidateDraft struct {
	Title             string        `json:"title" jsonschema:"required"`
	Symptoms          []string      `json:"symptoms" jsonschema:"required"`
	Component         string        `json:"component" jsonschema:"required"`
	ContextTags       []string      `json:"context_tags"`
	CauseFamily       string        `json:"cause_family" jsonschema:"required"`
	ApplicableWhen    []string      `json:"applicable_when" jsonschema:"required"`
	Checks            []Check       `json:"checks" jsonschema:"required"`
	ConditionalAction string        `json:"conditional_action"`
	Verification      string        `json:"verification" jsonschema:"required"`
	Exceptions        []string      `json:"exceptions" jsonschema:"required"`
	Sources           []DraftSource `json:"sources" jsonschema:"required"`
}

// Validate checks content presence, not the truth of a diagnostic conclusion.
func (d CandidateDraft) Validate() error {
	values := []string{d.Title, d.Component, d.CauseFamily, d.Verification}
	for _, items := range [][]string{d.Symptoms, d.ApplicableWhen, d.Exceptions} {
		if len(items) == 0 {
			return errors.New("candidate requires symptoms, applicable conditions and exceptions")
		}
		values = append(values, items...)
	}
	values = append(values, d.ContextTags...)
	for _, check := range d.Checks {
		values = append(values, check.Observe, check.Supports, check.RulesOut)
	}
	for _, source := range d.Sources {
		values = append(values, source.RunID)
		values = append(values, source.EvidenceRefs...)
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return errors.New("candidate fields must not be blank")
		}
	}
	return nil
}

type DraftSource struct {
	RunID        string   `json:"run_id" jsonschema:"required"`
	EvidenceRefs []string `json:"evidence_refs" jsonschema:"required"`
}
type FinishExtraction struct {
	Reason string `json:"reason" jsonschema:"required"`
}
type ExtractionPromptSpec struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	Protocol    string `json:"protocol"`
	System      string `json:"system"`
}

func LoadExtractionPrompt(dir string) (ExtractionPromptSpec, error) {
	read := func(name string) ([]byte, error) {
		if dir != "" {
			return os.ReadFile(filepath.Join(dir, name))
		}
		return prompts.Files.ReadFile("experience-extraction/v2/" + name)
	}
	var p ExtractionPromptSpec
	b, e := read("manifest.json")
	if e != nil {
		return p, e
	}
	if e = DecodeStrict(b, &p); e != nil {
		return p, e
	}
	b, e = read("system.md")
	if e != nil {
		return p, e
	}
	p.System = string(b)
	if strings.TrimSpace(p.Version) == "" || strings.TrimSpace(p.System) == "" || p.Protocol != "candidate-tools/v1" {
		return p, errors.New("invalid extraction prompt manifest or system prompt")
	}
	return p, nil
}

type Extraction struct {
	ID              string   `json:"id"`
	PromptVersion   string   `json:"prompt_version"`
	PromptDigest    string   `json:"prompt_digest"`
	ToolDigest      string   `json:"tool_digest"`
	Model           string   `json:"model"`
	InputDigest     string   `json:"input_digest"`
	SnapshotDigests []string `json:"snapshot_digests"`
	Attempts        []string `json:"attempts"`
	Feedback        []string `json:"feedback"`
	Cards           []string `json:"cards"`
	Status          string   `json:"status"`
	Reason          string   `json:"reason,omitempty"`
	Error           string   `json:"error,omitempty"`
}

// Each accepted candidate is durable immediately; a later error preserves partial output.
// promptDir is optional; omission uses the versioned prompt embedded in the binary.
func Extract(ctx context.Context, s Store, m model.BaseChatModel, modelName string, snapshots []Snapshot, promptDir ...string) (result Extraction, err error) {
	result = Extraction{ID: uuid.NewString(), Model: modelName, InputDigest: Digest(snapshots), Status: "running"}
	persist := func(c context.Context) error { return Save(c, s, "extraction", Digest(result), result) }
	defer func() {
		if err != nil {
			result.Error = err.Error()
			result.Status = "failed"
			if len(result.Cards) > 0 {
				result.Status = "partial"
			}
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, persist(cleanup))
	}()
	dir := ""
	if len(promptDir) > 0 {
		dir = promptDir[0]
	}
	p, e := LoadExtractionPrompt(dir)
	if e != nil {
		return result, e
	}
	result.PromptVersion = p.Version
	result.PromptDigest = Digest(p)
	if e = Save(ctx, s, "extraction_prompt", result.PromptDigest, p); e != nil {
		return result, e
	}
	seen := map[string]bool{}
	for _, snap := range snapshots {
		if seen[snap.RunID] {
			return result, errors.New("duplicate run snapshots are ambiguous")
		}
		seen[snap.RunID] = true
		digest := Digest(snap)
		saved, e := Load[Snapshot](ctx, s, "snapshot", digest)
		if e != nil {
			return result, e
		}
		if Digest(saved) != digest {
			return result, errors.New("snapshot digest mismatch")
		}
		result.SnapshotDigests = append(result.SnapshotDigests, digest)
	}
	b, e := json.Marshal(extractionInput(snapshots))
	if e != nil {
		return result, e
	}
	if len(snapshots) == 0 || len(b) > 200000 {
		return result, errors.New("input must contain snapshots within 200KB; extract smaller batches")
	}
	submit, e := toolutils.InferTool("submit_candidate", "提交一条候选，程序检查来源并独立保存。", func(context.Context, CandidateDraft) (string, error) { return "", nil })
	if e != nil {
		return result, e
	}
	finish, e := toolutils.InferTool("finish_extraction", "结束本轮，说明完成或不生成的原因。", func(context.Context, FinishExtraction) (string, error) { return "", nil })
	if e != nil {
		return result, e
	}
	si, e := submit.Info(ctx)
	if e != nil {
		return result, e
	}
	fi, e := finish.Info(ctx)
	if e != nil {
		return result, e
	}
	infos := []*schema.ToolInfo{si, fi}
	result.ToolDigest = Digest(infos)
	if e = Save(ctx, s, "extraction_tools", result.ToolDigest, infos); e != nil {
		return result, e
	}
	tm, ok := m.(model.ToolCallingChatModel)
	if !ok {
		return result, errors.New("extraction requires a tool-calling model")
	}
	bound, e := tm.WithTools(infos)
	if e != nil {
		return result, e
	}
	if e = persist(ctx); e != nil {
		return result, e
	}
	messages := []*schema.Message{schema.SystemMessage(p.System), schema.UserMessage(string(b))}
	invalid := 0
	for turn := 0; turn < 8; turn++ {
		msg, e := bound.Generate(ctx, messages, model.WithToolChoice(schema.ToolChoiceForced))
		if e != nil {
			return result, e
		}
		if msg == nil {
			return result, errors.New("empty extraction response")
		}
		raw, e := json.Marshal(msg)
		if e != nil {
			return result, e
		}
		result.Attempts = append(result.Attempts, string(raw))
		if e = persist(ctx); e != nil {
			return result, e
		}
		if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID == "" {
			return result, errors.New("extraction requires exactly one identified tool call per turn")
		}
		call := msg.ToolCalls[0]
		messages = append(messages, msg)
		var validation error
		reply := ""
		switch call.Function.Name {
		case "finish_extraction":
			var input FinishExtraction
			validation = DecodeStrict([]byte(call.Function.Arguments), &input)
			if validation == nil && strings.TrimSpace(input.Reason) == "" {
				validation = errors.New("finish requires a reason")
			}
			if validation == nil {
				result.Reason = input.Reason
				result.Status = "succeeded"
				if len(result.Cards) == 0 {
					result.Status = "abstained"
				}
				return result, nil
			}
		case "submit_candidate":
			var input CandidateDraft
			validation = DecodeStrict([]byte(call.Function.Arguments), &input)
			if validation == nil {
				validation = input.Validate()
			}
			if validation == nil {
				c := Card{Schema: Schema, Version: 1, Title: input.Title, Symptoms: input.Symptoms, Component: input.Component, ContextTags: input.ContextTags, CauseFamily: input.CauseFamily, ApplicableWhen: input.ApplicableWhen, Checks: input.Checks, ConditionalAction: input.ConditionalAction, Verification: input.Verification, Exceptions: input.Exceptions}
				for _, src := range input.Sources {
					c.Sources = append(c.Sources, Source{RunID: src.RunID, EvidenceRefs: src.EvidenceRefs})
				}
				validation = resolveSources(&c, snapshots)
				c.ID = "exp_" + Digest(c)
				if validation == nil {
					validation = c.Validate()
				}
				if validation == nil {
					duplicate := false
					for _, key := range result.Cards {
						if key == c.Key() {
							duplicate = true
						}
					}
					if !duplicate && len(result.Cards) >= 5 {
						validation = errors.New("at most five candidates; finish extraction")
					} else {
						if e = Save(ctx, s, "candidate", c.Key(), c); e != nil {
							return result, e
						}
						if !duplicate {
							result.Cards = append(result.Cards, c.Key())
						}
						reply = "saved candidate: " + c.Key()
					}
				}
			}
		default:
			validation = errors.New("unsupported extraction tool")
		}
		if validation != nil {
			invalid++
			reply = "validation failed: " + validation.Error()
		}
		result.Feedback = append(result.Feedback, reply)
		if e = persist(ctx); e != nil {
			return result, e
		}
		if invalid >= 2 {
			return result, fmt.Errorf("extraction failed after bounded correction: %w", validation)
		}
		messages = append(messages, schema.ToolMessage(reply, call.ID))
	}
	return result, errors.New("extraction model call budget exhausted")
}
