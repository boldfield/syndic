package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable not set: env:%s", name)
	}
	return value, nil
}

func resolveFile(dir string, name string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		return "", fmt.Errorf("secret file name contains path separator: file:%s", name)
	}

	filePath := filepath.Join(dir, name)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read secret file: file:%s: %w", name, err)
	}

	// Trim one trailing newline
	result := strings.TrimSuffix(string(content), "\n")

	return result, nil
}
