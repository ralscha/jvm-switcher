package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindWalksUpAndPrefersDedicatedFile(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "src", "main")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".java-version"), []byte("temurin@17\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dedicated := filepath.Join(root, ".jvm-switcher")
	if err := os.WriteFile(dedicated, []byte("corretto@21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	selector, path, err := Find(nested)
	if err != nil {
		t.Fatal(err)
	}
	if selector != "corretto@21" {
		t.Fatalf("selector = %q, want %q", selector, "corretto@21")
	}
	if path != dedicated {
		t.Fatalf("path = %q, want %q", path, dedicated)
	}
}

func TestFindRejectsInvalidFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".java-version")
	if err := os.WriteFile(path, []byte("temurin@17 temurin@21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Find(root); err == nil {
		t.Fatal("Find() succeeded for a multi-token selector")
	}
}

func TestFindReturnsNotFound(t *testing.T) {
	if _, _, err := Find(t.TempDir()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find() error = %v, want ErrNotFound", err)
	}
}
