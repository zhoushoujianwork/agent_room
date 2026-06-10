package main

import (
	"regexp"
	"testing"
)

func TestGenerateTokenUsesURLSafeCharacters(t *testing.T) {
	token, err := generateToken(24)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) == 0 {
		t.Fatal("token is empty")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(token) {
		t.Fatalf("token contains non URL-safe characters: %q", token)
	}
}

func TestGenerateLocalIDSanitizesPrefixAndHostParts(t *testing.T) {
	t.Setenv("COMPUTERNAME", "Win Box")
	t.Setenv("USERNAME", "Alice Smith")

	id, err := generateLocalID("executor!")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^executor-win-box-alice-smith-[a-f0-9]{8}$`).MatchString(id) {
		t.Fatalf("unexpected id: %q", id)
	}
}

func TestNormalizeRelayWSURL(t *testing.T) {
	const room = "bbc1044e01566c563d7e0ff1"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bare origin gets room path appended",
			raw:  "wss://relay.example.com",
			want: "wss://relay.example.com/v1/rooms/" + room + "/ws",
		},
		{
			name: "trailing slash is collapsed",
			raw:  "wss://relay.example.com/",
			want: "wss://relay.example.com/v1/rooms/" + room + "/ws",
		},
		{
			name: "https origin upgraded to wss",
			raw:  "https://relay.example.com",
			want: "wss://relay.example.com/v1/rooms/" + room + "/ws",
		},
		{
			name: "http origin upgraded to ws",
			raw:  "http://127.0.0.1:8080",
			want: "ws://127.0.0.1:8080/v1/rooms/" + room + "/ws",
		},
		{
			name: "full room ws path is left untouched",
			raw:  "wss://relay.example.com/v1/rooms/" + room + "/ws",
			want: "wss://relay.example.com/v1/rooms/" + room + "/ws",
		},
		{
			name: "any rooms path is trusted as-is",
			raw:  "wss://relay.example.com/rooms/other-room/ws",
			want: "wss://relay.example.com/rooms/other-room/ws",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRelayWSURL(tc.raw, room); got != tc.want {
				t.Fatalf("normalizeRelayWSURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
