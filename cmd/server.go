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
	"arupa/internal/service"
	"arupa/internal/web"
)

func runServer(logger *slog.Logger) error {
	serverLog := logger.With("component", "kernel", "from", "server")
	mux := http.NewServeMux()

	// Socket.IO server (services attach their own namespaces).
	socketServer := netx.GetGlobalServer()
	mux.Handle("/socket.io/", socketServer.Handler())
	// Keep auth endpoints so protected service routes/events can be used.
	web.StartLogin(mux)

	sm, err := service.NewManager(service.Options{
		Mux:    mux,
		Socket: socketServer,
		Logger: logger,
		ReservedHTTP: []string{
			"/socket.io/",
			"/api/login", "/api/logout", "/api/check-auth",
			"/api/services", "/api/services/start",
			"/api/services/stop", "/api/services/restart", "/api/services/config",
			"/api/kernel/version", "/api/kernel/reload",
		},
	})
	if err != nil {
		return err
	}
	defer sm.Close()
	web.StartService(mux, sm)

	reloadConfig := func() error {
		return conf.Reload()
	}
	web.StartKernel(mux, version, reloadConfig)

	if err := sm.LoadConfigured(); err != nil {
		serverLog.Error("failed to start configured services", "err", err)
	}
	for _, entry := range sm.Entries() {
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
		serverLog.Info("discovered service", logArgs...)
	}

	listen := conf.GetListen()
	tlsEnabled := conf.GetTLS()
	handler := logHTTPRequests(logger, auth.WithUser(auth.RouteAccess(mux)))
	srv := &http.Server{Addr: listen, Handler: handler}
	if tlsEnabled {
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
		if tlsEnabled {
			protocol = "https"
			serve = func() error { return srv.ListenAndServeTLS("", "") }
		}
		serverLog.Info("arupa listening", "addr", listen, "protocol", protocol)
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
