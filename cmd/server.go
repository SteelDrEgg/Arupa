package main

import (
	"context"
	"errors"
	"fmt"
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
	serverLog := logger.With("component", "kernel", "from", "server")
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

	reloadConfig := func() error {
		if err := conf.Update(); err != nil {
			return err
		}
		pm.UpdateConfig(conf.GetPluginSystem())
		return nil
	}
	web.StartKernel(mux, version, reloadConfig)

	if err := pm.LoadConfigured(); err != nil {
		serverLog.Error("failed to start configured plugins", "err", err)
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
		serverLog.Info("discovered plugin", logArgs...)
	}

	handler := logHTTPRequests(logger, auth.WithUser(auth.RouteAccess(mux)))
	srv := &http.Server{Addr: cfg.Listen, Handler: handler}
	if cfg.TLS {
		tlsConfig, err := netx.NewSelfSignedTLSConfig()
		if err != nil {
			return fmt.Errorf("configure self-signed TLS: %w", err)
		}
		srv.TLSConfig = tlsConfig
	}
	errCh := make(chan error, 1)

	go func() {
		protocol := "http"
		serve := srv.ListenAndServe
		if cfg.TLS {
			protocol = "https"
			serve = func() error { return srv.ListenAndServeTLS("", "") }
		}
		serverLog.Info("arupa listening", "addr", cfg.Listen, "protocol", protocol)
		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(stop)

	for {
		select {
		case sig := <-stop:
			if sig == syscall.SIGHUP {
				if err := reloadConfig(); err != nil {
					logger.With("component", "kernel", "from", "config").Error("failed to reload configuration", "err", err)
				} else {
					logger.With("component", "kernel", "from", "config").Info("configuration reloaded")
				}
				continue
			}
		case err := <-errCh:
			return err
		}
		break
	}

	serverLog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
