package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogFromConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"catalog":"catalog.json"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	configuration, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := configuration.Catalog, filepath.Join(root, "catalog.json"); got != want {
		t.Fatalf("catalog = %q, want %q", got, want)
	}
}

func TestEnvironmentCatalogOverridesConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"catalog":"ignored.json"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(CatalogEnvironmentVariable, "https://raw.githubusercontent.com/example/jdks/main/catalog.json")
	configuration, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := configuration.Catalog, "https://raw.githubusercontent.com/example/jdks/main/catalog.json"; got != want {
		t.Fatalf("catalog = %q, want %q", got, want)
	}
}
