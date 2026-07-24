package service

import (
	"strings"

	"arupa/internal/auth"
	"arupa/internal/conf"
)

const defaultServiceName = "default"

func currentServiceConfig(name string) conf.Service {
	current, _ := currentServiceOperationConfig(name)
	return current
}

func currentServiceOperationConfig(name string) (conf.Service, string) {
	_, tempDir, services, found := conf.GetServiceSettings(defaultServiceName, name)
	current := conf.Service{}
	if found[0] {
		current = services[0]
	}
	if name != defaultServiceName && found[1] {
		current = mergeServiceConfig(current, services[1])
	}
	current.Restart = strings.TrimSpace(current.Restart)
	current.RunAsUser = strings.TrimSpace(current.RunAsUser)
	current.Checksum = strings.TrimSpace(current.Checksum)
	if current.Params == nil {
		current.Params = map[string]string{}
	}
	return current, tempDir
}

func currentServiceAccess(name string) auth.AccessPolicy {
	allows := conf.GetServiceAllows(defaultServiceName, name)
	groups := allows[0]
	if name != defaultServiceName && allows[1] != nil {
		groups = allows[1]
	}
	return auth.AccessPolicy{Groups: groups}
}

func configuredServiceNames() []string {
	names := conf.GetServiceNames()
	out := names[:0]
	for _, name := range names {
		if name != defaultServiceName {
			out = append(out, name)
		}
	}
	return out
}

func mergeServiceConfig(base, override conf.Service) conf.Service {
	if strings.TrimSpace(override.Restart) != "" {
		base.Restart = override.Restart
	}
	if strings.TrimSpace(override.RunAsUser) != "" {
		base.RunAsUser = override.RunAsUser
	}
	if strings.TrimSpace(override.Checksum) != "" {
		base.Checksum = override.Checksum
	}
	if override.Allow != nil {
		base.Allow = append([]string{}, override.Allow...)
	}
	if len(override.Params) > 0 {
		if base.Params == nil {
			base.Params = make(map[string]string)
		}
		for key, value := range override.Params {
			base.Params[key] = value
		}
	}
	return base
}
