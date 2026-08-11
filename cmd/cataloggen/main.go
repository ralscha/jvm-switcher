package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"jvm-switcher/internal/catalog"
	"jvm-switcher/internal/cataloggen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cataloggen:", err)
		os.Exit(1)
	}
}

func run() error {
	output := flag.String("output", "catalog.json", "generated catalog path")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	httpClient := &http.Client{Timeout: 45 * time.Second}
	document, err := cataloggen.New(httpClient).Generate(ctx)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	content = append(content, '\n')

	temporary, err := os.CreateTemp("", "jvm-switcher-catalog-*.json")
	if err != nil {
		return fmt.Errorf("create validation file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(content); err != nil {
		return errors.Join(fmt.Errorf("write validation file: %w", err), temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close validation file: %w", err)
	}
	if err := catalog.New(httpClient, temporaryPath).Validate(ctx); err != nil {
		return fmt.Errorf("validate generated catalog: %w", err)
	}
	if directory := filepath.Dir(*output); directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := os.WriteFile(*output, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", *output, err)
	}
	fmt.Printf("wrote %d releases to %s\n", len(document.Releases), *output)
	return nil
}
