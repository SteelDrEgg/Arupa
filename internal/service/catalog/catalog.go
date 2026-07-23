// Package catalog discovers and validates service bundles without starting
// their runtime.
package catalog

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"arupa/internal/service/spec"

	goservice "github.com/SteelDrEgg/go-plugin"
	"gopkg.in/yaml.v3"
)

const catalogKVPrefix = "service/catalog/"

type SystemStore interface {
	SystemSet(ns, key string, value []byte)
	SystemDelete(ns, key string)
}

// DiscoveredService is metadata read from info.yaml without executing service
// code.
type DiscoveredService struct {
	Name            string
	Version         string
	Type            string
	ContractVersion int
	Command         string
	Metadata        map[string]any
	PackagePath     string
}

type Catalog struct {
	kv  SystemStore
	log *slog.Logger
}

func New(kv SystemStore, log *slog.Logger) *Catalog {
	if log == nil {
		log = slog.Default()
	}
	return &Catalog{kv: kv, log: log}
}

func (c *Catalog) Discover(dir string) ([]DiscoveredService, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.log.Warn("service directory does not exist; skipping", "dir", dir)
			return nil, nil
		}
		return nil, fmt.Errorf("read service dir: %w", err)
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".plg" {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)

	seen := make(map[string]struct{}, len(paths))
	out := make([]DiscoveredService, 0, len(paths))
	for _, path := range paths {
		info, err := ReadInfo(path)
		if err != nil {
			c.log.Error("failed to scan service package", "path", path, "err", err)
			continue
		}
		if _, exists := seen[info.Name]; exists {
			c.log.Error("duplicate service name found in packages; keeping first", "name", info.Name, "path", path)
			continue
		}
		seen[info.Name] = struct{}{}
		out = append(out, info)
	}
	return out, nil
}

func (c *Catalog) Publish(info DiscoveredService) {
	data, err := json.Marshal(info)
	if err != nil {
		return
	}
	c.kv.SystemSet("sys", catalogKVPrefix+info.Name, data)
}

func (c *Catalog) Unpublish(name string) {
	c.kv.SystemDelete("sys", catalogKVPrefix+name)
}

func ReadInfo(path string) (DiscoveredService, error) {
	file, err := os.Open(path)
	if err != nil {
		return DiscoveredService{}, fmt.Errorf("open service package: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return DiscoveredService{}, fmt.Errorf("stat service package: %w", err)
	}
	archive, err := zip.NewReader(file, stat.Size())
	if err != nil {
		return DiscoveredService{}, fmt.Errorf("read zip service package: %w", err)
	}

	var info goservice.Info
	for _, entry := range archive.File {
		if filepath.Clean(entry.Name) != "info.yaml" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return DiscoveredService{}, fmt.Errorf("open info.yaml: %w", err)
		}
		data, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return DiscoveredService{}, fmt.Errorf("read info.yaml: %w", err)
		}
		if err := yaml.Unmarshal(data, &info); err != nil {
			return DiscoveredService{}, fmt.Errorf("parse info.yaml: %w", err)
		}
		break
	}

	if strings.TrimSpace(info.Name) == "" {
		return DiscoveredService{}, fmt.Errorf("info.yaml Name is required")
	}
	if strings.TrimSpace(info.Version) == "" {
		return DiscoveredService{}, fmt.Errorf("info.yaml Version is required")
	}
	if info.Type != "grpc" && info.Type != "wasm" && info.Type != "static" {
		return DiscoveredService{}, fmt.Errorf("info.yaml Type must be static, grpc or wasm")
	}
	if info.ContractVersion != spec.ContractVersion {
		return DiscoveredService{}, fmt.Errorf("info.yaml ContractVersion must be %d", spec.ContractVersion)
	}
	if info.Type != "static" && strings.TrimSpace(info.Command) == "" {
		return DiscoveredService{}, fmt.Errorf("info.yaml Command is required")
	}

	return DiscoveredService{
		Name: info.Name, Version: info.Version, Type: info.Type,
		ContractVersion: info.ContractVersion, Command: info.Command,
		Metadata: info.Metadata, PackagePath: path,
	}, nil
}
