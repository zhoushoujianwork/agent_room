//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// acquireBridgeLock takes an exclusive, non-blocking flock on a per-identity
// lock file. The lock is held for the process lifetime; the OS releases it
// automatically when the process exits (even on crash), so a previously
// orphaned bridge never blocks a fresh start. Returns errBridgeAlreadyRunning
// if a live process already holds it. The returned cleanup closes the file.
func acquireBridgeLock(agentID, roomID string) (func(), error) {
	path, err := bridgeLockPath(agentID, roomID)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open bridge lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, errBridgeAlreadyRunning
		}
		return nil, fmt.Errorf("lock bridge: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
