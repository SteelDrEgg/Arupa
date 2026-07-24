package conf

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"

	"arupa/internal/netx"

	"github.com/BurntSushi/toml"
	"github.com/google/renameio"
)

var defaults = Config{
	Listen: ":8080",
	Log: LogConfig{
		Format: "json",
		Level:  "info",
	},
	ServiceSystem: ServiceSystem{
		ServiceDir:     "services",
		ServiceTempDir: "tmp",
	},
}

var configState = struct {
	mu      sync.RWMutex
	path    string
	current Config
}{}

// LoadConfig selects path as the persistent configuration file and loads it.
// A missing file is created empty; hard-coded defaults remain implicit.
func LoadConfig(path string) error {
	configState.mu.Lock()
	defer configState.mu.Unlock()

	previousPath := configState.path
	configState.path = path
	loaded := false
	defer func() {
		if !loaded {
			configState.path = previousPath
		}
	}()

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			if !errors.Is(createErr, os.ErrExist) {
				return fmt.Errorf("create config file: %w", createErr)
			}
			if err := reloadLocked(); err != nil {
				return err
			}
			loaded = true
			return nil
		}
		if closeErr := file.Close(); closeErr != nil {
			return fmt.Errorf("close config file: %w", closeErr)
		}
		configState.current = Config{}
		loaded = true
		return nil
	} else if err != nil {
		return fmt.Errorf("stat config file %s: %w", path, err)
	}

	if err := reloadLocked(); err != nil {
		return err
	}
	loaded = true
	return nil
}

// Reload atomically replaces the in-memory configuration with the complete
// contents of the selected config file. Missing fields are removed.
func Reload() error {
	configState.mu.Lock()
	defer configState.mu.Unlock()
	return reloadLocked()
}

func reloadLocked() error {
	source, err := readConfigLocked()
	if err != nil {
		return err
	}
	next, err := decodeConfig(source)
	if err != nil {
		return err
	}

	configState.current = next
	return nil
}

func readConfigLocked() ([]byte, error) {
	if strings.TrimSpace(configState.path) == "" {
		return nil, fmt.Errorf("config path is not set")
	}
	source, err := os.ReadFile(configState.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config file does not exist: %s: %w", configState.path, err)
	} else if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", configState.path, err)
	}
	return source, nil
}

func decodeConfig(source []byte) (Config, error) {
	var next Config
	metadata, err := toml.NewDecoder(bytes.NewReader(source)).Decode(&next)
	if err != nil {
		return Config{}, fmt.Errorf("decode config file: %w", err)
	}
	if err := validateTOMLKeys(metadata.Keys()); err != nil {
		return Config{}, err
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("unknown config field %q", undecoded[0].String())
	}
	if err := validateConfig(next); err != nil {
		return Config{}, err
	}
	return next, nil
}

func persistLocked(source []byte) error {
	// TODO: define cross-process coordination before a standalone CLI writes
	// the same config file concurrently with the kernel.
	if err := renameio.WriteFile(configState.path, source, 0o600); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}

func validateConfig(cfg Config) error {
	if err := validateRouteAllow(cfg.Route.Allow); err != nil {
		return err
	}
	if err := validateLog(resolveLog(cfg.Log)); err != nil {
		return err
	}
	return validateServiceChecksums(cfg.ServiceSystem.Services)
}

func validateRouteAllow(allow map[string][]string) error {
	for pattern := range allow {
		if err := netx.ValidatePathPattern(pattern); err != nil {
			return fmt.Errorf("invalid Route.Allow pattern: %w", err)
		}
	}
	return nil
}

func validateLog(logCfg LogConfig) error {
	switch strings.ToLower(strings.TrimSpace(logCfg.Format)) {
	case "json", "text":
	default:
		return fmt.Errorf("invalid Log.Format %q: must be json or text", logCfg.Format)
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(logCfg.Level))); err != nil {
		return fmt.Errorf("invalid Log.Level %q: %w", logCfg.Level, err)
	}
	return nil
}

func validateServiceChecksums(services map[string]Service) error {
	for name, service := range services {
		if _, _, err := service.SHA256Checksum(); err != nil {
			return fmt.Errorf("invalid Services.%s.Checksum: %w", name, err)
		}
	}
	return nil
}

func validateTOMLKeys(keys []toml.Key) error {
	for _, key := range keys {
		if !validTOMLKey(key) {
			return fmt.Errorf("unknown or incorrectly cased config field %q", key.String())
		}
	}
	return nil
}

func validTOMLKey(key toml.Key) bool {
	if len(key) == 0 {
		return true
	}
	switch ConfigField(key[0]) {
	case ConfigFieldListen, ConfigFieldTLS, ConfigFieldServiceDir, ConfigFieldServiceTempDir:
		return len(key) == 1
	case ConfigFieldLog:
		if len(key) == 1 {
			return true
		}
		return len(key) == 2 && (LogField(key[1]) == LogFieldFormat || LogField(key[1]) == LogFieldLevel)
	case ConfigFieldUsers, ConfigFieldGroups, ConfigFieldPages:
		return len(key) == 1 || len(key) == 2
	case ConfigFieldRoute:
		if len(key) == 1 {
			return true
		}
		return key[1] == string(RouteFieldAllow) && (len(key) == 2 || len(key) == 3)
	case ConfigFieldServices:
		if len(key) <= 2 {
			return true
		}
		switch ServiceField(key[2]) {
		case ServiceFieldRestart, ServiceFieldRunAsUser, ServiceFieldChecksum, ServiceFieldAllow:
			return len(key) == 3
		case ServiceFieldParams:
			return len(key) == 3 || len(key) == 4
		default:
			return false
		}
	default:
		return false
	}
}

