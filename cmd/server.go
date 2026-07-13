package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"arupa/internal/auth"
	"arupa/internal/conf"
	"arupa/internal/netx"
	"arupa/internal/plugin"
	"arupa/internal/web"
)

func runServer(cfg conf.Config, logger *slog.Logger) error {
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
		return err
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

	handler := auth.WithUser(auth.RouteAccess(cfg.Route.Allow, mux))
	srv := &http.Server{Addr: cfg.Listen, Handler: handler}
	errCh := make(chan error, 1)

	go func() {
		logger.Info("arupa listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-stop:
	case err := <-errCh:
		return err
	}

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
