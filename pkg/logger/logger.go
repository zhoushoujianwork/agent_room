package logger

import (
	"log/slog"
	"os"
)

// New creates the process-wide structured logger.
func New(level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
