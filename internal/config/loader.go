package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

func Load(path string) (*Roster, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read roster file: %w", err)
	}

	var roster Roster
	metadata, err := toml.Decode(string(content), &roster)
	if err != nil {
		return nil, fmt.Errorf("failed to parse TOML: %w", err)
	}

	// Check for unknown keys
	if len(metadata.Undecoded()) > 0 {
		var keys []string
		for _, k := range metadata.Undecoded() {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("unknown keys in TOML: %v", keys)
	}

	return &roster, nil
}
