package conf

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
)

type ConfigField string

const (
	ConfigFieldListen         ConfigField = "Listen"
	ConfigFieldTLS            ConfigField = "TLS"
	ConfigFieldLog            ConfigField = "Log"
	ConfigFieldUsers          ConfigField = "Users"
	ConfigFieldGroups         ConfigField = "Groups"
	ConfigFieldRoute          ConfigField = "Route"
	ConfigFieldServiceDir     ConfigField = "ServiceDir"
	ConfigFieldServiceTempDir ConfigField = "ServiceTempDir"
	ConfigFieldServices       ConfigField = "Services"
	ConfigFieldPages          ConfigField = "Pages"
)

type LogField string

const (
	LogFieldFormat LogField = "Format"
	LogFieldLevel  LogField = "Level"
)

type RouteField string

const (
	RouteFieldAllow RouteField = "Allow"
)

type ServiceField string

const (
	ServiceFieldRestart   ServiceField = "Restart"
	ServiceFieldRunAsUser ServiceField = "RunAsUser"
	ServiceFieldChecksum  ServiceField = "Checksum"
	ServiceFieldAllow     ServiceField = "Allow"
	ServiceFieldParams    ServiceField = "Params"
)

type OperationType uint8

const (
	OperationSet OperationType = iota + 1
	OperationRemove
)

type Operation struct {
	Type  OperationType
	Path  string
	Value any
}

func Set(path string, value any) Operation {
	return Operation{Type: OperationSet, Path: path, Value: value}
}

func Remove(path string) Operation {
	return Operation{Type: OperationRemove, Path: path}
}

// JoinPath creates a JSON Pointer from decoded path segments.
func JoinPath(segments ...string) string {
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteByte('/')
		segment = strings.ReplaceAll(segment, "~", "~0")
		segment = strings.ReplaceAll(segment, "/", "~1")
		builder.WriteString(segment)
	}
	return builder.String()
}

// Update applies operations to the latest persistent document in order as one
// serialized transaction. The persistent file is replaced before the edited
// configuration is published in memory.
func Update(operations ...Operation) error {
	configState.mu.Lock()
	defer configState.mu.Unlock()

	source, err := readConfigLocked()
	if err != nil {
		return err
	}
	diskConfig, err := decodeConfig(source)
	if err != nil {
		return err
	}
	document, err := parseTOMLDocument(source)
	if err != nil {
		return err
	}

	base := diskConfig
	if reflect.DeepEqual(configState.current, diskConfig) {
		// Preserve copy-on-write sharing in the normal case where the file has
		// not been edited externally since the last successful transaction.
		base = configState.current
	}
	editor := newEditor(base)
	for index, operation := range operations {
		if operation.Type == OperationSet && isNil(operation.Value) {
			return fmt.Errorf("operation %d: set %s: nil value is not allowed", index, operation.Path)
		}
		segments, err := parsePointer(operation.Path)
		if err != nil {
			return fmt.Errorf("operation %d: %w", index, err)
		}
		if err := editor.apply(operation, segments); err != nil {
			return fmt.Errorf("operation %d: %s %s: %w", index, operationName(operation.Type), operation.Path, err)
		}
		if err := document.apply(operation, segments); err != nil {
			return fmt.Errorf("operation %d: edit %s %s: %w", index, operationName(operation.Type), operation.Path, err)
		}
	}

	if err := validateConfig(editor.next); err != nil {
		return err
	}
	persisted := document.bytes()
	decoded, err := decodeConfig(persisted)
	if err != nil {
		return fmt.Errorf("validate edited config document: %w", err)
	}
	if !reflect.DeepEqual(editor.next, decoded) {
		return fmt.Errorf("edited config document does not match requested update")
	}

	if !bytes.Equal(source, persisted) {
		if err := persistLocked(persisted); err != nil {
			return err
		}
	}
	if reflect.DeepEqual(configState.current, editor.next) {
		return nil
	}
	configState.current = editor.next
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}

func operationName(operationType OperationType) string {
	switch operationType {
	case OperationSet:
		return "set"
	case OperationRemove:
		return "remove"
	default:
		return "unknown"
	}
}

