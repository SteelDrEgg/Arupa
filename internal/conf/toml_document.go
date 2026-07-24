package conf

import (
	"fmt"
	"reflect"

	"github.com/pelletier/go-toml/v2/unstable/edit"
)

// tomlDocument confines the unstable go-toml editing API to this file.
type tomlDocument struct {
	document *edit.Document
}

func parseTOMLDocument(source []byte) (*tomlDocument, error) {
	document, err := edit.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse config document: %w", err)
	}
	return &tomlDocument{document: document}, nil
}

func (d *tomlDocument) apply(operation Operation, path []string) error {
	switch operation.Type {
	case OperationSet:
		value := tomlDocumentValue(operation.Value)
		if err := d.document.Set(path, value); err == nil {
			return nil
		} else if !isContainerValue(operation.Value) || !d.document.Has(path) {
			return err
		}

		// Set deliberately refuses to replace an existing table. A whole-map or
		// whole-struct update has replacement semantics in conf, so delete that
		// table before setting its new value.
		d.document.Delete(path)
		if err := d.document.Set(path, value); err != nil {
			return err
		}
		return nil
	case OperationRemove:
		d.document.Delete(path)
		return nil
	default:
		return fmt.Errorf("unknown operation type %d", operation.Type)
	}
}

func (d *tomlDocument) bytes() []byte {
	return d.document.Bytes()
}

func isContainerValue(value any) bool {
	switch reflect.ValueOf(value).Kind() {
	case reflect.Map, reflect.Struct:
		return true
	default:
		return false
	}
}

func tomlDocumentValue(value any) any {
	switch value := value.(type) {
	case LogConfig:
		out := make(map[string]any, 2)
		if value.Format != "" {
			out[string(LogFieldFormat)] = value.Format
		}
		if value.Level != "" {
			out[string(LogFieldLevel)] = value.Level
		}
		return out
	case RouteConfig:
		out := make(map[string]any, 1)
		if value.Allow != nil {
			out[string(RouteFieldAllow)] = value.Allow
		}
		return out
	case Service:
		return tomlServiceValue(value)
	case map[string]Service:
		out := make(map[string]any, len(value))
		for name, service := range value {
			out[name] = tomlServiceValue(service)
		}
		return out
	default:
		return value
	}
}

func tomlServiceValue(service Service) map[string]any {
	out := make(map[string]any, 5)
	if service.Restart != "" {
		out[string(ServiceFieldRestart)] = service.Restart
	}
	if service.RunAsUser != "" {
		out[string(ServiceFieldRunAsUser)] = service.RunAsUser
	}
	if service.Checksum != "" {
		out[string(ServiceFieldChecksum)] = service.Checksum
	}
	if service.Allow != nil {
		out[string(ServiceFieldAllow)] = service.Allow
	}
	if service.Params != nil {
		out[string(ServiceFieldParams)] = service.Params
	}
	return out
}
