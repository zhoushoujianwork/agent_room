//go:build windows

package console

import "golang.org/x/sys/windows"

// DisableQuickEdit 关闭当前控制台的 QuickEdit 模式。
//
// conhost 默认开启 QuickEdit：用户在窗口里单击一下就进入"选择"状态，
// 此后所有写 console 的线程全部阻塞，直到按键解除。bridge/executor 的
// 处理循环里有同步日志写，一次误点就会把整个进程冻住（线上表现为
// executor 收到命令后数分钟无响应、解除后瞬间执行完）。服务型进程
// 不需要 QuickEdit，启动时直接关掉。
//
// Best-effort：没有控制台（如重定向/服务方式运行）时静默跳过。
func DisableQuickEdit() {
	handle, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil || handle == windows.InvalidHandle {
		return
	}
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return
	}
	mode &^= windows.ENABLE_QUICK_EDIT_MODE
	mode |= windows.ENABLE_EXTENDED_FLAGS
	_ = windows.SetConsoleMode(handle, mode)
}
