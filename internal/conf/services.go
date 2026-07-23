package conf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const servicesDefaultKey = "default"

const (
	// envParamPrefix marks a Params value as an environment variable reference.
	// The optional suffix is deliberately explicit so a missing variable cannot
	// silently turn a required setting into an empty value.
	envParamPrefix   = "env://"
	envParamOptional = "?"
)

// ServiceAutoStart reports whether a service should be auto-started.
//
// Per-service policy under [Services.<name>] overrides [Services.default].
func (c Config) ServiceAutoStart(name string) bool {
	return c.ServiceSystem.EffectiveService(name).AutoStart()
}

// ServiceParams returns the config params a service should receive.
//
// Params from [Services.default.params] are used as a base, then overridden by
// per-service [Services.<name>.params] entries.
func (c Config) ServiceParams(name string) map[string]string {
	return c.ServiceSystem.EffectiveService(name).Params
}

// ResolveParams returns the values a service should receive at registration.
//
// A normal value is passed through unchanged. Values in the form
// "env://NAME" are read from the environment and are required. Values in the
// form "env://NAME?" are optional; an unset variable is passed as an empty
// string. LookupEnv is injected so this logic remains deterministic and easy
// to test.
func (p Service) ResolveParams(lookup func(string) (string, bool)) (map[string]string, error) {
	if lookup == nil {
		return nil, fmt.Errorf("environment lookup is required")
	}

	resolved := make(map[string]string, len(p.Params))
	for key, raw := range p.Params {
		name, optional, isReference, err := parseEnvParamReference(raw)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", key, err)
		}
		if !isReference {
			resolved[key] = raw
			continue
		}

		value, exists := lookup(name)
		if !exists {
			if optional {
				resolved[key] = ""
				continue
			}
			return nil, fmt.Errorf("required environment variable %q is not set", name)
		}
		resolved[key] = value
	}
	return resolved, nil
}

// ServiceRunAsUser returns the OS user a gRPC service should run as.
//
// Per-service policy under [Services.<name>] overrides [Services.default].
// An empty result means the current arupa process user.
func (c Config) ServiceRunAsUser(name string) string {
	return c.ServiceSystem.EffectiveService(name).RunAsUser
}

// SHA256Checksum returns the normalized expected package digest. An empty
// Checksum disables verification. The method is shared by configuration
// validation and the service loader so every launch path applies the same
// syntax rules.
func (p Service) SHA256Checksum() (digest string, enabled bool, err error) {
	raw := strings.TrimSpace(p.Checksum)
	if raw == "" {
		return "", false, nil
	}

	algorithm, digest, found := strings.Cut(raw, ":")
	if !found || !strings.EqualFold(algorithm, "sha256") {
		return "", false, fmt.Errorf("must use sha256:<64 hexadecimal digits>")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", false, fmt.Errorf("must use sha256:<64 hexadecimal digits>")
	}
	return strings.ToLower(digest), true, nil
}

// PatchServiceParams updates one service's explicit Params override and persists
// the full config. Defaults remain inherited at EffectiveService read time.
func PatchServiceParams(name string, patch ServiceParamsPatch) (Config, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Config{}, fmt.Errorf("service name is required")
	}
	if name == servicesDefaultKey {
		return Config{}, fmt.Errorf("default service params cannot be patched by a service")
	}

	mu.Lock()
	defer mu.Unlock()

	next := cloneConfig(Conf)
	if next.ServiceSystem.Services == nil {
		next.ServiceSystem.Services = map[string]Service{}
	}

	policy, exists := next.ServiceSystem.Services[name]
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
	next.ServiceSystem.Services[name] = policy

	if err := writeLocked(next); err != nil {
		return Config{}, err
	}
	return next, nil
}

// EffectiveService returns the merged runtime config for name.
//
// [Services.default] is used as a base. Per-service Restart and RunAsUser values
// override the base when non-empty, Allow overrides the default group list
// when configured, and Params are merged with per-service keys taking
// precedence.
func (s ServiceSystem) EffectiveService(name string) Service {
	base := Service{}
	if def, ok := s.Services[servicesDefaultKey]; ok {
		base = cloneService(def)
	}
	if policy, ok := s.Services[name]; ok && name != servicesDefaultKey {
		base = mergeService(base, policy)
	}
	base.Restart = strings.TrimSpace(base.Restart)
	base.RunAsUser = strings.TrimSpace(base.RunAsUser)
	base.Checksum = strings.TrimSpace(base.Checksum)
	if base.Params == nil {
		base.Params = map[string]string{}
	}
	return base
}

// AutoStart reports whether this service config enables automatic startup.
func (p Service) AutoStart() bool {
	return parseRestart(p.Restart)
}

// Clone returns a deep copy of a service config.
func (p Service) Clone() Service {
	return cloneService(p)
}

// ConfiguredServiceNames returns explicit service config names, excluding default.
func (s ServiceSystem) ConfiguredServiceNames() []string {
	out := make([]string, 0, len(s.Services))
	for name := range s.Services {
		if name == servicesDefaultKey {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Clone returns a deep copy of the service-system configuration.
func (s ServiceSystem) Clone() ServiceSystem {
	out := ServiceSystem{
		ServiceDir:     s.ServiceDir,
		ServiceTempDir: s.ServiceTempDir,
		Services:       make(map[string]Service, len(s.Services)),
	}
	for name, policy := range s.Services {
		out.Services[name] = cloneService(policy)
	}
	return out
}

func mergeService(base, override Service) Service {
	if strings.TrimSpace(override.Restart) != "" {
		base.Restart = override.Restart
	}
	if strings.TrimSpace(override.RunAsUser) != "" {
		base.RunAsUser = override.RunAsUser
	}
	if strings.TrimSpace(override.Checksum) != "" {
		base.Checksum = override.Checksum
	}
	if len(override.Allow) > 0 {
		base.Allow = append([]string(nil), override.Allow...)
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

func cloneService(p Service) Service {
	out := Service{
		Restart:   p.Restart,
		RunAsUser: p.RunAsUser,
		Checksum:  p.Checksum,
		Allow:     append([]string(nil), p.Allow...),
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

func parseEnvParamReference(raw string) (name string, optional, isReference bool, err error) {
	if !strings.HasPrefix(raw, envParamPrefix) {
		return "", false, false, nil
	}

	name = strings.TrimPrefix(raw, envParamPrefix)
	if strings.HasSuffix(name, envParamOptional) {
		optional = true
		name = strings.TrimSuffix(name, envParamOptional)
	}
	if name == "" {
		return "", false, true, fmt.Errorf("environment variable name is empty")
	}
	if strings.TrimSpace(name) != name {
		return "", false, true, fmt.Errorf("environment variable name %q contains surrounding whitespace", name)
	}
	return name, optional, true, nil
}
