package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	"github.com/wen/opentalon/pkg/utils"
)

type Config struct {
	Debug      bool
	OneLogFile bool
	LogDir     string
	LLM        LLMConfig
}

type LLMConfig struct {
	Provider   string
	APIKey     string
	Endpoint   string
	Model      string
	PromptsDir string
}

// GlobalConfig 作为一个全局单例，方便在 Engine 或 Worker 中引用
var Global *Config

func Load() {
	workspaceRoot, err := utils.FindWorkspaceRoot()
	if err != nil {
		panic(err)
	}

	envPath := filepath.Join(workspaceRoot, ".env")
	if err := godotenv.Load(envPath); err != nil {
		fmt.Printf("Warning: failed to load .env file %s: %v", envPath, err)
	}

	logDir := getEnv("LOG_DIR", filepath.Join(workspaceRoot, "./logs"))

	if err := os.MkdirAll(logDir, 0755); err != nil {
		fmt.Printf("Warning: failed to create log directory %s: %v", logDir, err)
	}

	Global = &Config{
		Debug:      getEnvAsBool("DEBUG", false),
		OneLogFile: getEnvAsBool("ONLY_ONE_LOG_FILE", false),
		LogDir:     logDir,
		LLM: LLMConfig{
			Provider:   getEnv("LLM_PROVIDER", "ollama"),
			Model:      getEnv("LLM_MODEL", "qwen3:32b"),
			Endpoint:   getEnv("LLM_ENDPOINT", "http://222.195.7.108:11434"),
			APIKey:     getEnv("LLM_API_KEY", ""),
			PromptsDir: getEnv("LLM_PROMPTS_DIR", ""),
		},
	}
}

func IsDebug() bool {
	return Global.Debug
}
