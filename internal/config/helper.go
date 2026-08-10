package config

import (
	"os"
	"strconv"
	"strings"
)

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

func Int(env string, defaultValue int) int {
	if env == "" || getEnv(env, "") == "" {
		return defaultValue
	}
	num, err := strconv.Atoi(getEnv(env, ""))
	if err != nil {
		return defaultValue
	}
	return num
}

func Float64(env string, defaultValue float64) float64 {
	if env == "" || getEnv(env, "") == "" {
		return defaultValue
	}
	num, err := strconv.ParseFloat(getEnv(env, ""), 64)
	if err != nil {
		return defaultValue
	}
	return num
}

func String(env string, defaultValue string) string {
	if env == "" || getEnv(env, "") == "" {
		return defaultValue
	}
	return os.Getenv(env)
}
