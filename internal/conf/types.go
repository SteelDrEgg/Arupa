package conf

type Config struct {
	Listen string
	TLS    bool
	Log    LogConfig
	Auth
	Route RouteConfig
	ServiceSystem
	Pages map[string]string
}

// LogConfig controls the process-wide structured log output.
// Format is either "json" or "text"; Level follows slog's level names.
type LogConfig struct {
	Format string
	Level  string
}

type Auth struct {
	Users  map[string]string
	Groups map[string][]string
}

// RouteConfig contains host-level access rules. Rules are matched before the
// request reaches either a host handler or a service handler. Patterns are
// exact unless they end in "/", which selects a subtree; "/*" is unsupported.
type RouteConfig struct {
	Allow map[string][]string
}

// ServiceSystem holds service manager configuration and per-service policy.
type ServiceSystem struct {
	// ServiceDir is the directory scanned for *.plg service packages.
	ServiceDir string
	// ServiceTempDir is where service packages are extracted at load time.
	ServiceTempDir string
	// Services maps service name to runtime configuration. The "default" entry
	// is used as a base for discovered services without explicit configuration.
	Services map[string]Service
}

// Service controls runtime behavior from [Services.<name>].
type Service struct {
	// Restart controls auto-start behavior at host startup.
	// Typical values: "always", "yes", "true", "on", "no", "false", "off".
	Restart string `json:"restart"`
	// RunAsUser controls the OS user used to start gRPC service processes.
	// Empty means the service runs as the current arupa process user.
	RunAsUser string `json:"run_as_user,omitempty"`
	// Checksum is the optional SHA-256 digest of the complete .plg package.
	// It must use the form "sha256:<64 lowercase-or-uppercase hex digits>".
	// An empty value disables package integrity checking.
	Checksum string `json:"checksum,omitempty"`
	// Allow lists groups that may access the service as a whole. An empty list
	// leaves the service open; route and event policies are declared by services.
	Allow []string `json:"allow,omitempty"`
	// Params are arbitrary string config values passed directly to the service
	// at registration, from [Services.<name>.params].
	Params map[string]string `json:"params,omitempty"`
}

// ServiceParamsPatch describes a partial update to one service's explicit
// Params override.
type ServiceParamsPatch struct {
	Set    map[string]string
	Delete []string
}