// GetListen returns the configured listen address or the hard-coded default.
func GetListen() string {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	if value := strings.TrimSpace(configState.current.Listen); value != "" {
		return value
	}
	return defaults.Listen
}

// GetTLS reports whether the kernel should create its listener with TLS.
func GetTLS() bool {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return configState.current.TLS
}

// GetLog returns the effective logging configuration.
func GetLog() LogConfig {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return resolveLog(configState.current.Log)
}

func resolveLog(current LogConfig) LogConfig {
	if strings.TrimSpace(current.Format) == "" {
		current.Format = defaults.Log.Format
	}
	if strings.TrimSpace(current.Level) == "" {
		current.Level = defaults.Log.Level
	}
	return current
}

// GetUsers returns a copy of the configured users.
func GetUsers() map[string]string {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return cloneStrings(configState.current.Auth.Users)
}

// GetGroups returns a copy of the configured group membership.
func GetGroups() map[string][]string {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return cloneStringSlices(configState.current.Auth.Groups)
}

// GetRouteAllow returns a copy of the host-level route rules.
func GetRouteAllow() map[string][]string {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return cloneStringSlices(configState.current.Route.Allow)
}

// GetServicePaths returns the effective package and extraction directories.
func GetServicePaths() (serviceDir, serviceTempDir string) {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return servicePathsLocked()
}

func servicePathsLocked() (serviceDir, serviceTempDir string) {
	serviceDir = strings.TrimSpace(configState.current.ServiceSystem.ServiceDir)
	if serviceDir == "" {
		serviceDir = defaults.ServiceSystem.ServiceDir
	}
	serviceTempDir = strings.TrimSpace(configState.current.ServiceSystem.ServiceTempDir)
	if serviceTempDir == "" {
		serviceTempDir = defaults.ServiceSystem.ServiceTempDir
	}
	return serviceDir, serviceTempDir
}

// GetServices returns copies of the explicitly configured services in the same
// order as names. Missing names produce a zero Service and false.
func GetServices(names ...string) ([]Service, []bool) {
	configState.mu.RLock()
	defer configState.mu.RUnlock()
	return servicesLocked(names)
}

func servicesLocked(names []string) ([]Service, []bool) {
	services := make([]Service, len(names))
	found := make([]bool, len(names))
	for index, name := range names {
		service, ok := configState.current.ServiceSystem.Services[name]
		if !ok {
			continue
		}
		services[index] = cloneService(service)
		found[index] = true
	}
	return services, found
}

// GetServiceSettings returns the effective paths and requested explicit
// services from one read of the current configuration. It is intended for a
// service operation that needs these values to remain mutually consistent.
func GetServiceSettings(names ...string) (
	serviceDir string,
	serviceTempDir string,
	services []Service,
	found []bool,
) {
	configState.mu.RLock()
	defer configState.mu.RUnlock()

	serviceDir, serviceTempDir = servicePathsLocked()
	services, found = servicesLocked(names)
	return serviceDir, serviceTempDir, services, found
}

// GetServiceAllows returns copies of only the Allow fields for the requested
// service names. A nil entry means the service did not explicitly configure it.
func GetServiceAllows(names ...string) [][]string {
	configState.mu.RLock()
	defer configState.mu.RUnlock()

	out := make([][]string, len(names))
	for index, name := range names {
		if service, ok := configState.current.ServiceSystem.Services[name]; ok {
			out[index] = cloneSlice(service.Allow)
		}
	}
	return out
}

// GetServiceNames returns all explicitly configured service names. The conf
// package treats every name uniformly; callers decide whether a name such as
// "default" has domain-specific meaning.
func GetServiceNames() []string {
	configState.mu.RLock()
	defer configState.mu.RUnlock()

	out := make([]string, 0, len(configState.current.ServiceSystem.Services))
	for name := range configState.current.ServiceSystem.Services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func cloneConfig(cfg Config) Config {
	out := Config{
		Listen: cfg.Listen,
		TLS:    cfg.TLS,
		Log:    cfg.Log,
		Auth: Auth{
			Users:  cloneStrings(cfg.Auth.Users),
			Groups: cloneStringSlices(cfg.Auth.Groups),
		},
		Route: RouteConfig{
			Allow: cloneStringSlices(cfg.Route.Allow),
		},
		ServiceSystem: ServiceSystem{
			ServiceDir:     cfg.ServiceSystem.ServiceDir,
			ServiceTempDir: cfg.ServiceSystem.ServiceTempDir,
		},
		Pages: cloneStrings(cfg.Pages),
	}
	if cfg.ServiceSystem.Services != nil {
		out.ServiceSystem.Services = make(map[string]Service, len(cfg.ServiceSystem.Services))
		for name, service := range cfg.ServiceSystem.Services {
			out.ServiceSystem.Services[name] = cloneService(service)
		}
	}
	return out
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneStringSlices(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	out := make(map[string][]string, len(values))
	for key, value := range values {
		out[key] = cloneSlice(value)
	}
	return out
}

func cloneSlice(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
