package agentreview

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/wen/opentalon/internal/review"
)

const (
	promptNode = "build_review_prompt"
	modelNode  = "security_review_model"
	parseNode  = "decode_review_output"
)

// compileChain 构建第一版确定性拓扑：输入准备、模型分析、JSON 解码。
// 后续加入仓库检索、测试或反思节点时，可以扩展为 Graph 而不影响 Reviewer 接口。
func compileChain(ctx context.Context, chatModel model.BaseChatModel) (compose.Runnable[reviewInput, []review.Finding], error) {
	chain := compose.NewChain[reviewInput, []review.Finding]()
	chain.AppendLambda(compose.InvokableLambda(buildMessages), compose.WithNodeKey(promptNode))
	chain.AppendChatModel(chatModel, compose.WithNodeKey(modelNode))
	chain.AppendLambda(compose.InvokableLambda(parseModelMessage), compose.WithNodeKey(parseNode))
	return chain.Compile(ctx, compose.WithGraphName("OpenTalonEinoReviewV1"))
}

func buildMessages(_ context.Context, input reviewInput) ([]*schema.Message, error) {
	return []*schema.Message{
		schema.SystemMessage(systemPrompt),
		schema.UserMessage(buildUserPrompt(input)),
	}, nil
}

func parseModelMessage(_ context.Context, message *schema.Message) ([]review.Finding, error) {
	if message == nil {
		return nil, errEmptyModelResponse
	}
	return decodeAndValidateFindings(message.Content)
}
