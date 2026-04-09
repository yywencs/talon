package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Debug      bool
	OneLogFile bool
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
	err := LoadEnv()
	if err != nil {
		fmt.Printf("Warning: .env file not found: %v", err)
	}
	Global = &Config{
		Debug:      getEnvAsBool("DEBUG", false),
		OneLogFile: getEnvAsBool("ONLY_ONE_LOG_FILE", false),
		LLM: LLMConfig{
			Provider:   getEnv("LLM_PROVIDER", "ollama"),
			Model:      getEnv("LLM_MODEL", "qwen3:32b"),
			Endpoint:   getEnv("LLM_ENDPOINT", "http://222.195.7.108:11434"),
			APIKey:     getEnv("LLM_API_KEY", ""),
			PromptsDir: getEnv("LLM_PROMPTS_DIR", ""),
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

func LoadEnv() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	// 向上遍历，最多找 10 层，防止死循环
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// 找到了 go.mod 所在的目录（即项目根目录）
			envPath := filepath.Join(dir, ".env")
			return godotenv.Load(envPath)
		}
		// 往上一层
		parent := filepath.Dir(dir)
		if parent == dir {
			break // 到达系统根目录
		}
		dir = parent
	}

	return fmt.Errorf("未能找到项目根目录 (go.mod)")
}
