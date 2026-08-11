package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const CatalogEnvironmentVariable = "JVM_SWITCHER_CATALOG"

type Config struct {
	Catalog string `json:"catalog"`
}

func Load(root string) (Config, error) {
	if source := strings.TrimSpace(os.Getenv(CatalogEnvironmentVariable)); source != "" {
		return Config{Catalog: source}, nil
	}
	configPath := filepath.Join(root, "config.json")
	file, err := os.Open(configPath)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open configuration %q: %w", configPath, err)
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var configuration Config
	if err := decoder.Decode(&configuration); err != nil {
		return Config{}, fmt.Errorf("decode configuration %q: %w", configPath, err)
	}
	configuration.Catalog = strings.TrimSpace(configuration.Catalog)
	if configuration.Catalog != "" && !isHTTPURL(configuration.Catalog) && !filepath.IsAbs(configuration.Catalog) {
		configuration.Catalog = filepath.Join(root, configuration.Catalog)
	}
	return configuration, nil
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
