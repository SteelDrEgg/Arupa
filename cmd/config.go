package main

import (
	"log/slog"

	"arupa/internal/conf"
)

func loadServerConfig(path string, logger *slog.Logger) conf.Config {
	if err := conf.LoadConfig(path); err != nil {
		logger.Warn("failed to load config; using defaults", "path", path, "err", err)
	}
	return conf.Read()
}
