package main

import (
	"errors"
	"testing"
)

func TestAcquireBridgeLock(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_ROOM_STATE_DIR", dir)

	unlock, err := acquireBridgeLock("agent-x", "room-1")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	// A second acquire for the same identity must be refused while the first
	// is still held.
	if _, err := acquireBridgeLock("agent-x", "room-1"); !errors.Is(err, errBridgeAlreadyRunning) {
		t.Fatalf("second lock: want errBridgeAlreadyRunning, got %v", err)
	}

	// A different agent id (or room) is a distinct lock and must succeed.
	otherID, err := acquireBridgeLock("agent-y", "room-1")
	if err != nil {
		t.Fatalf("different agent id should lock: %v", err)
	}
	otherID()

	otherRoom, err := acquireBridgeLock("agent-x", "room-2")
	if err != nil {
		t.Fatalf("different room should lock: %v", err)
	}
	otherRoom()

	// After releasing, the identity can be locked again.
	unlock()
	again, err := acquireBridgeLock("agent-x", "room-1")
	if err != nil {
		t.Fatalf("re-acquire after unlock: %v", err)
	}
	again()
}
