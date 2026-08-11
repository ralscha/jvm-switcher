package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"jvm-switcher/internal/jdk"
)

type installedRecord struct {
	Index        int    `json:"index"`
	ID           string `json:"id"`
	Distribution string `json:"distribution,omitempty"`
	Version      string `json:"version"`
	Path         string `json:"path"`
	Active       bool   `json:"active"`
}

type remoteRecord struct {
	Index        int    `json:"index"`
	ID           string `json:"id"`
	Distribution string `json:"distribution,omitempty"`
	Feature      int    `json:"feature"`
	Version      string `json:"version"`
	FileName     string `json:"file_name"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
}

type showDocument struct {
	Source   string         `json:"source"`
	Releases []remoteRecord `json:"releases"`
}

type indexedRemote struct {
	index   int
	release jdk.Remote
}

type showOptions struct {
	json         bool
	lts          bool
	installed    bool
	distribution string
	feature      int
}

func parseShowOptions(args []string) (showOptions, error) {
	var options showOptions
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--json":
			options.json = true
		case "--lts":
			options.lts = true
		case "--installed":
			options.installed = true
		case "--distribution":
			index++
			if index == len(args) {
				return showOptions{}, fmt.Errorf("--distribution requires a value")
			}
			options.distribution = strings.ToLower(args[index])
		case "--feature":
			index++
			if index == len(args) {
				return showOptions{}, fmt.Errorf("--feature requires a value")
			}
			feature, err := strconv.Atoi(args[index])
			if err != nil || feature <= 0 {
				return showOptions{}, fmt.Errorf("--feature requires a positive Java feature version")
			}
			options.feature = feature
		default:
			return showOptions{}, fmt.Errorf("unknown show option %q", args[index])
		}
	}
	return options, nil
}

func filterReleases(releases []jdk.Remote, options showOptions, installed map[string]bool) []indexedRemote {
	filtered := make([]indexedRemote, 0, len(releases))
	for index, release := range releases {
		if options.distribution != "" && !strings.EqualFold(release.Distribution, options.distribution) {
			continue
		}
		if options.feature > 0 && release.Feature != options.feature {
			continue
		}
		if options.lts && !isLTS(release.Feature) {
			continue
		}
		if options.installed && !installed[release.ID()] {
			continue
		}
		filtered = append(filtered, indexedRemote{index: index + 1, release: release})
	}
	return filtered
}

func isLTS(feature int) bool {
	return feature == 8 || feature == 11 || feature >= 17 && (feature-17)%4 == 0
}

func installedRecords(installed []jdk.Installed) []installedRecord {
	records := make([]installedRecord, 0, len(installed))
	for index, installation := range installed {
		records = append(records, installedRecord{
			Index:        index + 1,
			ID:           installation.ID(),
			Distribution: installation.Distribution,
			Version:      installation.Version,
			Path:         installation.Path,
			Active:       installation.Active,
		})
	}
	return records
}

func remoteRecords(releases []indexedRemote) []remoteRecord {
	records := make([]remoteRecord, 0, len(releases))
	for _, item := range releases {
		release := item.release
		records = append(records, remoteRecord{
			Index:        item.index,
			ID:           release.ID(),
			Distribution: release.Distribution,
			Feature:      release.Feature,
			Version:      release.Version,
			FileName:     release.FileName,
			URL:          release.URL,
			SHA256:       release.Checksum,
			Size:         release.Size,
		})
	}
	return records
}

func writeJSON(destination io.Writer, value any) error {
	encoder := json.NewEncoder(destination)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
