package web

import (
	"encoding/json"
	"fmt"
	"minimalpanel/internal/auth"
	"minimalpanel/internal/conf"
	"minimalpanel/internal/netx"
	"minimalpanel/internal/plugin"
	"net/http"
	"os"
	"sort"
	"strings"
)

// pluginActionRequest is the common JSON payload for start/stop/restart.
type pluginActionRequest struct {
	Name string `json:"name"`
}

// pluginConfigRequest carries plugin directory settings from the management UI.
type pluginConfigRequest struct {
	PluginDir     string `json:"plugin_dir"`
	PluginTempDir string `json:"plugin_temp_dir"`
}

// pluginView is the catalog row returned to the management UI. It combines
// scanned package metadata with the current runtime status.
type pluginView struct {
	Name            string              `json:"name"`
	Version         string              `json:"version"`
	Type            string              `json:"type"`
	ContractVersion int                 `json:"contract_version"`
	Command         string              `json:"command"`
	PackagePath     string              `json:"package_path"`
	Config          conf.Plugin         `json:"config"`
	Metadata        map[string]any      `json:"metadata,omitempty"`
	Status          plugin.PluginStatus `json:"status"`
}

// StartPlugin registers plugin management endpoints used by frontend pages.
// All plugin endpoints are protected.
func StartPlugin(mux *http.ServeMux, pm *plugin.Manager) {
	mux.HandleFunc("/api/plugins", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlePluginList(w, r, pm)
	}))
	mux.HandleFunc("/api/plugins/scan", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlePluginScan(w, r, pm)
	}))
	mux.HandleFunc("/api/plugins/start", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlePluginStart(w, r, pm)
	}))
	mux.HandleFunc("/api/plugins/stop", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlePluginStop(w, r, pm)
	}))
	mux.HandleFunc("/api/plugins/restart", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlePluginRestart(w, r, pm)
	}))
	mux.HandleFunc("/api/plugins/config", auth.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		handlePluginConfig(w, r, pm)
	}))
}

func handlePluginList(w http.ResponseWriter, r *http.Request, pm *plugin.Manager) {
	if r.Method != http.MethodGet {
		netx.WriteMethodNotAllowed(w)
		return
	}

	entries := pm.Entries()
	plugins := make([]pluginView, 0, len(entries))
	for _, entry := range entries {
		plugins = append(plugins, pluginView{
			Name:            entry.Name,
			Version:         entry.Version,
			Type:            entry.Type,
			ContractVersion: entry.ContractVersion,
			Command:         entry.Command,
			PackagePath:     entry.PackagePath,
			Config:          entry.Config,
			Metadata:        entry.Metadata,
			Status:          entry.Status,
		})
	}

	running := pm.Registry().List()
	sort.Slice(running, func(i, j int) bool {
		return running[i].InstanceID < running[j].InstanceID
	})

	cfg := conf.GetPluginSystem()
	netx.WriteSuccess(w, "Plugin state fetched", map[string]any{
		"plugin_dir":      cfg.PluginDir,
		"plugin_temp_dir": cfg.PluginTempDir,
		"discovered":      plugins,
		"running":         running,
	})
}

func handlePluginScan(w http.ResponseWriter, r *http.Request, pm *plugin.Manager) {
	if r.Method != http.MethodPost {
		netx.WriteMethodNotAllowed(w)
		return
	}

	pm.UpdateConfig(conf.GetPluginSystem())
	if err := pm.Scan(); err != nil {
		netx.WriteInternalServerError(w, "Failed to scan plugin directory", err)
		return
	}

	pluginCfg := pm.Config()
	netx.WriteSuccess(w, "Plugin directory scanned", map[string]any{
		"plugin_dir": pluginCfg.PluginDir,
		"count":      len(pm.Entries()),
	})
}

func handlePluginStart(w http.ResponseWriter, r *http.Request, pm *plugin.Manager) {
	if r.Method != http.MethodPost {
		netx.WriteMethodNotAllowed(w)
		return
	}

	name, ok := readPluginActionName(w, r)
	if !ok {
		return
	}

	if err := pm.Start(name); err != nil {
		netx.WriteBadRequest(w, fmt.Sprintf("Failed to start plugin %q: %v", name, err))
		return
	}

	netx.WriteSuccess(w, "Plugin started", map[string]any{
		"name": name,
	})
}

