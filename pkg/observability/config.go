package observability

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	envEnabled        = "OBS_ENABLED"
	envExporters      = "OBS_EXPORTERS"
	envSampleRate     = "OBS_SAMPLE_RATE"
	envServiceName    = "OBS_SERVICE_NAME"
	envServiceVersion = "OBS_SERVICE_VERSION"
	envEnvironment    = "OBS_ENVIRONMENT"
	envTraceDir       = "OBS_TRACE_DIR"
	envOTLPEndpoint   = "OBS_OTLP_ENDPOINT"
	envOTLPInsecure   = "OBS_OTLP_INSECURE"
	envStdoutPretty   = "OBS_STDOUT_PRETTY"
	envPayloadSize    = "OBS_PAYLOAD_SIZE_LIMIT"
	envPayloadPreview = "OBS_PAYLOAD_PREVIEW_LIMIT"
)

// Config 定义 observability 初始化所需配置。
type Config struct {
	Enabled        bool
	Exporters      []ExporterKind
	SampleRate     float64
	ServiceName    string
	ServiceVersion string
	Environment    string
	TraceDir       string
	OTLPEndpoint   string
	OTLPInsecure   bool
	StdoutPretty   bool
	PayloadSizeLimit    int
	PayloadPreviewLimit int
	RedactionRules []RedactionRule
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		Exporters:      []ExporterKind{ExporterStdout},
		SampleRate:     1.0,
		ServiceName:    "opentalon-cli",
		ServiceVersion: "",
		Environment:    "dev",
		TraceDir:       defaultTraceDir(),
		OTLPInsecure:   false,
		StdoutPretty:   true,
		PayloadSizeLimit:    4096,
		PayloadPreviewLimit: 512,
		RedactionRules: DefaultRedactionRules(),
	}
}

// LoadConfigFromEnv 从环境变量加载配置。
func LoadConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.Enabled = getEnvBool(envEnabled, cfg.Enabled)
	cfg.Exporters = parseExporterKinds(getEnv(envExporters, joinExporterKinds(cfg.Exporters)))
	cfg.SampleRate = getEnvFloat64(envSampleRate, cfg.SampleRate)
	cfg.ServiceName = getEnv(envServiceName, cfg.ServiceName)
	cfg.ServiceVersion = getEnv(envServiceVersion, cfg.ServiceVersion)
	cfg.Environment = getEnv(envEnvironment, cfg.Environment)
	cfg.TraceDir = getEnv(envTraceDir, cfg.TraceDir)
	cfg.OTLPEndpoint = getEnv(envOTLPEndpoint, cfg.OTLPEndpoint)
	cfg.OTLPInsecure = getEnvBool(envOTLPInsecure, cfg.OTLPInsecure)
	cfg.StdoutPretty = getEnvBool(envStdoutPretty, cfg.StdoutPretty)
	cfg.PayloadSizeLimit = getEnvInt(envPayloadSize, cfg.PayloadSizeLimit)
	cfg.PayloadPreviewLimit = getEnvInt(envPayloadPreview, cfg.PayloadPreviewLimit)
	return cfg.Normalize()
}

// Normalize 归一化配置并补齐默认值。
func (c Config) Normalize() Config {
	if c.SampleRate < 0 {
		c.SampleRate = 0
	}
	if c.SampleRate > 1 {
		c.SampleRate = 1
	}
	if len(c.Exporters) == 0 {
		c.Exporters = []ExporterKind{ExporterStdout}
	}
	if strings.TrimSpace(c.ServiceName) == "" {
		c.ServiceName = "opentalon-cli"
	}
	if strings.TrimSpace(c.Environment) == "" {
		c.Environment = "dev"
	}
	if strings.TrimSpace(c.TraceDir) == "" {
		c.TraceDir = defaultTraceDir()
	}
	if c.PayloadSizeLimit <= 0 {
		c.PayloadSizeLimit = 4096
	}
	if c.PayloadPreviewLimit <= 0 {
		c.PayloadPreviewLimit = 512
	}
	if c.RedactionRules == nil {
		c.RedactionRules = DefaultRedactionRules()
	}
	return c
}

func defaultTraceDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.Join(".", ".opentalon", "traces")
	}
	return filepath.Join(cwd, ".opentalon", "traces")
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

func getEnvFloat64(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
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
