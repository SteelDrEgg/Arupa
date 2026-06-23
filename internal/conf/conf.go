package conf

import (
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
	"sync"
)

var (
	Path string       // Config path
	mu   sync.RWMutex // Protects access to Conf
	Conf = Config{    // Default values
		SSHConfigPath: "~/.ssh",
		Auth:          Auth{},
		Web: Web{
			RootPath: "web",
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
			defer f.Close()
			if err == nil {
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
		Auth: Auth{
			Users: make(map[string]string),
		},
		Web: Conf.Web,
	}

	// Copy the users map
	for k, v := range Conf.Auth.Users {
		conf.Auth.Users[k] = v
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
