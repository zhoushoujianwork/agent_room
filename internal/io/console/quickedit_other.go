//go:build !windows

package console

// DisableQuickEdit 仅在 Windows 上有意义；其他平台为 no-op。
func DisableQuickEdit() {}
