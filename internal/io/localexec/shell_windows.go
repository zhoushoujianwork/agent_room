//go:build windows

package localexec

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// configureShell 对 cmd.exe 启用原样命令行直传。Go 的默认参数转义
// (CommandLineToArgvW 规则)和 cmd.exe 的实际解析规则不兼容:内嵌引号被
// 转义成 \" 后,cmd 只剥首尾引号、把 \" 当字面量,带引号的命令必挂。
// 用 SysProcAttr.CmdLine 直接给出 `"<shell>" /S /C "<command>"`,
// 子进程看到的命令行与用户手敲一致。PowerShell 走默认转义(其 CLI 解析
// 兼容 \" 转义),无需特殊处理。
func configureShell(cmd *exec.Cmd, shell, command string) {
	base := strings.ToLower(filepath.Base(shell))
	if base != "cmd" && base != "cmd.exe" {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: rawCmdLine(cmd.Path, command)}
}