func parsePointer(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("config root cannot be updated")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q must be a JSON Pointer", path)
	}

	raw := strings.Split(path[1:], "/")
	segments := make([]string, len(raw))
	for index, segment := range raw {
		var builder strings.Builder
		for offset := 0; offset < len(segment); offset++ {
			if segment[offset] != '~' {
				builder.WriteByte(segment[offset])
				continue
			}
			if offset+1 >= len(segment) {
				return nil, fmt.Errorf("path %q contains an invalid escape", path)
			}
			offset++
			switch segment[offset] {
			case '0':
				builder.WriteByte('~')
			case '1':
				builder.WriteByte('/')
			default:
				return nil, fmt.Errorf("path %q contains an invalid escape", path)
			}
		}
		segments[index] = builder.String()
	}
	return segments, nil
}

type configEditor struct {
	next Config

	usersOwned      bool
	groupsOwned     bool
	routeAllowOwned bool
	servicesOwned   bool
	pagesOwned      bool
	paramsOwned     map[string]bool
}

func newEditor(current Config) *configEditor {
	return &configEditor{
		next:        current,
		paramsOwned: make(map[string]bool),
	}
}

func (e *configEditor) apply(operation Operation, segments []string) error {
	if len(segments) == 0 || segments[0] == "" {
		return fmt.Errorf("config root cannot be updated")
	}
	switch ConfigField(segments[0]) {
	case ConfigFieldListen:
		return applyString(&e.next.Listen, operation, segments)
	case ConfigFieldTLS:
		return applyBool(&e.next.TLS, operation, segments)
	case ConfigFieldLog:
		return e.applyLog(operation, segments[1:])
	case ConfigFieldUsers:
		return e.applyUsers(operation, segments[1:])
	case ConfigFieldGroups:
		return e.applyGroups(operation, segments[1:])
	case ConfigFieldRoute:
		return e.applyRoute(operation, segments[1:])
	case ConfigFieldServiceDir:
		return applyString(&e.next.ServiceSystem.ServiceDir, operation, segments)
	case ConfigFieldServiceTempDir:
		return applyString(&e.next.ServiceSystem.ServiceTempDir, operation, segments)
	case ConfigFieldServices:
		return e.applyServices(operation, segments[1:])
	case ConfigFieldPages:
		return e.applyPages(operation, segments[1:])
	default:
		return fmt.Errorf("unknown or incorrectly cased field %q", segments[0])
	}
}

func applyString(target *string, operation Operation, fullPath []string) error {
	if len(fullPath) != 1 {
		return fmt.Errorf("%q is a scalar field", fullPath[0])
	}
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", operation.Value)
		}
		*target = value
	case OperationRemove:
		*target = ""
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func applyBool(target *bool, operation Operation, fullPath []string) error {
	if len(fullPath) != 1 {
		return fmt.Errorf("%q is a scalar field", fullPath[0])
	}
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.(bool)
		if !ok {
			return fmt.Errorf("expected bool, got %T", operation.Value)
		}
		*target = value
	case OperationRemove:
		*target = false
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func (e *configEditor) applyLog(operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(LogConfig)
			if !ok {
				return fmt.Errorf("expected conf.LogConfig, got %T", operation.Value)
			}
			e.next.Log = value
		case OperationRemove:
			e.next.Log = LogConfig{}
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		return nil
	}
	if len(segments) != 1 {
		return fmt.Errorf("Log.%s is not a container", segments[0])
	}
	switch LogField(segments[0]) {
	case LogFieldFormat:
		return applyString(&e.next.Log.Format, operation, []string{segments[0]})
	case LogFieldLevel:
		return applyString(&e.next.Log.Level, operation, []string{segments[0]})
	default:
		return fmt.Errorf("unknown or incorrectly cased Log field %q", segments[0])
	}
}

