package provider

import (
	"fmt"

	"agent-room/internal/config"
	"agent-room/internal/io/cliprovider"
	"agent-room/internal/io/opencodeprovider"
	"agent-room/internal/models"
)

func New(cfg config.Config) (models.AgentProvider, error) {
	switch cfg.Provider {
	case "", "claude":
		return cliprovider.NewClaudeProvider(
			cfg.Claude.Command,
			cfg.Claude.WorkingDir,
			cfg.Claude.Model,
			cfg.Claude.APIBaseURL,
			cfg.Claude.APIKey,
			cfg.Claude.Timeout,
			cfg.Claude.MaxTurns,
			cfg.Claude.DisableTools,
			cfg.Claude.NoSessionPersistence,
			cfg.Claude.SkipPermissions,
		), nil
	case "opencode":
		return opencodeprovider.NewOpenCodeProvider(
			cfg.OpenCode.ServerURL,
			cfg.Claude.WorkingDir,
			cfg.OpenCode.Timeout,
			cfg.OpenCode.ProviderID,
			cfg.OpenCode.ModelID,
			cfg.OpenCode.SkipPermissions,
		), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}
