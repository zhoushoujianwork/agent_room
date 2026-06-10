package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// errBridgeAlreadyRunning means another live bridge process on this
// machine already holds the lock for this agent id + room. Starting a
// second one would make both answer the same message — burning the turn
// budget and showing up as "N sessions" in the sidebar. Callers exit.
var errBridgeAlreadyRunning = errors.New("another bridge with this agent id is already connected to this room")

// bridgeLockPath returns the per-(agent id, room) lock file path under the
// state dir. The identity is hashed so arbitrary ids/rooms can't escape the
// directory or collide with the cached id/token files.
func bridgeLockPath(agentID, roomID string) (string, error) {
	stateDir, err := bridgeStateDir()
	if err != nil {
		return "", err
	}
	locksDir := filepath.Join(stateDir, "locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		return "", fmt.Errorf("create lock dir: %w", err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(agentID) + "\x00" + strings.TrimSpace(roomID)))
	return filepath.Join(locksDir, hex.EncodeToString(sum[:8])+".lock"), nil
}
