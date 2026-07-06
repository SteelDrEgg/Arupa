package conf

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

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
		Auth:   Auth{},
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
	Conf = next
	return nil
}

// Write saves the provided config to the TOML file at the global Path
func Write(conf Config) (err error) {
	mu.Lock()
	defer mu.Unlock()

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

// Read returns a copy of the current configuration
func Read() Config {
	mu.RLock()
	defer mu.RUnlock()

	// Create a deep copy of the config
	conf := Config{
		Listen: Conf.Listen,
		Auth: Auth{
			Users: make(map[string]string),
		},
		PluginSystem: Conf.PluginSystem.Clone(),
	}

	// Copy the users map
	for k, v := range Conf.Auth.Users {
		conf.Auth.Users[k] = v
	}
	if len(Conf.Pages) > 0 {
		conf.Pages = make(map[string]string, len(Conf.Pages))
		for k, v := range Conf.Pages {
			conf.Pages[k] = v
		}
	}

	return conf
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

// GetPluginSystem returns the plugin-system config in a thread-safe manner.
func GetPluginSystem() PluginSystem {
	mu.RLock()
	defer mu.RUnlock()
	return Conf.PluginSystem.Clone()
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
