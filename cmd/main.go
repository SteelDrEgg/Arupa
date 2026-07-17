package main

import (
	"fmt"
	"log/slog"
	"os"

	"arupa/internal/conf"
	"arupa/internal/logx"
)

func main() {
	bootstrapLogger := logx.New(conf.LogConfig{}, os.Stdout)
	slog.SetDefault(bootstrapLogger)

	opts, err := parseCLI(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "arupa: %v\n", err)
		fmt.Fprintln(os.Stderr, "Run `arupa -help` for usage.")
		os.Exit(2)
	}
	if opts.ShowHelp {
		helpOpts := cliOptions{ConfigPath: defaultConfigPath}
		fs := flagSetForHelp(os.Stdout, &helpOpts)
		writeUsage(os.Stdout, fs)
		return
	}
	if opts.ShowVersion {
		fmt.Fprintf(os.Stdout, "Arupa Kernel %s\n", version)
		return
	}

	if opts.Username != "" {
		logger := bootstrapLogger.With("component", "kernel", "from", "cli")
		if err := createOrUpdateUser(opts.ConfigPath, opts.Username, opts.Password); err != nil {
			logger.Error("failed to write user to config", "path", opts.ConfigPath, "user", opts.Username, "err", err)
			os.Exit(1)
		}
		logger.Info("user written to config", "path", opts.ConfigPath, "user", opts.Username)
		return
	}

	cfg := loadServerConfig(opts.ConfigPath, bootstrapLogger)
	logger := logx.New(cfg.Log, os.Stdout)
	slog.SetDefault(logger)
	if err := runServer(cfg, logger); err != nil {
		logger.With("component", "kernel", "from", "server").Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}
