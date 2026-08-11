package observability

import (
	"os"
	"strconv"
	"strings"
)

const (
	envEnabled             = "COZELOOP_ENABLED"
	envAPIBaseURL          = "COZELOOP_API_BASE_URL"
	envWorkspaceID         = "COZELOOP_WORKSPACE_ID"
	envAPIToken            = "COZELOOP_API_TOKEN"
	envJWTClientID         = "COZELOOP_JWT_OAUTH_CLIENT_ID"
	envJWTPrivateKey       = "COZELOOP_JWT_OAUTH_PRIVATE_KEY"
	envJWTPublicKeyID      = "COZELOOP_JWT_OAUTH_PUBLIC_KEY_ID"
	envServiceName         = "COZELOOP_SERVICE_NAME"
	envDeploymentEnv       = "COZELOOP_DEPLOYMENT_ENV"
	envInputOutputMaxBytes = "COZELOOP_INPUT_OUTPUT_MAX_BYTES"
	envNormalMaxBytes      = "COZELOOP_NORMAL_MAX_BYTES"
	envAggregateOutput     = "COZELOOP_AGGREGATE_OUTPUT"
)

// Config 定义 CozeLoop Agent 观测所需配置。
type Config struct {
	Enabled             bool
	APIBaseURL          string
	WorkspaceID         string
	APIToken            string
	JWTClientID         string
	JWTPrivateKey       string
	JWTPublicKeyID      string
	ServiceName         string
	DeploymentEnv       string
	InputOutputMaxBytes int
	NormalMaxBytes      int
	AggregateOutput     bool
	RedactionRules      []RedactionRule
}

// DefaultConfig 返回默认关闭的 CozeLoop 配置，避免测试意外向外部平台发送数据。
func DefaultConfig() Config {
	return Config{
		Enabled:             false,
		ServiceName:         "talon-toolops",
		DeploymentEnv:       "dev",
		InputOutputMaxBytes: 4096,
		NormalMaxBytes:      2048,
		AggregateOutput:     true,
		RedactionRules:      DefaultRedactionRules(),
	}
}

// LoadConfigFromEnv 从 CozeLoop 官方环境变量和 Talon 扩展配置加载观测配置。
func LoadConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.Enabled = getEnvBool(envEnabled, cfg.Enabled)
	cfg.APIBaseURL = getEnv(envAPIBaseURL, cfg.APIBaseURL)
	cfg.WorkspaceID = getEnv(envWorkspaceID, cfg.WorkspaceID)
	cfg.APIToken = getEnv(envAPIToken, cfg.APIToken)
	cfg.JWTClientID = getEnv(envJWTClientID, cfg.JWTClientID)
	cfg.JWTPrivateKey = strings.TrimSpace(os.Getenv(envJWTPrivateKey))
	cfg.JWTPublicKeyID = getEnv(envJWTPublicKeyID, cfg.JWTPublicKeyID)
	cfg.ServiceName = getEnv(envServiceName, cfg.ServiceName)
	cfg.DeploymentEnv = getEnv(envDeploymentEnv, cfg.DeploymentEnv)
	cfg.InputOutputMaxBytes = getEnvInt(envInputOutputMaxBytes, cfg.InputOutputMaxBytes)
	cfg.NormalMaxBytes = getEnvInt(envNormalMaxBytes, cfg.NormalMaxBytes)
	cfg.AggregateOutput = getEnvBool(envAggregateOutput, cfg.AggregateOutput)
	return cfg.Normalize()
}

// Normalize 归一化配置并补齐安全默认值。
func (c Config) Normalize() Config {
	c.APIBaseURL = strings.TrimRight(strings.TrimSpace(c.APIBaseURL), "/")
	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	c.APIToken = strings.TrimSpace(c.APIToken)
	c.JWTClientID = strings.TrimSpace(c.JWTClientID)
	c.JWTPrivateKey = strings.TrimSpace(c.JWTPrivateKey)
	c.JWTPublicKeyID = strings.TrimSpace(c.JWTPublicKeyID)
	c.ServiceName = strings.TrimSpace(c.ServiceName)
	if c.ServiceName == "" {
		c.ServiceName = "talon-toolops"
	}
	c.DeploymentEnv = strings.TrimSpace(c.DeploymentEnv)
	if c.DeploymentEnv == "" {
		c.DeploymentEnv = "dev"
	}
	if c.InputOutputMaxBytes <= 0 {
		c.InputOutputMaxBytes = 4096
	}
	if c.NormalMaxBytes <= 0 {
		c.NormalMaxBytes = 2048
	}
	if c.RedactionRules == nil {
		c.RedactionRules = DefaultRedactionRules()
	}
	return c
}

func (c Config) hasAuthentication() bool {
	if c.APIToken != "" {
		return true
	}
	return c.JWTClientID != "" && c.JWTPrivateKey != "" && c.JWTPublicKeyID != ""
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getEnvBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
