package conf

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"arupa/internal/netx"

	"github.com/BurntSushi/toml"
	"github.com/google/renameio"
)

var (
	Path string       // Config path
	mu   sync.RWMutex // Protects access to Conf
	Conf = defaultConfig()
)

func defaultConfig() Config {
	return Config{
		Listen: ":8080",
		Log: LogConfig{
			Format: "json",
			Level:  "info",
		},
		Auth: Auth{},
		PluginSystem: PluginSystem{
			PluginDir:     "plugins",
			PluginTempDir: "tmp",
		},
	}
}

// LoadConfig Set Path and load config into memory
// Run this at start
func LoadConfig(path string) error {
	Path = path
	err := Update()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
			if err == nil {
				defer f.Close()
				mu.Lock()
				Conf = defaultConfig()
				mu.Unlock()
				return nil
			}
		}
		return fmt.Errorf("failed to load config: %w", err)
	}
	return nil
}

// Update reads the config file and loads it into the global Conf variable
func Update() (err error) {
	mu.Lock()
	defer mu.Unlock()

	if _, err = os.Stat(Path); os.IsNotExist(err) {
		return fmt.Errorf("config file does not exist: %s: %w", Path, err)
	} else if err != nil {
		return fmt.Errorf("failed to stat config file %s: %w", Path, err)
	}
	next := defaultConfig()
	_, err = toml.DecodeFile(Path, &next)
	if err != nil {
		return fmt.Errorf("failed to update global config %w", err)
	}
	if err := validateRouteAllow(next.Route.Allow); err != nil {
		return err
	}
	if err := validateLog(next.Log); err != nil {
		return err
	}
	if err := validatePluginChecksums(next.PluginSystem.Plugins); err != nil {
		return err
	}
	Conf = next
	return nil
}

// Write saves the provided config to the TOML file at the global Path
func Write(conf Config) (err error) {
	mu.Lock()
	defer mu.Unlock()
	return writeLocked(conf)
}

// writeLocked persists conf and updates Conf. The caller must hold mu.Lock.
func writeLocked(conf Config) (err error) {
	if err := validateRouteAllow(conf.Route.Allow); err != nil {
		return err
	}
	if err := validatePluginChecksums(conf.PluginSystem.Plugins); err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(conf); err != nil {
		return fmt.Errorf("failed to write config file %w", err)
	}

	mode := os.FileMode(0o644)
	if st, err := os.Stat(Path); err == nil {
		mode = st.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat config file %w", err)
	}

	if err := renameio.WriteFile(Path, buf.Bytes(), mode); err != nil {
		return fmt.Errorf("failed to replace config file %w", err)
	}

	// Update global config after successful write
	Conf = conf
	return nil
}

func validateRouteAllow(allow map[string][]string) error {
	for pattern := range allow {
		if err := netx.ValidatePathPattern(pattern); err != nil {
			return fmt.Errorf("invalid Route.Allow pattern: %w", err)
		}
	}
	return nil
}

// Read returns a copy of the current configuration
func Read() Config {
	mu.RLock()
	defer mu.RUnlock()
	return cloneConfig(Conf)
}

// cloneConfig returns a deep copy of cfg. The caller is responsible for
// synchronizing access when cfg is the package-global Conf.
func cloneConfig(cfg Config) Config {
	conf := Config{
		Listen: cfg.Listen,
		Log:    cfg.Log,
		Auth: Auth{
			Users:  make(map[string]string),
			Groups: make(map[string][]string, len(cfg.Auth.Groups)),
		},
		Route: RouteConfig{
			Allow: cloneRouteAllow(cfg.Route.Allow),
		},
		PluginSystem: cfg.PluginSystem.Clone(),
	}

	for k, v := range cfg.Auth.Users {
		conf.Auth.Users[k] = v
	}
	for group, users := range cfg.Auth.Groups {
		conf.Auth.Groups[group] = append([]string(nil), users...)
	}
	if len(cfg.Pages) > 0 {
		conf.Pages = make(map[string]string, len(cfg.Pages))
		for k, v := range cfg.Pages {
			conf.Pages[k] = v
		}
	}

	return conf
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

func validatePluginChecksums(plugins map[string]Plugin) error {
	for name, plugin := range plugins {
		if _, _, err := plugin.SHA256Checksum(); err != nil {
			return fmt.Errorf("invalid Plugins.%s.Checksum: %w", name, err)
		}
	}
	return nil
}

// GetUsers returns a copy of the users map in a thread-safe manner
func GetUsers() map[string]string {
	mu.RLock()
	defer mu.RUnlock()

	users := make(map[string]string)
	for k, v := range Conf.Auth.Users {
		users[k] = v
	}
	return users
}

// GetGroups returns a deep copy of configured group membership.
func GetGroups() map[string][]string {
	mu.RLock()
	defer mu.RUnlock()
	return cloneGroups(Conf.Auth.Groups)
}

// GetPluginSystem returns the plugin-system config in a thread-safe manner.
func GetPluginSystem() PluginSystem {
	mu.RLock()
	defer mu.RUnlock()
	return Conf.PluginSystem.Clone()
}

// GetRouteAllow returns a deep copy of the current host-level route rules.
func GetRouteAllow() map[string][]string {
	mu.RLock()
	defer mu.RUnlock()
	return cloneRouteAllow(Conf.Route.Allow)
}

func cloneGroups(groups map[string][]string) map[string][]string {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string][]string, len(groups))
	for group, users := range groups {
		out[group] = append([]string(nil), users...)
	}
	return out
}

func cloneRouteAllow(allow map[string][]string) map[string][]string {
	if len(allow) == 0 {
		return nil
	}
	out := make(map[string][]string, len(allow))
	for pattern, groups := range allow {
		out[pattern] = append([]string(nil), groups...)
	}
	return out
}

// SetPluginPaths updates plugin package and temp directories and persists them.
func SetPluginPaths(pluginDir, pluginTempDir string) (Config, error) {
	pluginDir = strings.TrimSpace(pluginDir)
	pluginTempDir = strings.TrimSpace(pluginTempDir)

	if pluginDir == "" {
		return Config{}, fmt.Errorf("plugin directory cannot be empty")
	}
	if pluginTempDir == "" {
		return Config{}, fmt.Errorf("plugin temp directory cannot be empty")
	}

	next := Read()
	next.PluginSystem.PluginDir = pluginDir
	next.PluginSystem.PluginTempDir = pluginTempDir

	if err := Write(next); err != nil {
		return Config{}, err
	}
	return next, nil
}