func handlePluginStop(w http.ResponseWriter, r *http.Request, pm *plugin.Manager) {
	if r.Method != http.MethodPost {
		netx.WriteMethodNotAllowed(w)
		return
	}

	name, ok := readPluginActionName(w, r)
	if !ok {
		return
	}

	if err := pm.Stop(name); err != nil {
		netx.WriteBadRequest(w, fmt.Sprintf("Failed to stop plugin %q: %v", name, err))
		return
	}

	netx.WriteSuccess(w, "Plugin stopped", map[string]any{
		"name": name,
	})
}

func handlePluginRestart(w http.ResponseWriter, r *http.Request, pm *plugin.Manager) {
	if r.Method != http.MethodPost {
		netx.WriteMethodNotAllowed(w)
		return
	}

	name, ok := readPluginActionName(w, r)
	if !ok {
		return
	}

	if err := pm.Restart(name); err != nil {
		netx.WriteBadRequest(w, fmt.Sprintf("Failed to restart plugin %q: %v", name, err))
		return
	}

	netx.WriteSuccess(w, "Plugin restarted", map[string]any{
		"name": name,
	})
}

func readPluginActionName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req pluginActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		netx.WriteBadRequest(w, "Invalid request body")
		return "", false
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		netx.WriteBadRequest(w, "Plugin name is required")
		return "", false
	}
	return name, true
}

func handlePluginConfig(w http.ResponseWriter, r *http.Request, pm *plugin.Manager) {
	switch r.Method {
	case http.MethodGet:
		cfg := conf.GetPluginSystem()
		netx.WriteSuccess(w, "Plugin config fetched", map[string]any{
			"plugin_dir":      cfg.PluginDir,
			"plugin_temp_dir": cfg.PluginTempDir,
		})
	case http.MethodPut:
		var req pluginConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			netx.WriteBadRequest(w, "Invalid request body")
			return
		}

		req.PluginDir = strings.TrimSpace(req.PluginDir)
		req.PluginTempDir = strings.TrimSpace(req.PluginTempDir)
		if req.PluginDir == "" || req.PluginTempDir == "" {
			netx.WriteBadRequest(w, "plugin_dir and plugin_temp_dir are required")
			return
		}

		// Validate/create directories first, then persist config.
		if err := os.MkdirAll(req.PluginDir, 0o755); err != nil {
			netx.WriteBadRequest(w, fmt.Sprintf("Invalid plugin_dir: %v", err))
			return
		}
		if err := os.MkdirAll(req.PluginTempDir, 0o755); err != nil {
			netx.WriteBadRequest(w, fmt.Sprintf("Invalid plugin_temp_dir: %v", err))
			return
		}

		oldCfg := conf.GetPluginSystem()
		newCfg, err := conf.SetPluginPaths(req.PluginDir, req.PluginTempDir)
		if err != nil {
			netx.WriteInternalServerError(w, "Failed to persist plugin config", err)
			return
		}

		pm.UpdateConfig(newCfg.PluginSystem)
		if err := pm.Scan(); err != nil {
			netx.WriteInternalServerError(w, "Plugin config saved, but scan failed", err)
			return
		}

		tempDirChanged := oldCfg.PluginTempDir != newCfg.PluginSystem.PluginTempDir
		netx.WriteSuccess(w, "Plugin config updated", map[string]any{
			"plugin_dir":                      newCfg.PluginSystem.PluginDir,
			"plugin_temp_dir":                 newCfg.PluginSystem.PluginTempDir,
			"temp_dir_requires_restart":       tempDirChanged,
			"temp_dir_restart_reason":         "plugin manager temp dir is initialized at startup",
			"discovered_plugin_count":         len(pm.Entries()),
			"scan_path_effective_immediately": true,
		})
	default:
		netx.WriteMethodNotAllowed(w)
	}
}
