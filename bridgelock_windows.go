//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// acquireBridgeLock takes an exclusive, non-blocking lock on a per-identity
// lock file via LockFileEx. Windows releases the lock when the handle is
// closed, which happens automatically on process exit, so an orphaned bridge
// never blocks a fresh start. Returns errBridgeAlreadyRunning when a live
// process already holds it. The returned cleanup unlocks and closes the file.
func acquireBridgeLock(agentID, roomID string) (func(), error) {
	path, err := bridgeLockPath(agentID, roomID)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open bridge lock: %w", err)
	}
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	const flags = windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY
	if err := windows.LockFileEx(handle, flags, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, errBridgeAlreadyRunning
		}
		return nil, fmt.Errorf("lock bridge: %w", err)
	}
	return func() {
		var ov windows.Overlapped
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &ov)
		_ = f.Close()
	}, nil
}
