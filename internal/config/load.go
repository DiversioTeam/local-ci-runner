package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

func Load(path string) (File, error) {
	var cfg File

	absPath, err := filepath.Abs(path)
	if err != nil {
		return File{}, fmt.Errorf("resolve config path: %w", err)
	}

	meta, err := toml.DecodeFile(absPath, &cfg)
	if err != nil {
		return File{}, fmt.Errorf("decode %s: %w", absPath, err)
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, item := range undecoded {
			keys = append(keys, item.String())
		}
		return File{}, fmt.Errorf("unknown fields in %s: %s", absPath, strings.Join(keys, ", "))
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return File{}, fmt.Errorf("validate %s: %w", absPath, err)
	}

	return cfg, nil
}
