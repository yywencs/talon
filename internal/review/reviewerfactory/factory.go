// Package reviewerfactory constructs review implementations shared by the
// single-review and dataset-evaluation commands.
package reviewerfactory

import (
	"context"
	"fmt"

	"github.com/wen/opentalon/internal/review"
	"github.com/wen/opentalon/internal/review/agentreview"
	"github.com/wen/opentalon/internal/tool/repository"
	"github.com/wen/opentalon/pkg/config"
)

// Options describes one reviewer instance. RepositoryRoot may be omitted to
// use the diff-only agent path. LLMConfig is loaded from the normal Talon
// environment when it is nil.
type Options struct {
	Mode           string
	RepositoryRoot string
	BaseSHA        string
	HeadSHA        string
	LLMConfig      *config.LLMConfig
}

// Build creates a deterministic rules reviewer or an Eino agent reviewer.
func Build(ctx context.Context, options Options) (review.Reviewer, error) {
	switch options.Mode {
	case "rules":
		return review.NewRuleReviewer(), nil
	case "agent":
		llmConfig, err := resolveLLMConfig(options.LLMConfig)
		if err != nil {
			return nil, err
		}
		if options.RepositoryRoot == "" {
			reviewerImpl, err := agentreview.NewFromConfig(ctx, llmConfig)
			if err != nil {
				return nil, fmt.Errorf("initialize agent reviewer: %w", err)
			}
			return reviewerImpl, nil
		}
		reader, err := repository.NewGitReader(ctx, repository.GitConfig{
			RepositoryRoot: options.RepositoryRoot,
			BaseSHA:        options.BaseSHA,
			HeadSHA:        options.HeadSHA,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize read-only repository tools: %w", err)
		}
		reviewerImpl, err := agentreview.NewFromConfigWithRepository(ctx, llmConfig, reader)
		if err != nil {
			return nil, fmt.Errorf("initialize repository agent reviewer: %w", err)
		}
		return reviewerImpl, nil
	default:
		return nil, fmt.Errorf("unsupported reviewer %q; available: rules, agent", options.Mode)
	}
}

func resolveLLMConfig(provided *config.LLMConfig) (config.LLMConfig, error) {
	if provided != nil {
		return *provided, nil
	}
	loaded, err := config.LoadLLMConfig()
	if err != nil {
		return config.LLMConfig{}, fmt.Errorf("load agent reviewer config: %w", err)
	}
	return loaded, nil
}
