package tools

import (
	"context"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/wen/opentalon/internal/skill"
)

type skillChangeInput struct {
	SkillName    string   `json:"skill_name" jsonschema:"required,description=从系统提示提供的 Skill Catalog 中原样选择的 Skill 名称"`
	Reason       string   `json:"reason" jsonschema:"required,description=基于公开观察说明为什么需要加载或卸载该 Skill"`
	EvidenceRefs []string `json:"evidence_refs" jsonschema:"required,description=成功只读工具结果中返回的 evidence_ref 值列表，必须原样复制；不得填写工具名、错误码或资源 ID"`
}

func buildSkillTools(session *skill.Session) ([]einotool.InvokableTool, error) {
	load, err := toolutils.InferTool(
		"load_skill",
		"根据当前公开证据按需加载一个 Skill。先查看系统提示中的 Skill Catalog，只能原样选择已安装名称；成功后本轮会结束，下一轮将获得该 Skill 的正文和专用工具。",
		func(_ context.Context, input skillChangeInput) (response[skill.Change], error) {
			change, activateErr := session.Activate(input.SkillName, input.Reason, input.EvidenceRefs)
			return platformResponse(change, activateErr), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build load_skill tool: %w", err)
	}
	unload, err := toolutils.InferTool(
		"unload_skill",
		"当新公开证据否定某个已加载 Skill 的假设时卸载它。成功后本轮会结束，下一轮将收回其专用工具。",
		func(_ context.Context, input skillChangeInput) (response[skill.Change], error) {
			change, deactivateErr := session.Deactivate(input.SkillName, input.Reason, input.EvidenceRefs)
			return platformResponse(change, deactivateErr), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build unload_skill tool: %w", err)
	}
	return []einotool.InvokableTool{load, unload}, nil
}
