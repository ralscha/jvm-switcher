package store

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"jvm-switcher/internal/archive"
	"jvm-switcher/internal/jdk"
)

const homeEnvironmentVariable = "JVM_SWITCHER_HOME"
const metadataFileName = ".jvm-switcher.json"

type installationMetadata struct {
	Distribution string `json:"distribution"`
	Version      string `json:"version"`
}

type Store struct {
	root string
}

func DefaultRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv(homeEnvironmentVariable)); root != "" {
		return filepath.Abs(root)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home: %w", err)
	}
	return filepath.Join(home, ".jvm-switcher"), nil
}

func New(root string) *Store {
	return &Store{root: root}
}

func (store *Store) Root() string {
	return store.root
}

func (store *Store) List() ([]jdk.Installed, error) {
	entries, err := os.ReadDir(store.jdksPath())
	if os.IsNotExist(err) {
		return []jdk.Installed{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list installed JDKs: %w", err)
	}
	active, hasActive := store.currentTarget()
	installed := make([]jdk.Installed, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validID(entry.Name()) {
			continue
		}
		path := filepath.Join(store.jdksPath(), entry.Name())
		installation := readInstallation(path, entry.Name())
		installation.Path = path
		if hasActive {
			info, err := os.Stat(path)
			installation.Active = err == nil && os.SameFile(info, active)
		}
		installed = append(installed, installation)
	}
	sort.Slice(installed, func(left, right int) bool {
		comparison := jdk.CompareVersions(installed[left].Version, installed[right].Version)
		if comparison != 0 {
			return comparison > 0
		}
		return installed[left].ID() < installed[right].ID()
	})
	return installed, nil
}

func (store *Store) InstallArchive(release jdk.Remote, archivePath string) error {
	id := release.ID()
	if !validID(id) {
		return fmt.Errorf("invalid JDK identifier %q", id)
	}
	if err := os.MkdirAll(store.jdksPath(), 0o755); err != nil {
		return fmt.Errorf("create JDK store: %w", err)
	}
	target := filepath.Join(store.jdksPath(), id)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("JDK %s is already installed", id)
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary, err := os.MkdirTemp(store.root, "install-*")
	if err != nil {
		return fmt.Errorf("create install directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	extracted := filepath.Join(temporary, "extracted")
	if err := archive.Extract(archivePath, extracted); err != nil {
		return fmt.Errorf("extract JDK %s: %w", id, err)
	}
	jdkHome, err := findJDKHome(extracted)
	if err != nil {
		return fmt.Errorf("extract JDK %s: %w", id, err)
	}
	if err := writeInstallation(jdkHome, release); err != nil {
		return fmt.Errorf("record JDK %s metadata: %w", id, err)
	}
	if err := os.Rename(jdkHome, target); err != nil {
		return fmt.Errorf("install JDK %s: %w", id, err)
	}
	return nil
}

func (store *Store) Switch(selector string) (jdk.Installed, error) {
	installed, err := store.Resolve(selector)
	if err != nil {
		return jdk.Installed{}, err
	}
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return jdk.Installed{}, err
	}
	temporaryLink := store.currentPath() + ".new"
	if err := removeLink(temporaryLink); err != nil {
		return jdk.Installed{}, err
	}
	if err := createDirectoryLink(temporaryLink, installed.Path); err != nil {
		return jdk.Installed{}, fmt.Errorf("create current JDK link: %w", err)
	}
	if err := removeLink(store.currentPath()); err != nil {
		_ = removeLink(temporaryLink)
		return jdk.Installed{}, err
	}
	if err := os.Rename(temporaryLink, store.currentPath()); err != nil {
		_ = removeLink(temporaryLink)
		return jdk.Installed{}, fmt.Errorf("activate JDK %s: %w", installed.Version, err)
	}
	// Older releases tracked the active JDK in this file; the current link is now the only source of truth.
	_ = os.Remove(filepath.Join(store.root, "active"))
	installed.Active = true
	return installed, nil
}

func (store *Store) Remove(selector string) (jdk.Installed, error) {
	installed, err := store.Resolve(selector)
	if err != nil {
		return jdk.Installed{}, err
	}
	if installed.Active {
		return jdk.Installed{}, fmt.Errorf("cannot remove active JDK %s; switch to another version first", installed.ID())
	}
	if err := os.RemoveAll(installed.Path); err != nil {
		return jdk.Installed{}, fmt.Errorf("remove JDK %s: %w", installed.ID(), err)
	}
	return installed, nil
}

func (store *Store) Resolve(selector string) (jdk.Installed, error) {
	installed, err := store.List()
	if err != nil {
		return jdk.Installed{}, err
	}
	for _, candidate := range installed {
		if candidate.ID() == selector {
			return candidate, nil
		}
	}
	var versionMatches []jdk.Installed
	for _, candidate := range installed {
		if candidate.Version == selector {
			versionMatches = append(versionMatches, candidate)
		}
	}
	if len(versionMatches) == 1 {
		return versionMatches[0], nil
	}
	if len(versionMatches) > 1 {
		return jdk.Installed{}, fmt.Errorf("JDK version %q is ambiguous; use a distribution@version identifier", selector)
	}
	if distribution, feature, ok := featureSelector(selector); ok {
		for _, candidate := range installed {
			if candidate.Distribution == distribution && jdk.Feature(candidate.Version) == feature {
				return candidate, nil
			}
		}
	}
	if feature, err := strconv.Atoi(selector); err == nil && feature > 0 {
		var featureMatches []jdk.Installed
		for _, candidate := range installed {
			if jdk.Feature(candidate.Version) == feature {
				featureMatches = append(featureMatches, candidate)
			}
		}
		if len(featureMatches) > 0 {
			distribution := featureMatches[0].Distribution
			for _, candidate := range featureMatches[1:] {
				if candidate.Distribution != distribution {
					return jdk.Installed{}, fmt.Errorf("JDK feature %q is ambiguous; use a distribution@feature identifier", selector)
				}
			}
			return featureMatches[0], nil
		}
	}
	index, err := strconv.Atoi(selector)
	if err == nil && index >= 1 && index <= len(installed) {
		return installed[index-1], nil
	}
	return jdk.Installed{}, fmt.Errorf("JDK %q is not installed", selector)
}

func featureSelector(selector string) (string, int, bool) {
	distribution, featureText, ok := strings.Cut(selector, "@")
	feature, err := strconv.Atoi(featureText)
	return distribution, feature, ok && distribution != "" && err == nil && feature > 0
}

func (store *Store) jdksPath() string {
	return filepath.Join(store.root, "jdks")
}

func (store *Store) currentPath() string {
	return filepath.Join(store.root, "current")
}

// currentTarget resolves the current link so the active JDK is identified by identity rather than by name.
func (store *Store) currentTarget() (os.FileInfo, bool) {
	info, err := os.Stat(store.currentPath())
	if err != nil {
		return nil, false
	}
	return info, true
}
func findJDKHome(root string) (string, error) {
	var homes []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if isJDKHome(path) {
			homes = append(homes, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(homes) != 1 {
		return "", fmt.Errorf("archive must contain exactly one JDK home; found %d", len(homes))
	}
	return homes[0], nil
}

func isJDKHome(path string) bool {
	for _, executable := range []string{"java", "java.exe"} {
		info, err := os.Stat(filepath.Join(path, "bin", executable))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, character := range id {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && !strings.ContainsRune("._+-@", character) {
			return false
		}
	}
	return true
}

func readInstallation(path, fallbackVersion string) jdk.Installed {
	content, err := os.ReadFile(filepath.Join(path, metadataFileName))
	if err != nil {
		return jdk.Installed{Version: fallbackVersion}
	}
	var metadata installationMetadata
	if json.Unmarshal(content, &metadata) != nil || metadata.Version == "" {
		return jdk.Installed{Version: fallbackVersion}
	}
	return jdk.Installed{Distribution: metadata.Distribution, Version: metadata.Version}
}

func writeInstallation(path string, release jdk.Remote) error {
	content, err := json.Marshal(installationMetadata{
		Distribution: release.Distribution,
		Version:      release.Version,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, metadataFileName), append(content, '\n'), 0o644)
}

func removeLink(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("remove link %s: %w", path, err)
}
