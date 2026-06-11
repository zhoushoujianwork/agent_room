package cliprovider

import "testing"

func TestEffectiveConfigPrefersServerOverride(t *testing.T) {
	p := NewClaudeProvider("claude", "", "local-model", "https://local/v1", "local-key", 0, 1, false, false, false)

	// Before any push: local defaults apply.
	if m, b, k := p.effectiveConfig(); m != "local-model" || b != "https://local/v1" || k != "local-key" {
		t.Fatalf("defaults = %q,%q,%q", m, b, k)
	}

	// Server override wins where non-empty; empty fields fall back to local.
	p.ApplyServerConfig("srv-model", "", "")
	if m, b, k := p.effectiveConfig(); m != "srv-model" || b != "https://local/v1" || k != "local-key" {
		t.Fatalf("after partial override = %q,%q,%q", m, b, k)
	}

	// Full override.
	p.ApplyServerConfig("m2", "https://srv/v1", "srv-key")
	if m, b, k := p.effectiveConfig(); m != "m2" || b != "https://srv/v1" || k != "srv-key" {
		t.Fatalf("after full override = %q,%q,%q", m, b, k)
	}

	// Clearing the override (all empty) reverts to local defaults.
	p.ApplyServerConfig("", "", "")
	if m, b, k := p.effectiveConfig(); m != "local-model" || b != "https://local/v1" || k != "local-key" {
		t.Fatalf("after clear = %q,%q,%q", m, b, k)
	}
}
