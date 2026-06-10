// Package version 提供构建版本号:发布时经 ldflags 烤入,本地构建回退到
// Go 嵌入的 VCS 信息。版本号会在进程启动日志打印,并随 bridge 的 presence
// metadata 上报到房间(前端 agent 卡片可见),用于线上排查"这台机器到底跑
// 的哪个版本"——此前 executor 假死事故中无法确认对端版本,排查代价很高。
package version

import "runtime/debug"

// Version is stamped at release build time via:
//
//	go build -ldflags "-X agent-room/internal/version.Version=<version>"
//
// Empty for plain `go build`; String() then falls back to VCS build info.
var Version = ""

// String returns the best available version identifier:
// stamped Version > short VCS revision (+dirty) > "dev".
func String() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		return rev + "+dirty"
	}
	return rev
}
