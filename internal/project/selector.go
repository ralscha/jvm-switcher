package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrNotFound = errors.New("no .jvm-switcher or .java-version file found")

var selectorFiles = []string{".jvm-switcher", ".java-version"}

func Find(start string) (string, string, error) {
	directory, err := filepath.Abs(start)
	if err != nil {
		return "", "", fmt.Errorf("resolve working directory: %w", err)
	}
	for {
		for _, name := range selectorFiles {
			path := filepath.Join(directory, name)
			selector, err := read(path)
			if err == nil {
				return selector, path, nil
			}
			if !os.IsNotExist(err) {
				return "", "", err
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", "", ErrNotFound
		}
		directory = parent
	}
}

func read(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(content))
	if len(fields) != 1 {
		return "", fmt.Errorf("project selector %q must contain exactly one JDK selector", path)
	}
	return fields[0], nil
}
