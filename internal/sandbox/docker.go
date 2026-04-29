package sandbox

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/google/uuid"
)

type dockerCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
	LookPath(ctx context.Context, file string) (string, error)
}

type execDockerCommandRunner struct{}

func (r *execDockerCommandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *execDockerCommandRunner) LookPath(ctx context.Context, file string) (string, error) {
	_ = ctx
	return exec.LookPath(file)
}

// DockerSandbox 表示基于 Docker CLI 的最小 sandbox 实现。
type DockerSandbox struct {
	config        Config
	runner        dockerCommandRunner
	status        Status
	containerName string
}

// NewDockerSandbox 创建最小 Docker sandbox 实例。
func NewDockerSandbox(config Config) *DockerSandbox {
	return &DockerSandbox{
		config: normalizeConfig(config),
		runner: &execDockerCommandRunner{},
		status: StatusCreated,
	}
}

// Start 启动最小 Docker sandbox。
func (s *DockerSandbox) Start(ctx context.Context) error {
	if s.status == StatusRunning {
		return nil
	}
	if s.status == StatusClosed {
		return ErrSandboxClosed
	}
	if _, err := s.runner.LookPath(ctx, "docker"); err != nil {
		return fmt.Errorf("docker is not available: %w", err)
	}

	name := s.config.ContainerName
	if name == "" {
		name = "opentalon-sandbox-" + uuid.NewString()
	}
	args := s.dockerRunArgs(name)
	if _, err := s.runner.Run(ctx, "docker", args...); err != nil {
		return fmt.Errorf("failed to start docker sandbox %q: %w", name, err)
	}

	s.containerName = name
	s.status = StatusRunning
	return nil
}

// Close 关闭最小 Docker sandbox。
func (s *DockerSandbox) Close(ctx context.Context) error {
	if s.status == StatusClosed {
		return nil
	}
	if s.containerName != "" {
		if _, err := s.runner.Run(ctx, "docker", "rm", "-f", s.containerName); err != nil {
			return fmt.Errorf("failed to remove docker sandbox %q: %w", s.containerName, err)
		}
	}
	s.status = StatusClosed
	return nil
}

// Exec 在 sandbox 容器内执行单条命令。
func (s *DockerSandbox) Exec(ctx context.Context, command string, args ...string) (string, error) {
	if s.status == StatusClosed {
		return "", ErrSandboxClosed
	}
	if s.status != StatusRunning {
		return "", ErrSandboxNotRunning
	}

	execArgs := []string{"exec", "-w", s.config.ContainerWorkDir, s.containerName, command}
	execArgs = append(execArgs, args...)
	out, err := s.runner.Run(ctx, "docker", execArgs...)
	if err != nil {
		return out, fmt.Errorf("failed to exec command in docker sandbox %q: %w", s.containerName, err)
	}
	return out, nil
}

// Info 返回 Docker sandbox 的状态快照。
func (s *DockerSandbox) Info() Info {
	return Info{
		Status:           s.status,
		HostWorkingDir:   s.config.WorkingDir,
		ContainerWorkDir: s.config.ContainerWorkDir,
		Image:            s.config.Image,
		ContainerName:    s.containerName,
	}
}

func (s *DockerSandbox) dockerRunArgs(name string) []string {
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"-w", s.config.ContainerWorkDir,
	}
	if s.config.WorkingDir != "" {
		args = append(args, "-v", s.config.WorkingDir+":"+s.config.ContainerWorkDir)
	}
	args = append(args, s.config.Image, "sh", "-c", "while true; do sleep 3600; done")
	return args
}

// DockerFactory 表示创建 Docker sandbox 的工厂。
type DockerFactory struct{}

// Create 创建 Docker sandbox 实例。
func (DockerFactory) Create(config Config) Sandbox {
	return NewDockerSandbox(config)
}
