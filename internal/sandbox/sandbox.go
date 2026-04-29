package sandbox

import (
	"context"
	"time"
)

const (
	// DefaultDockerImage 表示阶段 2 默认使用的轻量 Go Docker 镜像。
	DefaultDockerImage = "golang:alpine"
	// DefaultContainerWorkDir 表示 sandbox 在容器内的默认工作目录。
	DefaultContainerWorkDir = "/workspace"
	// DefaultOutputLimitBytes 表示 sandbox 聚合输出的默认大小限制。
	DefaultOutputLimitBytes = 1024 * 1024
	// DefaultMemoryLimitBytes 表示阶段 5 固定施加的内存上限。
	DefaultMemoryLimitBytes int64 = 1024 * 1024 * 1024
	// DefaultProcessLimit 表示阶段 5 固定施加的进程数上限。
	DefaultProcessLimit = 128
)

// Status 表示 sandbox 当前生命周期状态。
type Status string

const (
	// StatusCreated 表示 sandbox 句柄已创建但尚未启动。
	StatusCreated Status = "created"
	// StatusRunning 表示 sandbox 已启动。
	StatusRunning Status = "running"
	// StatusClosed 表示 sandbox 已关闭。
	StatusClosed Status = "closed"
)

// Config 定义 sandbox 的最小初始化参数。
type Config struct {
	// WorkingDir 表示宿主机工作目录；若非空，会挂载到容器内工作目录。
	WorkingDir string
	// Image 表示 sandbox 使用的 Docker 镜像。
	Image string
	// ContainerWorkDir 表示 sandbox 在容器内的工作目录。
	ContainerWorkDir string
	// ContainerName 表示 sandbox 对应容器名；为空时自动生成。
	ContainerName string
	// ReadOnlyMounts 表示额外暴露给 sandbox 的只读挂载。
	ReadOnlyMounts []Mount
	// ExecTimeout 表示 sandbox 命令执行的统一超时兜底；为 0 时仅依赖上层 ctx。
	ExecTimeout time.Duration
	// OutputLimitBytes 表示 sandbox 聚合输出的最大字节数。
	OutputLimitBytes int
}

// Mount 表示宿主机到容器内的挂载映射。
type Mount struct {
	HostPath      string
	ContainerPath string
}

// Info 表示 sandbox 的最小状态快照。
type Info struct {
	Status           Status
	HostWorkingDir   string
	ContainerWorkDir string
	Image            string
	ContainerName    string
	OutputLimitBytes int
	ReadOnlyMounts   []Mount
}

// Sandbox 定义单个 sandbox 实例的最小生命周期与命令执行能力。
type Sandbox interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error
	Exec(ctx context.Context, command string, args ...string) (string, error)
	CleanupIfIdle(ctx context.Context, idleThreshold time.Duration) (bool, error)
	Info() Info
}

// Factory 定义 sandbox 实例的创建入口。
type Factory interface {
	Create(config Config) Sandbox
}

func normalizeConfig(config Config) Config {
	if config.Image == "" {
		config.Image = DefaultDockerImage
	}
	if config.ContainerWorkDir == "" {
		config.ContainerWorkDir = DefaultContainerWorkDir
	}
	if config.OutputLimitBytes <= 0 {
		config.OutputLimitBytes = DefaultOutputLimitBytes
	}
	return config
}
