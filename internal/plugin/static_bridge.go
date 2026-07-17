package plugin

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"arupa/internal/auth"
	"arupa/internal/netx"
)

// registerStatic wires a plugin-declared static directory or file into the
// plugin dispatch table. Directory mounts are normalized to subtree patterns
// ending in "/", while file mounts remain exact patterns.
func (router *pluginRouter) registerStatic(owner, pluginRoot string, mount StaticMount, lp *loadedPlugin) error {
	prefix := strings.TrimSpace(mount.Prefix)
	if err := netx.ValidatePathPattern(prefix); err != nil {
		return fmt.Errorf("invalid static mount prefix: %w", err)
	}

	dir := strings.TrimSpace(mount.Directory)
	if dir == "" {
		return fmt.Errorf("static mount path is required")
	}
	if pluginRoot != "" {
		dir = strings.ReplaceAll(dir, "$PLUGIN_ROOT", pluginRoot)
	}
	dir = filepath.Clean(dir)

	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat static mount path %q: %w", dir, err)
	}
	pattern := prefix
	var handler http.Handler
	if st.IsDir() {
		if !strings.HasSuffix(pattern, "/") {
			pattern += "/"
		}

		stripPrefix := strings.TrimSuffix(pattern, "/")
		if stripPrefix == "" {
			stripPrefix = "/"
		}
		handler = http.StripPrefix(stripPrefix, http.FileServer(http.Dir(dir)))
	} else {
		if strings.HasSuffix(pattern, "/") {
			return fmt.Errorf("static file mount prefix %q must not end with '/'", pattern)
		}
		filePath := dir
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			file, err := os.Open(filePath)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer file.Close()

			info, err := file.Stat()
			if err != nil {
				http.NotFound(w, r)
				return
			}

			// ServeContent provides the normal static-file features without
			// ServeFile's automatic /index.html -> / redirect.
			http.ServeContent(w, r, filepath.Base(filePath), info.ModTime(), file)
		})
	}

	router.routeMu.RLock()
	routeOwner, routeExists := router.routePatternOwnedByOtherLocked(pattern, owner)
	router.routeMu.RUnlock()
	if routeExists {
		return fmt.Errorf("static mount prefix %q already owned by http route from plugin %q", pattern, routeOwner)
	}

	router.staticMu.Lock()
	defer router.staticMu.Unlock()
	if binding, ok := router.static[pattern]; ok {
		if binding.owner != owner {
			return fmt.Errorf("static mount prefix %q already owned by plugin %q", pattern, binding.owner)
		}
		binding.owner = owner
		binding.mount = mount
		binding.plugin = lp
		binding.handler = handler
		return nil
	}

	router.static[pattern] = &staticMountBinding{owner: owner, mount: mount, handler: handler, plugin: lp}
	return nil
}

func (router *pluginRouter) routePatternOwnedByOtherLocked(pattern, owner string) (string, bool) {
	for key, binding := range router.routes {
		if key.pattern == pattern && binding != nil && binding.owner != owner {
			return binding.owner, true
		}
	}
	return "", false
}

func (router *pluginRouter) unregisterStatic(owner string) {
	router.staticMu.Lock()
	defer router.staticMu.Unlock()
	for pattern, binding := range router.static {
		if binding.owner == owner {
			delete(router.static, pattern)
		}
	}
}

// matchPluginStatic returns the longest static mount pattern that matches path.
// The returned handler already knows whether it should serve a file or strip a
// directory prefix.
func (router *pluginRouter) matchPluginStatic(path string) (StaticMount, http.Handler, *loadedPlugin, int) {
	router.staticMu.RLock()
	defer router.staticMu.RUnlock()

	var best *staticMountBinding
	bestPattern := ""
	for pattern, binding := range router.static {
		if binding == nil || binding.handler == nil {
			continue
		}
		if !netx.MatchPathPattern(path, pattern, netx.RootPathSubtree) {
			continue
		}
		if len(pattern) > len(bestPattern) {
			best = binding
			bestPattern = pattern
		}
	}
	if best == nil {
		return StaticMount{}, nil, nil, -1
	}
	return best.mount, best.handler, best.plugin, len(bestPattern)
}

// handlePluginStatic applies mount-level auth and delegates file serving to the
// prepared static handler.
func (router *pluginRouter) handlePluginStatic(mount StaticMount, lp *loadedPlugin, handler http.Handler, w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromRequest(r)
	if lp != nil && writePluginAccessError(w, r, lp.accessPolicy().Check(user)) {
		return
	}
	if writePluginAccessError(w, r, mount.Access.Check(user)) {
		return
	}
	handler.ServeHTTP(w, r)
}
