package service

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

	goservice "github.com/SteelDrEgg/go-plugin"
	"gopkg.in/yaml.v3"
)

// DiscoveredService is metadata scanned from a .plg package's info.yaml without
// loading the service runtime.
type DiscoveredService struct {
	Name            string
	Version         string
	Type            string
	ContractVersion int
	Command         string
	Metadata        map[string]any
	PackagePath     string
}

type serviceCatalog struct {
	kv  *KV
	log *slog.Logger
}

func newServiceCatalog(kv *KV, log *slog.Logger) *serviceCatalog {
	if log == nil {
		log = slog.Default()
	}
	return &serviceCatalog{kv: kv, log: log}
}

func (c *serviceCatalog) discover(dir string) ([]DiscoveredService, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			c.log.Warn("service directory does not exist; skipping", "dir", dir)
			return nil, nil
		}
		return nil, fmt.Errorf("read service dir: %w", err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".plg" {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)

	seen := make(map[string]struct{}, len(paths))
	out := make([]DiscoveredService, 0, len(paths))
	for _, p := range paths {
		info, err := readServiceInfo(p)
		if err != nil {
			c.log.Error("failed to scan service package", "path", p, "err", err)
			continue
		}
		if _, exists := seen[info.Name]; exists {
			c.log.Error("duplicate service name found in packages; keeping first", "name", info.Name, "path", p)
			continue
		}
		seen[info.Name] = struct{}{}
		out = append(out, info)
	}
	return out, nil
}

func (c *serviceCatalog) publish(d DiscoveredService) {
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	c.kv.SystemSet(SysNamespace, registryKVPrefix+"catalog/"+d.Name, b)
}

func (c *serviceCatalog) unpublish(name string) {
	c.kv.SystemDelete(SysNamespace, registryKVPrefix+"catalog/"+name)
}

func readServiceInfo(path string) (DiscoveredService, error) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveredService{}, fmt.Errorf("open service package: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return DiscoveredService{}, fmt.Errorf("stat service package: %w", err)
	}

	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return DiscoveredService{}, fmt.Errorf("read zip service package: %w", err)
	}

	var info goservice.Info
	for _, zf := range zr.File {
		if filepath.Clean(zf.Name) != "info.yaml" {
			continue
		}
		r, err := zf.Open()
		if err != nil {
			return DiscoveredService{}, fmt.Errorf("open info.yaml: %w", err)
		}
		b, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return DiscoveredService{}, fmt.Errorf("read info.yaml: %w", err)
		}
		if err := yaml.Unmarshal(b, &info); err != nil {
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
	if info.ContractVersion != ContractVersion {
		return DiscoveredService{}, fmt.Errorf("info.yaml ContractVersion must be %d", ContractVersion)
	}
	if info.Type != "static" && strings.TrimSpace(info.Command) == "" {
		return DiscoveredService{}, fmt.Errorf("info.yaml Command is required")
	}

	return DiscoveredService{
		Name:            info.Name,
		Version:         info.Version,
		Type:            info.Type,
		ContractVersion: info.ContractVersion,
		Command:         info.Command,
		Metadata:        info.Metadata,
		PackagePath:     path,
	}, nil
}
