package terminal

import (
	"fmt"
	"os"
	"strings"
)

func validateAction(action *BashTool) error {
	if !action.IsInput && !action.Reset && strings.TrimSpace(action.Command) == "" {
		return fmt.Errorf("command is empty")
	}
	if action.Timeout != nil && (*action.Timeout <= 0 || *action.Timeout > float64(maxTimeoutSecs)) {
		return fmt.Errorf("timeout out of range")
	}
	return nil
}

func validateWorkingDir(workingDir string) error {
	if workingDir == "" {
		return nil
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("working_dir does not exist: %s", workingDir)
		}
		return fmt.Errorf("working_dir is not accessible: %s", workingDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("working_dir is not a directory: %s", workingDir)
	}
	return nil
}
