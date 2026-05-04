package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wen/opentalon/pkg/logger"
)

const (
	defaultSandboxHome = "/home/opentalon"
	defaultSandboxTmp  = "/tmp"
)

type dockerCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (dockerCommandResult, error)
	LookPath(ctx context.Context, file string) (string, error)
}

type dockerCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type execDockerCommandRunner struct{}

func (r *execDockerCommandRunner) Run(ctx context.Context, name string, args ...string) (dockerCommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdoutBuilder strings.Builder
	var stderrBuilder strings.Builder
	cmd.Stdout = &stdoutBuilder
	cmd.Stderr = &stderrBuilder
	err := cmd.Run()
	result := dockerCommandResult{
		Stdout: stdoutBuilder.String(),
		Stderr: stderrBuilder.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, err
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
	lastActiveAt  time.Time
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
	if err := validateDockerConfig(s.config); err != nil {
		return err
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
		s.resetToCreated()
		_ = s.removeContainer(ctx, name)
		return fmt.Errorf("failed to start docker sandbox %q: %w", name, err)
	}
	logger.Info("docker 成功启动 sandbox %q", name)
	s.containerName = name
	s.status = StatusRunning
	s.touchActivity()
	return nil
}

// Close 关闭最小 Docker sandbox。
func (s *DockerSandbox) Close(ctx context.Context) error {
	if s.status == StatusClosed {
		return nil
	}
	if s.containerName != "" {
		if err := s.removeContainer(ctx, s.containerName); err != nil {
			return fmt.Errorf("failed to remove docker sandbox %q: %w", s.containerName, err)
		}
	}
	s.status = StatusClosed
	s.containerName = ""
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

	execCtx, cancel := s.execContext(ctx)
	defer cancel()

	audit := execAudit{
		Runtime:          "docker",
		ContainerID:      s.containerName,
		Image:            s.config.Image,
		WorkspaceRoot:    s.config.WorkingDir,
		CWD:              s.config.ContainerWorkDir,
		Command:          strings.Join(append([]string{command}, args...), " "),
		MemoryLimitBytes: DefaultMemoryLimitBytes,
		PIDLimit:         DefaultProcessLimit,
	}
	spanCtx, span := startSandboxExecSpan(execCtx, s.containerName, audit)
	defer span.End()

	execArgs := []string{"exec", "-w", s.config.ContainerWorkDir, s.containerName, command}
	execArgs = append(execArgs, args...)
	result, err := s.runner.Run(spanCtx, "docker", execArgs...)
	combined := result.Stdout + result.Stderr
	output, truncated := truncateOutput(combined, s.config.OutputLimitBytes)
	audit.StdoutBytes = len(result.Stdout)
	audit.StderrBytes = len(result.Stderr)
	audit.OutputTruncated = truncated
	audit.ExitCode = result.ExitCode

	classifiedErr, recoveryAttempted, recoveryResult := s.classifyExecError(execCtx, result, err)
	audit.TimedOut = classifiedErr == ErrSandboxTimedOut
	audit.Cancelled = classifiedErr == ErrSandboxCancelled
	audit.RecoveryAttempted = recoveryAttempted
	audit.RecoveryResult = recoveryResult
	audit.ErrorReason = classifyExecError(classifiedErr)
	finishSandboxExecSpan(span, audit, classifiedErr)
	if classifiedErr != nil {
		return output, classifiedErr
	}
	s.touchActivity()
	return output, nil
}

// CleanupIfIdle 在 sandbox 闲置超过阈值时关闭容器并返回是否已清理。
func (s *DockerSandbox) CleanupIfIdle(ctx context.Context, idleThreshold time.Duration) (bool, error) {
	if idleThreshold <= 0 || s.status != StatusRunning {
		return false, nil
	}
	lastActiveAt := s.lastActiveAt
	if lastActiveAt.IsZero() {
		lastActiveAt = time.Now()
		s.lastActiveAt = lastActiveAt
	}
	idleDuration := time.Since(lastActiveAt)
	if idleDuration < idleThreshold {
		return false, nil
	}

	containerID := s.containerName
	err := s.Close(ctx)
	audit := cleanupAudit{
		Runtime:          "docker",
		ContainerID:      containerID,
		Image:            s.config.Image,
		MemoryLimitBytes: DefaultMemoryLimitBytes,
		PIDLimit:         DefaultProcessLimit,
		CleanupReason:    "idle",
		IdleDuration:     idleDuration,
		CleanupResult:    "closed",
	}
	if err != nil {
		audit.CleanupResult = "close_failed"
	}
	recordSandboxCleanup(ctx, containerID, audit, err)
	if err != nil {
		return false, err
	}
	return true, nil
}

// Info 返回 Docker sandbox 的状态快照。
func (s *DockerSandbox) Info() Info {
	return Info{
		Status:           s.status,
		HostWorkingDir:   s.config.WorkingDir,
		ContainerWorkDir: s.config.ContainerWorkDir,
		Image:            s.config.Image,
		ContainerName:    s.containerName,
		OutputLimitBytes: s.config.OutputLimitBytes,
		ReadOnlyMounts:   append([]Mount(nil), s.config.ReadOnlyMounts...),
	}
}

func (s *DockerSandbox) dockerRunArgs(name string) []string {
	workspaceStateDir := filepath.Join(s.config.ContainerWorkDir, ".opentalon")
	goTmpDir := filepath.Join(workspaceStateDir, "tmp")
	xdgCacheDir := filepath.Join(workspaceStateDir, "cache")
	goCacheDir := filepath.Join(workspaceStateDir, "cache", "go-build")
	bootstrapCommand := fmt.Sprintf(
		"mkdir -p %s %s && while true; do sleep 3600; done",
		goTmpDir,
		goCacheDir,
	)

	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--read-only",
		"--memory", fmt.Sprintf("%d", DefaultMemoryLimitBytes),
		"--pids-limit", fmt.Sprintf("%d", DefaultProcessLimit),
		"-w", s.config.ContainerWorkDir,
		"--tmpfs", defaultSandboxTmp,
		"--tmpfs", defaultSandboxHome,
		"-e", "HOME=" + defaultSandboxHome,
		"-e", "TMPDIR=" + defaultSandboxTmp,
		"-e", "GOTMPDIR=" + goTmpDir,
		"-e", "XDG_CACHE_HOME=" + xdgCacheDir,
		"-e", "GOCACHE=" + goCacheDir,
		"-e", "GOPATH=" + filepath.Join(defaultSandboxHome, "go"),
		"-e", "GOMODCACHE=" + filepath.Join(defaultSandboxHome, "go", "pkg", "mod"),
	}
	if s.config.WorkingDir != "" {
		args = append(args, "-v", s.config.WorkingDir+":"+s.config.ContainerWorkDir)
	}
	for _, mount := range s.config.ReadOnlyMounts {
		if mount.HostPath == "" || mount.ContainerPath == "" {
			continue
		}
		args = append(args, "-v", mount.HostPath+":"+mount.ContainerPath+":ro")
	}
	args = append(args, s.config.Image, "sh", "-c", bootstrapCommand)
	return args
}