func (e *configEditor) applyUsers(operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(map[string]string)
			if !ok {
				return fmt.Errorf("expected map[string]string, got %T", operation.Value)
			}
			e.next.Auth.Users = cloneStrings(value)
		case OperationRemove:
			e.next.Auth.Users = nil
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.usersOwned = true
		return nil
	}
	if len(segments) != 1 || segments[0] == "" {
		return fmt.Errorf("Users requires one non-empty username")
	}
	if operation.Type == OperationRemove {
		if _, exists := e.next.Auth.Users[segments[0]]; !exists {
			return nil
		}
	}
	e.ensureUsers()
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", operation.Value)
		}
		e.next.Auth.Users[segments[0]] = value
	case OperationRemove:
		delete(e.next.Auth.Users, segments[0])
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func (e *configEditor) applyGroups(operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(map[string][]string)
			if !ok {
				return fmt.Errorf("expected map[string][]string, got %T", operation.Value)
			}
			e.next.Auth.Groups = cloneStringSlices(value)
		case OperationRemove:
			e.next.Auth.Groups = nil
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.groupsOwned = true
		return nil
	}
	if len(segments) != 1 || segments[0] == "" {
		return fmt.Errorf("Groups requires one non-empty group name")
	}
	if operation.Type == OperationRemove {
		if _, exists := e.next.Auth.Groups[segments[0]]; !exists {
			return nil
		}
	}
	e.ensureGroups()
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.([]string)
		if !ok {
			return fmt.Errorf("expected []string, got %T", operation.Value)
		}
		e.next.Auth.Groups[segments[0]] = cloneSlice(value)
	case OperationRemove:
		delete(e.next.Auth.Groups, segments[0])
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func (e *configEditor) applyRoute(operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(RouteConfig)
			if !ok {
				return fmt.Errorf("expected conf.RouteConfig, got %T", operation.Value)
			}
			e.next.Route = RouteConfig{Allow: cloneStringSlices(value.Allow)}
		case OperationRemove:
			e.next.Route = RouteConfig{}
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.routeAllowOwned = true
		return nil
	}
	if RouteField(segments[0]) != RouteFieldAllow {
		return fmt.Errorf("unknown or incorrectly cased Route field %q", segments[0])
	}
	if len(segments) == 1 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(map[string][]string)
			if !ok {
				return fmt.Errorf("expected map[string][]string, got %T", operation.Value)
			}
			e.next.Route.Allow = cloneStringSlices(value)
		case OperationRemove:
			e.next.Route.Allow = nil
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.routeAllowOwned = true
		return nil
	}
	if len(segments) != 2 || segments[1] == "" {
		return fmt.Errorf("Route.Allow requires one non-empty path pattern")
	}
	if operation.Type == OperationRemove {
		if _, exists := e.next.Route.Allow[segments[1]]; !exists {
			return nil
		}
	}
	e.ensureRouteAllow()
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.([]string)
		if !ok {
			return fmt.Errorf("expected []string, got %T", operation.Value)
		}
		e.next.Route.Allow[segments[1]] = cloneSlice(value)
	case OperationRemove:
		delete(e.next.Route.Allow, segments[1])
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func (e *configEditor) applyServices(operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(map[string]Service)
			if !ok {
				return fmt.Errorf("expected map[string]conf.Service, got %T", operation.Value)
			}
			e.next.ServiceSystem.Services = cloneServices(value)
			e.paramsOwned = make(map[string]bool, len(value))
			for name := range value {
				e.paramsOwned[name] = true
			}
		case OperationRemove:
			e.next.ServiceSystem.Services = nil
			e.paramsOwned = make(map[string]bool)
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.servicesOwned = true
		return nil
	}
	name := segments[0]
	if name == "" {
		return fmt.Errorf("Services requires a non-empty service name")
	}
	if len(segments) == 1 {
		if operation.Type == OperationRemove {
			if _, exists := e.next.ServiceSystem.Services[name]; !exists {
				return nil
			}
		}
		e.ensureServices()
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(Service)
			if !ok {
				return fmt.Errorf("expected conf.Service, got %T", operation.Value)
			}
			e.next.ServiceSystem.Services[name] = cloneService(value)
			e.paramsOwned[name] = true
		case OperationRemove:
			delete(e.next.ServiceSystem.Services, name)
			delete(e.paramsOwned, name)
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		return nil
	}
	return e.applyServiceField(name, operation, segments[1:])
}

