//go:build !windows

package localexec

import "os/exec"

// configureShell 仅在 Windows 上需要(cmd.exe 原样命令行直传);其他平台 no-op。
func configureShell(_ *exec.Cmd, _, _ string) {}
