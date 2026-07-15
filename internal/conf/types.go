package conf

type Config struct {
	Listen string
	Auth
	Route RouteConfig
	PluginSystem
	Pages map[string]string
}

type Auth struct {
	Users  map[string]string
	Groups map[string][]string
}

// RouteConfig contains host-level access rules. Rules are matched before the
// request reaches either a host handler or a plugin handler. Patterns are
// exact unless they end in "/", which selects a subtree; "/*" is unsupported.
type RouteConfig struct {
	Allow map[string][]string
}

// PluginSystem holds plugin manager configuration and per-plugin policy.
type PluginSystem struct {
	// PluginDir is the directory scanned for *.plg plugin packages.
	PluginDir string
	// PluginTempDir is where plugin packages are extracted at load time.
	PluginTempDir string
	// Plugins maps plugin name to runtime configuration. The "default" entry
	// is used as a base for discovered plugins without explicit configuration.
	Plugins map[string]Plugin
}

// Plugin controls runtime behavior from [Plugins.<name>].
type Plugin struct {
	// Restart controls auto-start behavior at host startup.
	// Typical values: "always", "yes", "true", "on", "no", "false", "off".
	Restart string `json:"restart"`
	// RunAsUser controls the OS user used to start gRPC plugin processes.
	// Empty means the plugin runs as the current arupa process user.
	RunAsUser string `json:"run_as_user,omitempty"`
	// Allow lists groups that may access the plugin as a whole. An empty list
	// leaves the plugin open; route and event policies are declared by plugins.
	Allow []string `json:"allow,omitempty"`
	// Params are arbitrary string config values passed directly to the plugin
	// at registration, from [Plugins.<name>.params].
	Params map[string]string `json:"params,omitempty"`
}

// PluginParamsPatch describes a partial update to one plugin's explicit
// Params override.
type PluginParamsPatch struct {
	Set    map[string]string
	Delete []string
}
