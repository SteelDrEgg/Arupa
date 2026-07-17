// Package logx builds the process-wide structured logger.
package logx

import (
	"io"
	"log/slog"
	"strings"

	"arupa/internal/conf"
)

// New returns a logger configured for Arupa's unified log schema. All callers
// add component and from attributes at their trust boundary. Source locations
// are intentionally enabled only for debug logging, where their diagnostic
// value outweighs the additional output volume.
func New(cfg conf.LogConfig, output io.Writer) *slog.Logger {
	level := slog.LevelInfo
	if strings.TrimSpace(cfg.Level) != "" {
		_ = level.UnmarshalText([]byte(strings.TrimSpace(cfg.Level)))
	}

	opts := &slog.HandlerOptions{
		Level:     level,
		AddSource: level <= slog.LevelDebug,
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Format), "text") {
		return slog.New(slog.NewTextHandler(output, opts))
	}
	return slog.New(slog.NewJSONHandler(output, opts))
}
