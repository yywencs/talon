package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Debug      bool
	OneLogFile bool
	LLM        LLMConfig
}

type LLMConfig struct {
	Provider string
	APIKey   string
	Endpoint string
	Model    string
}

// GlobalConfig 作为一个全局单例，方便在 Engine 或 Worker 中引用
var Global *Config

func Load() {
	err := godotenv.Load(".env")
	if err != nil {
		fmt.Printf("Warning: .env file not found: %v", err)
	}
	Global = &Config{
		Debug:      getEnvAsBool("DEBUG", false),
		OneLogFile: getEnvAsBool("ONLY_ONE_LOG_FILE", false),
		LLM: LLMConfig{
			Provider: getEnv("LLM_PROVIDER", "ollama"),
			Model:    getEnv("LLM_MODEL", "qwen3:32b"),
			Endpoint: getEnv("LLM_ENDPOINT", "http://222.195.7.108:11434"),
			APIKey:   getEnv("LLM_API_KEY", ""),
		},
	}
}

// 辅助函数：简化获取环境变量的逻辑
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvAsBool(key string, fallback bool) bool {
	val := getEnv(key, "")
	if val == "" {
		return fallback
	}
	return strings.ToLower(val) == "true"
}
