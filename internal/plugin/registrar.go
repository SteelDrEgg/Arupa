package plugin

import "log/slog"

type pluginRegistrar struct {
	router *pluginRouter
	socket *socketBridge
	log    *slog.Logger
}

func newPluginRegistrar(router *pluginRouter, socket *socketBridge, log *slog.Logger) *pluginRegistrar {
	if log == nil {
		log = slog.Default()
	}
	return &pluginRegistrar{router: router, socket: socket, log: log}
}

func (r *pluginRegistrar) register(owner, pluginRoot string, reg *RegisterResult, lp *loadedPlugin) bool {
	degraded := false
	if reg == nil {
		return degraded
	}
	for _, route := range reg.Routes {
		if r.router == nil {
			degraded = true
			r.log.Error("failed to register plugin route", "plugin", owner, "pattern", route.Pattern, "err", "router is not configured")
			continue
		}
		if err := r.router.registerRoute(owner, route, lp); err != nil {
			degraded = true
			r.log.Error("failed to register plugin route", "plugin", owner, "pattern", route.Pattern, "err", err)
		}
	}
	for _, mount := range reg.Static {
		if r.router == nil {
			degraded = true
			r.log.Error("failed to register plugin static mount", "plugin", owner, "prefix", mount.Prefix, "dir", mount.Directory, "err", "router is not configured")
			continue
		}
		if err := r.router.registerStatic(owner, pluginRoot, mount, lp); err != nil {
			degraded = true
			r.log.Error("failed to register plugin static mount", "plugin", owner, "prefix", mount.Prefix, "dir", mount.Directory, "err", err)
		}
	}
	for _, ns := range reg.Namespaces {
		if r.socket == nil {
			degraded = true
			r.log.Error("failed to register plugin socket namespace", "plugin", owner, "namespace", ns.Name, "err", "socket bridge is not configured")
			continue
		}
		if err := r.socket.register(owner, ns, lp); err != nil {
			degraded = true
			r.log.Error("failed to register plugin socket namespace", "plugin", owner, "namespace", ns.Name, "err", err)
		}
	}
	return degraded
}

func (r *pluginRegistrar) unregister(owner string) {
	if r.router != nil {
		r.router.unregisterRoutes(owner)
		r.router.unregisterStatic(owner)
	}
	if r.socket != nil {
		r.socket.unregisterPlugin(owner)
	}
}
