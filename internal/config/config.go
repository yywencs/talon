package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

type Config struct {
	Debug      bool
	OneLogFile bool
	LogDir     string
	LLM        LLMConfig
}

type LLMConfig struct {
	Provider            string
	APIKey              string
	Endpoint            string
	Model               string
	PromptsDir          string
	ContextWindowTokens int
}

// GlobalConfig 作为一个全局单例，方便在 Engine 或 Worker 中引用
var Global *Config

func Load() {
	configRoot, err := FindRoot()
	if err != nil {
		panic(err)
	}

	envPath := filepath.Join(configRoot, ".env")
	if err := godotenv.Load(envPath); err != nil {
		fmt.Printf("Warning: failed to load .env file %s: %v", envPath, err)
	}

	logDir := getEnv("LOG_DIR", filepath.Join(configRoot, "logs"))

	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Printf("Warning: failed to create log directory %s: %v", logDir, err)
	}

	Global = &Config{
		Debug:      getEnvAsBool("DEBUG", false),
		OneLogFile: getEnvAsBool("ONLY_ONE_LOG_FILE", false),
		LogDir:     logDir,
		LLM:        llmConfigFromEnv(),
	}
}

// LoadLLMConfig 为独立命令加载与主 Agent 相同的 .env 和 LLM 配置，
// 但不创建日志目录，也不修改 Global，避免 rules-only Review 产生额外副作用。
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

func IsDebug() bool {
	return Global.Debug
}
