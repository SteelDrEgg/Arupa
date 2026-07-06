package conf

type Config struct {
	Listen string
	Auth
	PluginSystem
	Pages map[string]string
}

type Auth struct {
	Users map[string]string
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
	// Empty means the plugin runs as the current minimalpanel process user.
	RunAsUser string `json:"run_as_user,omitempty"`
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
