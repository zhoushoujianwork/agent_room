package models

import (
	"context"
	"time"
)

// CommandRunner executes one local command request and returns captured output.
type CommandRunner interface {
	Run(ctx context.Context, req CommandRunRequest) (CommandRunResult, error)
}

type CommandRunRequest struct {
	Command        string
	WorkingDir     string
	Timeout        time.Duration
	MaxOutputBytes int
	// Shell, when non-empty, overrides the runner's default shell for this one
	// command (e.g. "powershell" on a Windows executor whose cmd.exe quoting
	// mangles JSON arguments). Values are interpreted by localexec.shellCommand.
	Shell string
}

type CommandRunResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	Duration        time.Duration
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
}
