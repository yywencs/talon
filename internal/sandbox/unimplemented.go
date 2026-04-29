package sandbox

import (
	"context"
	"time"
)

// UnimplementedSandbox 表示当前阶段的 sandbox 占位实现。
type UnimplementedSandbox struct {
	config Config
	status Status
}

// NewUnimplementedSandbox 创建 sandbox 占位实现。
func NewUnimplementedSandbox(config Config) *UnimplementedSandbox {
	return &UnimplementedSandbox{
		config: normalizeConfig(config),
		status: StatusCreated,
	}
}

// Start 启动 sandbox 占位实现。
func (s *UnimplementedSandbox) Start(ctx context.Context) error {
	_ = ctx
	return ErrRuntimeNotImplemented
}

// Close 关闭 sandbox 占位实现。
func (s *UnimplementedSandbox) Close(ctx context.Context) error {
	_ = ctx
	s.status = StatusClosed
	return nil
}

// Exec 在占位实现中始终返回未实现错误。
func (s *UnimplementedSandbox) Exec(ctx context.Context, command string, args ...string) (string, error) {
	_ = ctx
	_ = command
	_ = args
	return "", ErrRuntimeNotImplemented
}

// CleanupIfIdle 对占位实现保持稳定空操作。
func (s *UnimplementedSandbox) CleanupIfIdle(ctx context.Context, idleThreshold time.Duration) (bool, error) {
	_ = ctx
	_ = idleThreshold
	return false, nil
}

// Info 返回 sandbox 占位实现的状态快照。
func (s *UnimplementedSandbox) Info() Info {
	return Info{
		Status:           s.status,
		HostWorkingDir:   s.config.WorkingDir,
		ContainerWorkDir: s.config.ContainerWorkDir,
		Image:            s.config.Image,
		ContainerName:    s.config.ContainerName,
		OutputLimitBytes: s.config.OutputLimitBytes,
		ReadOnlyMounts:   append([]Mount(nil), s.config.ReadOnlyMounts...),
	}
}
