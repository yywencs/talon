package githubapi

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ResolveToken 按 GH_TOKEN、GITHUB_TOKEN、GitHub CLI 的顺序寻找认证凭据。
// 返回值只应放入 Authorization Header，禁止写入日志、缓存或数据集。
func ResolveToken(ctx context.Context) (string, error) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token, nil
		}
	}
	command := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("github authentication is unavailable; run `gh auth login --hostname github.com --web`: %w", err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", fmt.Errorf("github authentication returned an empty token")
	}
	return token, nil
}
