package conf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// envParamPrefix marks a Params value as an environment variable reference.
	// The optional suffix is deliberately explicit so a missing variable cannot
	// silently turn a required setting into an empty value.
	envParamPrefix   = "env://"
	envParamOptional = "?"
)

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

// AutoStart reports whether this service config enables automatic startup.
func (p Service) AutoStart() bool {
	return parseRestart(p.Restart)
}

// Clone returns a deep copy of a service config.
func (p Service) Clone() Service {
	return cloneService(p)
}

func cloneService(p Service) Service {
	out := Service{
		Restart:   p.Restart,
		RunAsUser: p.RunAsUser,
		Checksum:  p.Checksum,
		Allow:     cloneSlice(p.Allow),
	}
	if p.Params != nil {
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
