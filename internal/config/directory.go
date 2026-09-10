package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LoadDirectory loads optional tools.toml first, followed by lexically sorted
// tools.d/*.toml. Complete tool definitions are concatenated, never overridden.
// A directory containing only modular manifests is supported.
func LoadDirectory(dir string) (*Config, error) {
	files := []string{}
	main := filepath.Join(dir, "tools.toml")
	if fi, err := os.Stat(main); err == nil {
		if !fi.IsDir() {
			files = append(files, main)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tools.d"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".toml" {
			files = append(files, filepath.Join(dir, "tools.d", e.Name()))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no manifests found (expected tools.toml or tools.d/*.toml)", dir)
	}
	merged := &Config{}
	sources := map[string]string{}
	var errs []error
	for _, path := range files {
		cfg, err := Load(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, tool := range cfg.Tools {
			if prev, ok := sources[tool.ID]; ok {
				errs = append(errs, fmt.Errorf("duplicate tool id %q in %s and %s", tool.ID, prev, path))
				continue
			}
			sources[tool.ID] = path
			merged.Tools = append(merged.Tools, tool)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return merged, nil
}