func (e *configEditor) applyServiceField(name string, operation Operation, segments []string) error {
	if len(segments) == 0 {
		return fmt.Errorf("service field is required")
	}
	if operation.Type == OperationRemove {
		if _, exists := e.next.ServiceSystem.Services[name]; !exists {
			return nil
		}
	}
	e.ensureServices()
	service := e.next.ServiceSystem.Services[name]
	switch ServiceField(segments[0]) {
	case ServiceFieldRestart:
		if len(segments) != 1 {
			return fmt.Errorf("Restart is a scalar field")
		}
		if err := applyString(&service.Restart, operation, segments); err != nil {
			return err
		}
	case ServiceFieldRunAsUser:
		if len(segments) != 1 {
			return fmt.Errorf("RunAsUser is a scalar field")
		}
		if err := applyString(&service.RunAsUser, operation, segments); err != nil {
			return err
		}
	case ServiceFieldChecksum:
		if len(segments) != 1 {
			return fmt.Errorf("Checksum is a scalar field")
		}
		if err := applyString(&service.Checksum, operation, segments); err != nil {
			return err
		}
	case ServiceFieldAllow:
		if len(segments) != 1 {
			return fmt.Errorf("Allow must be replaced as a complete array")
		}
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.([]string)
			if !ok {
				return fmt.Errorf("expected []string, got %T", operation.Value)
			}
			service.Allow = cloneSlice(value)
		case OperationRemove:
			service.Allow = nil
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
	case ServiceFieldParams:
		if err := e.applyParams(name, &service, operation, segments[1:]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown or incorrectly cased Service field %q", segments[0])
	}
	e.next.ServiceSystem.Services[name] = service
	return nil
}

func (e *configEditor) applyParams(name string, service *Service, operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(map[string]string)
			if !ok {
				return fmt.Errorf("expected map[string]string, got %T", operation.Value)
			}
			service.Params = cloneStrings(value)
		case OperationRemove:
			service.Params = nil
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.paramsOwned[name] = true
		return nil
	}
	if len(segments) != 1 || segments[0] == "" {
		return fmt.Errorf("Params requires one non-empty key")
	}
	if operation.Type == OperationRemove {
		if _, exists := service.Params[segments[0]]; !exists {
			return nil
		}
	}
	if !e.paramsOwned[name] {
		service.Params = cloneStrings(service.Params)
		e.paramsOwned[name] = true
	}
	if service.Params == nil {
		service.Params = make(map[string]string)
	}
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", operation.Value)
		}
		service.Params[segments[0]] = value
	case OperationRemove:
		delete(service.Params, segments[0])
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func (e *configEditor) applyPages(operation Operation, segments []string) error {
	if len(segments) == 0 {
		switch operation.Type {
		case OperationSet:
			value, ok := operation.Value.(map[string]string)
			if !ok {
				return fmt.Errorf("expected map[string]string, got %T", operation.Value)
			}
			e.next.Pages = cloneStrings(value)
		case OperationRemove:
			e.next.Pages = nil
		default:
			return fmt.Errorf("unknown operation type %d", operation.Type)
		}
		e.pagesOwned = true
		return nil
	}
	if len(segments) != 1 || segments[0] == "" {
		return fmt.Errorf("Pages requires one non-empty status code")
	}
	if operation.Type == OperationRemove {
		if _, exists := e.next.Pages[segments[0]]; !exists {
			return nil
		}
	}
	e.ensurePages()
	switch operation.Type {
	case OperationSet:
		value, ok := operation.Value.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", operation.Value)
		}
		e.next.Pages[segments[0]] = value
	case OperationRemove:
		delete(e.next.Pages, segments[0])
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
	return nil
}

func (e *configEditor) ensureUsers() {
	if !e.usersOwned {
		e.next.Auth.Users = cloneStrings(e.next.Auth.Users)
		e.usersOwned = true
	}
	if e.next.Auth.Users == nil {
		e.next.Auth.Users = make(map[string]string)
	}
}

func (e *configEditor) ensureGroups() {
	if !e.groupsOwned {
		e.next.Auth.Groups = cloneStringSlices(e.next.Auth.Groups)
		e.groupsOwned = true
	}
	if e.next.Auth.Groups == nil {
		e.next.Auth.Groups = make(map[string][]string)
	}
}

func (e *configEditor) ensureRouteAllow() {
	if !e.routeAllowOwned {
		e.next.Route.Allow = cloneStringSlices(e.next.Route.Allow)
		e.routeAllowOwned = true
	}
	if e.next.Route.Allow == nil {
		e.next.Route.Allow = make(map[string][]string)
	}
}

func (e *configEditor) ensureServices() {
	if !e.servicesOwned {
		e.next.ServiceSystem.Services = cloneServicesShallow(e.next.ServiceSystem.Services)
		e.servicesOwned = true
	}
	if e.next.ServiceSystem.Services == nil {
		e.next.ServiceSystem.Services = make(map[string]Service)
	}
}

func (e *configEditor) ensurePages() {
	if !e.pagesOwned {
		e.next.Pages = cloneStrings(e.next.Pages)
		e.pagesOwned = true
	}
	if e.next.Pages == nil {
		e.next.Pages = make(map[string]string)
	}
}

func cloneServices(values map[string]Service) map[string]Service {
	if values == nil {
		return nil
	}
	out := make(map[string]Service, len(values))
	for name, service := range values {
		out[name] = cloneService(service)
	}
	return out
}

func cloneServicesShallow(values map[string]Service) map[string]Service {
	if values == nil {
		return nil
	}
	out := make(map[string]Service, len(values))
	for name, service := range values {
		out[name] = service
	}
	return out
}
