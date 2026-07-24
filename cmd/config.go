package main

import (
	"log/slog"

	"arupa/internal/conf"
)

func loadServerConfig(path string, logger *slog.Logger) {
	if err := conf.LoadConfig(path); err != nil {
		logger.With("component", "kernel", "from", "config").Warn("failed to load config; using defaults", "path", path, "err", err)
	}
}
