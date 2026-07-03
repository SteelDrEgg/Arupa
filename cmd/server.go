package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"minimalpanel/internal/conf"
	"minimalpanel/internal/netx"
	"minimalpanel/internal/plugin"
	"minimalpanel/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	configPath := flag.String("config", "config.toml", "path to config file")
	flag.Parse()

	if err := conf.LoadConfig(*configPath); err != nil {
		logger.Warn("failed to load config; using defaults", "path", *configPath, "err", err)
	}
	cfg := conf.Read()

	mux := http.NewServeMux()

	// Socket.IO server (plugins attach their own namespaces).
	socketServer := netx.GetGlobalServer()
	mux.Handle("/socket.io/", socketServer.Handler())
	// Keep auth endpoints so protected plugin routes/events can be used.
	web.StartLogin(mux)

	pm, err := plugin.NewManager(plugin.Options{
		Config: cfg.PluginSystem,
		Mux:    mux,
		Socket: socketServer,
		Logger: logger,
	})
	if err != nil {
		logger.Error("failed to create plugin manager", "err", err)
		os.Exit(1)
	}
	defer pm.Close()
	web.StartPlugin(mux, pm)

	if err := pm.LoadConfigured(); err != nil {
		logger.Error("failed to start configured plugins", "err", err)
	}
	for _, entry := range pm.Entries() {
		logArgs := []any{
			"name", entry.Name,
			"version", entry.Version,
			"type", entry.Type,
			"status", entry.Status,
			"auto_start", entry.Config.AutoStart(),
			"package", entry.PackagePath,
		}
		if entry.Type == "grpc" {
			logArgs = append(logArgs, "run_as_user", entry.Config.RunAsUser)
		}
		logger.Info("discovered plugin", logArgs...)
	}

	srv := &http.Server{Addr: cfg.Listen, Handler: mux}

	go func() {
		logger.Info("minimalpanel listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
