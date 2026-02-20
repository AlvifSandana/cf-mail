package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config yaml: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp config file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("replace config file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod config file: %w", err)
	}
	if dirFD, err := os.Open(dir); err == nil {
		_ = dirFD.Sync()
		_ = dirFD.Close()
	}
	return nil
}
