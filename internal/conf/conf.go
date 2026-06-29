package conf

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

var (
	Path string       // Config path
	mu   sync.RWMutex // Protects access to Conf
	Conf = Config{    // Default values
		SSHConfigPath: "~/.ssh",
		Listen:        ":8080",
		Auth:          Auth{},
		Web: Web{
			RootPath: "web",
		},
		Plugin: Plugin{
			PluginDir:     "plugins",
			PluginTempDir: "tmp",
		},
		Plugins: map[string]PluginPolicy{
			pluginsDefaultKey: {Restart: "always"},
		},
	}
)

// LoadConfig Set Path and load config into memory
// Run this at start
func LoadConfig(path string) error {
	Path = path
	err := Update()
	if err != nil {
		if os.IsNotExist(err) {
			f, err := os.OpenFile(path, os.O_CREATE, 0644)
			if err == nil {
				defer f.Close()
				return nil
			}
		}
		return fmt.Errorf("failed to load config")
	}
	return nil
}

// Update reads the config file and loads it into the global Conf variable
func Update() (err error) {
	mu.Lock()
	defer mu.Unlock()

	if _, err = os.Stat(Path); os.IsNotExist(err) {
		return fmt.Errorf("config file does not exist: %s", Path)
	}
	_, err = toml.DecodeFile(Path, &Conf)
	if err != nil {
		return fmt.Errorf("failed to update global config %w", err)
	}
	return nil
}

// Write saves the provided config to the TOML file at the global Path
func Write(conf Config) (err error) {
	mu.Lock()
	defer mu.Unlock()

	f, err := os.Create(Path)
	if err != nil {
		return fmt.Errorf("failed to create config file %w", err)
	}
	defer f.Close()
	err = toml.NewEncoder(f).Encode(conf)
	if err != nil {
		return fmt.Errorf("failed to write config file %w", err)
	}

	// Update global config after successful write
	Conf = conf
	return nil
}

// Read returns a copy of the current configuration
func Read() Config {
	mu.RLock()
	defer mu.RUnlock()

	// Create a deep copy of the config
	conf := Config{
		SSHConfigPath: Conf.SSHConfigPath,
		Listen:        Conf.Listen,
		Auth: Auth{
			Users: make(map[string]string),
		},
		Web:     Conf.Web,
		Plugin:  Conf.Plugin,
		Plugins: make(map[string]PluginPolicy),
	}

	// Copy the users map
	for k, v := range Conf.Auth.Users {
		conf.Auth.Users[k] = v
	}
	for k, v := range Conf.Plugins {
		policy := PluginPolicy{Restart: v.Restart}
		if len(v.Params) > 0 {
			policy.Params = make(map[string]string, len(v.Params))
			for pk, pv := range v.Params {
				policy.Params[pk] = pv
			}
		}
		conf.Plugins[k] = policy
	}

	return conf
}

// GetSSHConfigPath returns the SSH config path in a thread-safe manner
func GetSSHConfigPath() string {
	mu.RLock()
	defer mu.RUnlock()
	return Conf.SSHConfigPath
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

// GetWeb returns the Web config in a thread-safe manner
func GetWeb() Web {
	mu.RLock()
	defer mu.RUnlock()
	return Conf.Web
}

// GetPlugin returns the Plugin config in a thread-safe manner.
func GetPlugin() Plugin {
	mu.RLock()
	defer mu.RUnlock()
	return Conf.Plugin
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
	next.Plugin.PluginDir = pluginDir
	next.Plugin.PluginTempDir = pluginTempDir

	if err := Write(next); err != nil {
		return Config{}, err
	}
	return next, nil
}
