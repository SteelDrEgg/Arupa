package conf

type Config struct {
	Listen string    `toml:",omitempty"`
	TLS    bool      `toml:",omitempty"`
	Log    LogConfig `toml:",omitempty"`
	Auth
	Route RouteConfig `toml:",omitempty"`
	ServiceSystem
	Pages map[string]string
}

// LogConfig controls the process-wide structured log output.
// Format is either "json" or "text"; Level follows slog's level names.
type LogConfig struct {
	Format string `toml:",omitempty"`
	Level  string `toml:",omitempty"`
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
	ServiceDir string `toml:",omitempty"`
	// ServiceTempDir is where service packages are extracted at load time.
	ServiceTempDir string `toml:",omitempty"`
	// Services maps service name to configuration. Names have no special meaning
	// to conf; the service manager interprets "default" as a runtime base.
	Services map[string]Service
}

// Service controls runtime behavior from [Services.<name>].
type Service struct {
	// Restart controls auto-start behavior at host startup.
	// Typical values: "always", "yes", "true", "on", "no", "false", "off".
	Restart string `json:"restart" toml:",omitempty"`
	// RunAsUser controls the OS user used to start gRPC service processes.
	// Empty means the service runs as the current arupa process user.
	RunAsUser string `json:"run_as_user,omitempty" toml:",omitempty"`
	// Checksum is the optional SHA-256 digest of the complete .plg package.
	// It must use the form "sha256:<64 lowercase-or-uppercase hex digits>".
	// An empty value disables package integrity checking.
	Checksum string `json:"checksum,omitempty" toml:",omitempty"`
	// Allow lists groups that may access the service as a whole. Nil means
	// unspecified; an explicit empty list leaves the service open.
	Allow []string `json:"allow,omitempty"`
	// Params are arbitrary string config values passed directly to the service
	// at registration, from [Services.<name>.params].
	Params map[string]string `json:"params,omitempty"`
}
