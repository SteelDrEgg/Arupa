package conf

import (
	"fmt"
	"sort"
	"strings"
)

const pluginsDefaultKey = "default"

// PluginAutoStart reports whether a plugin should be auto-started.
//
// Per-plugin policy under [Plugins.<name>] overrides [Plugins.default].
func (c Config) PluginAutoStart(name string) bool {
	return c.PluginSystem.EffectivePlugin(name).AutoStart()
}

// PluginParams returns the config params a plugin should receive.
//
// Params from [Plugins.default.params] are used as a base, then overridden by
// per-plugin [Plugins.<name>.params] entries.
func (c Config) PluginParams(name string) map[string]string {
	return c.PluginSystem.EffectivePlugin(name).Params
}

// PluginRunAsUser returns the OS user a gRPC plugin should run as.
//
// Per-plugin policy under [Plugins.<name>] overrides [Plugins.default].
// An empty result means the current arupa process user.
func (c Config) PluginRunAsUser(name string) string {
	return c.PluginSystem.EffectivePlugin(name).RunAsUser
}

// PatchPluginParams updates one plugin's explicit Params override and persists
// the full config. Defaults remain inherited at EffectivePlugin read time.
func PatchPluginParams(name string, patch PluginParamsPatch) (Config, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Config{}, fmt.Errorf("plugin name is required")
	}
	if name == pluginsDefaultKey {
		return Config{}, fmt.Errorf("default plugin params cannot be patched by a plugin")
	}

	next := Read()
	if next.PluginSystem.Plugins == nil {
		next.PluginSystem.Plugins = map[string]Plugin{}
	}

	policy, exists := next.PluginSystem.Plugins[name]
	if !exists && len(patch.Set) == 0 {
		return next, nil
	}
	if policy.Params == nil && (exists || len(patch.Set) > 0) {
		policy.Params = map[string]string{}
	}

	for _, key := range patch.Delete {
		delete(policy.Params, key)
	}
	for key, value := range patch.Set {
		policy.Params[key] = value
	}

	if len(policy.Params) == 0 {
		policy.Params = nil
	}
	next.PluginSystem.Plugins[name] = policy

	if err := Write(next); err != nil {
		return Config{}, err
	}
	return next, nil
}

// EffectivePlugin returns the merged runtime config for name.
//
// [Plugins.default] is used as a base. Per-plugin Restart and RunAsUser values
// override the base when non-empty, and Params are merged with per-plugin keys
// taking precedence.
func (s PluginSystem) EffectivePlugin(name string) Plugin {
	base := Plugin{}
	if def, ok := s.Plugins[pluginsDefaultKey]; ok {
		base = clonePlugin(def)
	}
	if policy, ok := s.Plugins[name]; ok && name != pluginsDefaultKey {
		base = mergePlugin(base, policy)
	}
	base.Restart = strings.TrimSpace(base.Restart)
	base.RunAsUser = strings.TrimSpace(base.RunAsUser)
	if base.Params == nil {
		base.Params = map[string]string{}
	}
	return base
}

// AutoStart reports whether this plugin config enables automatic startup.
func (p Plugin) AutoStart() bool {
	return parseRestart(p.Restart)
}

// Clone returns a deep copy of a plugin config.
func (p Plugin) Clone() Plugin {
	return clonePlugin(p)
}

// ConfiguredPluginNames returns explicit plugin config names, excluding default.
func (s PluginSystem) ConfiguredPluginNames() []string {
	out := make([]string, 0, len(s.Plugins))
	for name := range s.Plugins {
		if name == pluginsDefaultKey {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Clone returns a deep copy of the plugin-system configuration.
func (s PluginSystem) Clone() PluginSystem {
	out := PluginSystem{
		PluginDir:     s.PluginDir,
		PluginTempDir: s.PluginTempDir,
		Plugins:       make(map[string]Plugin, len(s.Plugins)),
	}
	for name, policy := range s.Plugins {
		out.Plugins[name] = clonePlugin(policy)
	}
	return out
}

func mergePlugin(base, override Plugin) Plugin {
	if strings.TrimSpace(override.Restart) != "" {
		base.Restart = override.Restart
	}
	if strings.TrimSpace(override.RunAsUser) != "" {
		base.RunAsUser = override.RunAsUser
	}
	if len(override.Params) > 0 {
		if base.Params == nil {
			base.Params = map[string]string{}
		}
		for k, v := range override.Params {
			base.Params[k] = v
		}
	}
	return base
}

func clonePlugin(p Plugin) Plugin {
	out := Plugin{
		Restart:   p.Restart,
		RunAsUser: p.RunAsUser,
	}
	if len(p.Params) > 0 {
		out.Params = make(map[string]string, len(p.Params))
		for k, v := range p.Params {
			out.Params[k] = v
		}
	}
	return out
}

// TODO: Change to enum
func parseRestart(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "always", "yes", "true", "on", "enable", "enabled", "1":
		return true
	default:
		return false
	}
}
