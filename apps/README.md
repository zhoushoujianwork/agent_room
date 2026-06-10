# Agent Room Apps

This directory contains native client experiments for Agent Room.

Current app workspaces:

- `ios/AgentRoomMobile`: SwiftPM-first iPhone client package with SwiftUI screens,
  REST helpers, WebSocket room streaming, and tests for the shared relay models.

The existing web UI remains the production client. Native apps should reuse the
relay protocol rather than adding app-specific server endpoints unless the
workflow needs a new capability.
