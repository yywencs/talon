package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

type LLMConfig struct {
	Provider            string
	APIKey              string
	Endpoint            string
	Model               string
	PromptsDir          string
	ContextWindowTokens int
}

// LoadLLMConfig 为独立命令加载与主 Agent 相同的 .env 和 LLM 配置。
func LoadLLMConfig() (LLMConfig, error) {
	configRoot, err := FindRoot()
	if err != nil {
		return LLMConfig{}, err
	}
	envPath := filepath.Join(configRoot, ".env")
	if err := godotenv.Load(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LLMConfig{}, fmt.Errorf("load LLM env %s: %w", envPath, err)
	}
	return llmConfigFromEnv(), nil
}

func llmConfigFromEnv() LLMConfig {
	return LLMConfig{
		Provider:            getEnv("LLM_PROVIDER", "ollama"),
		Model:               getEnv("LLM_MODEL", "qwen3:32b"),
		Endpoint:            getEnv("LLM_ENDPOINT", "http://222.195.7.108:11434"),
		APIKey:              getEnv("LLM_API_KEY", ""),
		PromptsDir:          getEnv("LLM_PROMPTS_DIR", ""),
		ContextWindowTokens: Int("LLM_CONTEXT_WINDOW_TOKENS", 0),
	}
}
