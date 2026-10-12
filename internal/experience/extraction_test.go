package experience_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	"github.com/wen/opentalon/internal/experience"
)

type extractionModel struct {
	messages  []*schema.Message
	calls     int
	tools     []*schema.ToolInfo
	failAfter int
}

func (m *extractionModel) WithTools(t []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = t
	return m, nil
}
func (m *extractionModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	if m.failAfter > 0 && m.calls >= m.failAfter {
		return nil, errors.New("model unavailable")
	}
	v := m.messages[min(m.calls, len(m.messages)-1)]
	m.calls++
	return v, nil
}
func (m *extractionModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
func toolCall(name string, input any) *schema.Message {
	b, _ := json.Marshal(input)
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(b)}}})
}
func finishCall(reason string) *schema.Message {
	return toolCall("finish_extraction", experience.FinishExtraction{Reason: reason})
}
func candidateCall(c experience.Card) *schema.Message {
	d := experience.CandidateDraft{Title: c.Title, Symptoms: c.Symptoms, Component: c.Component, ContextTags: c.ContextTags, CauseFamily: c.CauseFamily, ApplicableWhen: c.ApplicableWhen, Checks: c.Checks, ConditionalAction: c.ConditionalAction, Verification: c.Verification, Exceptions: c.Exceptions}
	for _, s := range c.Sources {
		d.Sources = append(d.Sources, experience.DraftSource{RunID: s.RunID, EvidenceRefs: s.EvidenceRefs})
	}
	return toolCall("submit_candidate", d)
}
func TestExtractionPartialAndDuplicate(t *testing.T) {
	ctx, s, c := setup(t)
	snap, e := experience.Load[experience.Snapshot](ctx, s.Store, "snapshot", c.Sources[0].SnapshotDigest)
	require.NoError(t, e)
	m := &extractionModel{messages: []*schema.Message{candidateCall(c), candidateCall(c)}, failAfter: 2}
	r, e := experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
	require.ErrorContains(t, e, "model unavailable")
	require.Equal(t, "partial", r.Status)
	require.Len(t, r.Cards, 1)
	_, e = experience.Load[experience.Card](ctx, s.Store, "candidate", r.Cards[0])
	require.NoError(t, e)
	saved, e := experience.Load[experience.Extraction](ctx, s.Store, "extraction", experience.Digest(r))
	require.NoError(t, e)
	require.Equal(t, r.ID, saved.ID)
	p, e := experience.Load[experience.ExtractionPromptSpec](ctx, s.Store, "extraction_prompt", r.PromptDigest)
	require.NoError(t, e)
	require.Equal(t, "experience-extraction/v2", p.Version)
	require.Len(t, m.tools, 2)
	// The full content participates in identity; changing advice cannot collide.
	c.ConditionalAction = "new conditional action"
	next, e := experience.Extract(ctx, s.Store, &extractionModel{messages: []*schema.Message{candidateCall(c), finishCall("done")}}, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.NotEqual(t, r.Cards[0], next.Cards[0])
}
func TestExtractionRejectsSystemFieldsAndCorrectsReference(t *testing.T) {
	ctx, s, c := setup(t)
	snap, e := experience.Load[experience.Snapshot](ctx, s.Store, "snapshot", c.Sources[0].SnapshotDigest)
	require.NoError(t, e)
	bad := candidateCall(c)
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(bad.ToolCalls[0].Function.Arguments), &fields))
	fields["id"] = "injected"
	m := &extractionModel{messages: []*schema.Message{toolCall("submit_candidate", fields), candidateCall(c), finishCall("corrected")}}
	r, e := experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.Len(t, r.Cards, 1)
	require.Contains(t, r.Feedback[0], "unknown field")
	badCard := c
	badCard.Sources = []experience.Source{{RunID: c.Sources[0].RunID, EvidenceRefs: []string{"invented"}}}
	m = &extractionModel{messages: []*schema.Message{candidateCall(badCard), candidateCall(c), finishCall("corrected")}}
	r, e = experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.Len(t, r.Cards, 1)
}
func TestExtractionAbstentionAndPromptFiles(t *testing.T) {
	ctx, s, c := setup(t)
	snap, e := experience.Load[experience.Snapshot](ctx, s.Store, "snapshot", c.Sources[0].SnapshotDigest)
	require.NoError(t, e)
	r, e := experience.Extract(ctx, s.Store, &extractionModel{messages: []*schema.Message{finishCall("insufficient evidence")}}, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.Equal(t, "abstained", r.Status)
	require.Equal(t, "insufficient evidence", r.Reason)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"test/v1","protocol":"candidate-tools/v1","description":"test"}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "system.md"), []byte("test instruction"), 0600))
	p, e := experience.LoadExtractionPrompt(dir)
	require.NoError(t, e)
	require.Equal(t, "test/v1", p.Version)
	custom, e := experience.Extract(ctx, s.Store, &extractionModel{messages: []*schema.Message{finishCall("none")}}, "test", []experience.Snapshot{snap}, dir)
	require.NoError(t, e)
	require.NotEqual(t, r.PromptDigest, custom.PromptDigest)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"test","protocol":"wrong"}`), 0600))
	_, e = experience.LoadExtractionPrompt(dir)
	require.Error(t, e)
}

func TestExtractionBoundsAndProtocol(t *testing.T) {
	ctx, s, c := setup(t)
	snap, e := experience.Load[experience.Snapshot](ctx, s.Store, "snapshot", c.Sources[0].SnapshotDigest)
	require.NoError(t, e)
	for _, tc := range []struct {
		name     string
		messages []*schema.Message
		want     string
	}{
		{"plain text", []*schema.Message{schema.AssistantMessage("{}", nil)}, "exactly one"},
		{"multiple calls", []*schema.Message{schema.AssistantMessage("", append(candidateCall(c).ToolCalls, finishCall("done").ToolCalls...))}, "exactly one"},
		{"empty reason", []*schema.Message{finishCall(" ")}, "finish requires"},
		{"unknown tool", []*schema.Message{toolCall("execute_repair", map[string]string{})}, "unsupported"},
		{"no finish", []*schema.Message{candidateCall(c)}, "budget exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &extractionModel{messages: tc.messages}
			_, e := experience.Extract(ctx, s.Store, m, "test", []experience.Snapshot{snap})
			require.ErrorContains(t, e, tc.want)
		})
	}
	messages := []*schema.Message{}
	for i := 0; i < 6; i++ {
		candidate := c
		candidate.Title = string(rune('a' + i))
		messages = append(messages, candidateCall(candidate))
	}
	messages = append(messages, finishCall("five accepted"))
	r, e := experience.Extract(ctx, s.Store, &extractionModel{messages: messages}, "test", []experience.Snapshot{snap})
	require.NoError(t, e)
	require.Len(t, r.Cards, 5)
	require.Contains(t, r.Feedback[5], "at most five")
	c.Component = " "
	_, e = experience.Extract(ctx, s.Store, &extractionModel{messages: []*schema.Message{candidateCall(c)}}, "test", []experience.Snapshot{snap})
	require.ErrorContains(t, e, "must not be blank")
}
