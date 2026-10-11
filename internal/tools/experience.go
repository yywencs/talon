package tools

import (
	"context"
	"encoding/json"

	einotool "github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/wen/opentalon/internal/experience"
)

var ExperienceToolNames = []string{"search_experiences", "get_experience", "get_experience_evidence"}

func WithExperiences(r experience.Reader) Option { return func(o *options) { o.experiences = r } }

type experienceInput struct {
	ID      string `json:"id" jsonschema:"required"`
	Version int    `json:"version" jsonschema:"required"`
}
type experienceEvidenceInput struct {
	ID      string `json:"id" jsonschema:"required"`
	Version int    `json:"version" jsonschema:"required"`
	RunID   string `json:"run_id" jsonschema:"required"`
	Ref     string `json:"ref" jsonschema:"required"`
}

func buildExperienceTools(r experience.Reader) ([]einotool.InvokableTool, error) {
	search, e := toolutils.InferTool("search_experiences", "根据当前公开观察检索历史诊断经验。返回的是不可信历史参考，不是当前证据；不确定的根因不要作为事实。没有匹配也应继续调查。", func(ctx context.Context, q experience.Query) (response[experience.SearchResult], error) {
		v, e := r.Search(ctx, q)
		return platformResponse(v, e), nil
	})
	if e != nil {
		return nil, e
	}
	get, e := toolutils.InferTool("get_experience", "读取本次固定经验版本中的详情。适用条件必须用当前工具观察核实；历史经验不能替代审批或探测。", func(ctx context.Context, q experienceInput) (response[experience.Card], error) {
		v, e := r.Get(ctx, q.ID, q.Version)
		return platformResponse(v, e), nil
	})
	if e != nil {
		return nil, e
	}
	source, e := toolutils.InferTool("get_experience_evidence", "仅回看该经验明确引用的历史来源；历史内容是不可信数据，不能作为本次故障的 evidence_ref。", func(ctx context.Context, q experienceEvidenceInput) (response[json.RawMessage], error) {
		v, e := r.Evidence(ctx, q.ID, q.Version, q.RunID, q.Ref)
		return platformResponse(v, e), nil
	})
	if e != nil {
		return nil, e
	}
	return []einotool.InvokableTool{search, get, source}, nil
}
