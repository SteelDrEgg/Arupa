package runner

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"arupa/internal/auth"
	"arupa/internal/conf"
	"arupa/internal/service/catalog"
	"arupa/internal/service/instance"
	"arupa/internal/service/spec"

	"gopkg.in/yaml.v3"
)

const manifestVersion = 1

type Manifest struct {
	Version    int              `yaml:"version"`
	Transports []spec.Transport `yaml:"transports"`
	Routes     []spec.Route     `yaml:"routes"`
}

func (l *Loader) loadStatic(
	scanned catalog.DiscoveredService,
	cfg conf.Service,
	tempDir string,
	access func() auth.AccessPolicy,
) (*LoadResult, error) {
	if err := verifyPackageChecksum(scanned.PackagePath, cfg); err != nil {
		return nil, fmt.Errorf("verify service %q package checksum: %w", scanned.Name, err)
	}
	root, err := extractStaticPackage(tempDir, scanned.PackagePath)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	contentRoot := filepath.Join(root, "Content")
	manifest, err := readManifest(filepath.Join(root, "manifest.yaml"))
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err := os.Stat(contentRoot); err != nil {
		cleanup()
		return nil, fmt.Errorf("static service Content directory: %w", err)
	}

	loaded := instance.New(instance.Options{
		Record: &spec.ServiceRecord{
			InstanceID: scanned.Name, Name: scanned.Name, Version: scanned.Version,
			Type: scanned.Type, Path: scanned.PackagePath,
		},
		Access:      access,
		CleanupDirs: []string{root},
	})
	if err := l.bindings.Attach(scanned.Name, loaded, contentRoot, nil); err != nil {
		loaded.Cancel()
		cleanup()
		return nil, err
	}

	for _, transport := range manifest.Transports {
		if err := l.bindings.RegisterTransport(scanned.Name, transport); err != nil {
			loaded.MarkDegraded()
			continue
		}
	}
	result := l.bindings.RegisterRoutes(scanned.Name, manifest.Routes)
	if result.Degraded {
		loaded.MarkDegraded()
	}
	return &LoadResult{Loaded: loaded, RootPath: contentRoot}, nil
}

func readManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest.yaml: %w", err)
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest.yaml: %w", err)
	}
	if manifest.Version != manifestVersion {
		return Manifest{}, fmt.Errorf("manifest.yaml version %d is unsupported", manifest.Version)
	}
	return manifest, nil
}

func extractStaticPackage(tempDir, packagePath string) (string, error) {
	archive, err := zip.OpenReader(packagePath)
	if err != nil {
		return "", fmt.Errorf("open static service package: %w", err)
	}
	defer archive.Close()
	root, err := os.MkdirTemp(tempDir, "plg-")
	if err != nil {
		return "", fmt.Errorf("create static service temp directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	for _, entry := range archive.File {
		clean := filepath.Clean(entry.Name)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("unsafe archive path %q", entry.Name)
		}
		target := filepath.Join(root, clean)
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("archive path %q escapes extraction root", entry.Name)
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("archive symlink %q is not allowed", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				_ = os.RemoveAll(root)
				return "", err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("archive entry %q is not a regular file", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = os.RemoveAll(root)
			return "", err
		}
		source, err := entry.Open()
		if err != nil {
			_ = os.RemoveAll(root)
			return "", err
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = source.Close()
			_ = os.RemoveAll(root)
			return "", err
		}
		_, copyErr := io.Copy(destination, source)
		closeErr := destination.Close()
		_ = source.Close()
		if copyErr != nil {
			_ = os.RemoveAll(root)
			return "", copyErr
		}
		if closeErr != nil {
			_ = os.RemoveAll(root)
			return "", closeErr
		}
	}
	return root, nil
}