// DockerFactory 表示创建 Docker sandbox 的工厂。
type DockerFactory struct{}

// Create 创建 Docker sandbox 实例。
func (DockerFactory) Create(config Config) Sandbox {
	return NewDockerSandbox(config)
}

func (s *DockerSandbox) execContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.config.ExecTimeout > 0 {
		if _, ok := ctx.Deadline(); !ok {
			return context.WithTimeout(ctx, s.config.ExecTimeout)
		}
	}
	return ctx, func() {}
}

func (s *DockerSandbox) classifyExecError(ctx context.Context, result dockerCommandResult, err error) (error, bool, string) {
	if err == nil {
		return nil, false, ""
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return ErrSandboxTimedOut, false, ""
	case ctx.Err() == context.Canceled:
		return ErrSandboxCancelled, false, ""
	case isResourceLimitError(result, err):
		return ErrSandboxResourceLimitExceeded, false, ""
	case isContainerUnavailableError(result, err):
		s.resetToCreated()
		return ErrSandboxNotRunning, true, "reset_to_created"
	default:
		return fmt.Errorf("failed to exec command in docker sandbox %q: %w", s.containerName, err), false, ""
	}
}

func (s *DockerSandbox) removeContainer(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	_, err := s.runner.Run(ctx, "docker", "rm", "-f", name)
	if isContainerUnavailableError(dockerCommandResult{}, err) {
		return nil
	}
	return err
}

func (s *DockerSandbox) resetToCreated() {
	s.status = StatusCreated
	s.containerName = ""
}

func (s *DockerSandbox) touchActivity() {
	s.lastActiveAt = time.Now()
}

func isContainerUnavailableError(result dockerCommandResult, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(result.Stdout + "\n" + result.Stderr + "\n" + err.Error()))
	return strings.Contains(text, "no such container") ||
		strings.Contains(text, "is not running") ||
		strings.Contains(text, "cannot exec in a stopped state") ||
		strings.Contains(text, "container not found")
}

func isResourceLimitError(result dockerCommandResult, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(result.Stdout + "\n" + result.Stderr + "\n" + err.Error()))
	return strings.Contains(text, "cannot allocate memory") ||
		strings.Contains(text, "out of memory") ||
		strings.Contains(text, "resource temporarily unavailable") ||
		strings.Contains(text, "pids limit") ||
		strings.Contains(text, "process limit") ||
		errors.Is(err, ErrSandboxResourceLimitExceeded)
}

func ensureDir(path string) error {
	if path == "" {
		return nil
	}
	return os.MkdirAll(path, 0o755)
}
