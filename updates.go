package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"jvm-switcher/internal/jdk"
	"jvm-switcher/internal/project"
)

type updateCandidate struct {
	installed jdk.Installed
	available jdk.Remote
}

type outdatedRecord struct {
	InstalledID      string `json:"installed_id"`
	AvailableID      string `json:"available_id"`
	Distribution     string `json:"distribution"`
	Feature          int    `json:"feature"`
	InstalledVersion string `json:"installed_version"`
	AvailableVersion string `json:"available_version"`
	Active           bool   `json:"active"`
}

func (app application) outdated(ctx context.Context, args []string) error {
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) != 0 && !jsonOutput {
		return fmt.Errorf("usage: jvm-switcher outdated [--json]")
	}
	installed, err := app.store.List()
	if err != nil {
		return err
	}
	releases, err := app.provider.Available(ctx)
	if err != nil {
		return err
	}
	candidates := availableUpdates(installed, releases)
	if jsonOutput {
		records := make([]outdatedRecord, 0, len(candidates))
		for _, candidate := range candidates {
			records = append(records, outdatedRecord{
				InstalledID:      candidate.installed.ID(),
				AvailableID:      candidate.available.ID(),
				Distribution:     candidate.installed.Distribution,
				Feature:          jdk.Feature(candidate.installed.Version),
				InstalledVersion: candidate.installed.Version,
				AvailableVersion: candidate.available.Version,
				Active:           candidate.installed.Active,
			})
		}
		return writeJSON(app.out, records)
	}
	if len(candidates) == 0 {
		return app.outputf("All installed JDKs are up to date.\n")
	}
	if err := app.outputf("DISTRIBUTION  JAVA  INSTALLED                AVAILABLE\n"); err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := app.outputf("%-12s  %-4d  %-23s  %s\n", displayDistribution(candidate.installed.Distribution), jdk.Feature(candidate.installed.Version), candidate.installed.Version, candidate.available.Version); err != nil {
			return err
		}
	}
	return nil
}

func (app application) update(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: jvm-switcher update [version|index|--all]")
	}
	installed, err := app.store.List()
	if err != nil {
		return err
	}
	releases, err := app.provider.Available(ctx)
	if err != nil {
		return err
	}
	all := len(args) == 1 && args[0] == "--all"
	var candidates []updateCandidate
	if all {
		candidates = deduplicateUpdates(availableUpdates(installed, releases))
	} else {
		selector := ""
		if len(args) == 1 {
			selector = args[0]
		} else {
			selector, _, err = project.Find(app.workingDirectory())
			if err != nil {
				return fmt.Errorf("find project JDK: %w", err)
			}
		}
		target, err := app.store.Resolve(selector)
		if err != nil {
			return err
		}
		if candidate, ok := updateFor(target, releases); ok {
			candidates = append(candidates, candidate)
		} else {
			return app.outputf("JDK %s is up to date.\n", target.ID())
		}
	}
	if len(candidates) == 0 {
		return app.outputf("All installed JDKs are up to date.\n")
	}

	for _, candidate := range candidates {
		if latest, ok := findInstalled(installed, candidate.available.ID()); ok {
			if candidate.installed.Active && !latest.Active {
				if err := app.withStoreLock(ctx, func() error {
					_, err := app.store.Switch(latest.ID())
					return err
				}); err != nil {
					return err
				}
				if err := app.outputf("Updated active JDK %s to already-installed %s.\n", candidate.installed.ID(), latest.ID()); err != nil {
					return err
				}
			} else {
				if err := app.outputf("JDK %s is already installed.\n", latest.ID()); err != nil {
					return err
				}
			}
			continue
		}
		oldID := candidate.installed.ID()
		activated, err := app.installRelease(ctx, candidate.available, func(current []jdk.Installed) bool {
			for _, installation := range current {
				if installation.ID() == oldID {
					return installation.Active
				}
			}
			return false
		})
		if err != nil {
			return err
		}
		suffix := ""
		if activated {
			suffix = " and kept it active"
		}
		if err := app.outputf("Updated %s to %s%s.\n", oldID, candidate.available.ID(), suffix); err != nil {
			return err
		}
		installed = append(installed, jdk.Installed{Distribution: candidate.available.Distribution, Version: candidate.available.Version, Active: activated})
	}
	return nil
}

func (app application) prune(ctx context.Context, args []string) error {
	dryRun := len(args) == 1 && args[0] == "--dry-run"
	if len(args) != 0 && !dryRun {
		return fmt.Errorf("usage: jvm-switcher prune [--dry-run]")
	}
	action := func() error {
		installed, err := app.store.List()
		if err != nil {
			return err
		}
		candidates := pruneCandidates(installed)
		if len(candidates) == 0 {
			return app.outputf("Nothing to prune.\n")
		}
		for _, candidate := range candidates {
			if dryRun {
				if err := app.outputf("Would remove JDK %s.\n", candidate.ID()); err != nil {
					return err
				}
				continue
			}
			if _, err := app.store.Remove(candidate.ID()); err != nil {
				return err
			}
			if err := app.outputf("Removed JDK %s.\n", candidate.ID()); err != nil {
				return err
			}
		}
		return nil
	}
	if dryRun {
		return action()
	}
	return app.withStoreLock(ctx, action)
}

func availableUpdates(installed []jdk.Installed, releases []jdk.Remote) []updateCandidate {
	candidates := make([]updateCandidate, 0)
	for _, installation := range installed {
		if candidate, ok := updateFor(installation, releases); ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func updateFor(installed jdk.Installed, releases []jdk.Remote) (updateCandidate, bool) {
	feature := jdk.Feature(installed.Version)
	var latest jdk.Remote
	for _, release := range releases {
		if !strings.EqualFold(release.Distribution, installed.Distribution) || release.Feature != feature {
			continue
		}
		if latest.Version == "" || jdk.CompareVersions(release.Version, latest.Version) > 0 {
			latest = release
		}
	}
	if latest.Version == "" || jdk.CompareVersions(latest.Version, installed.Version) <= 0 {
		return updateCandidate{}, false
	}
	return updateCandidate{installed: installed, available: latest}, true
}

func deduplicateUpdates(candidates []updateCandidate) []updateCandidate {
	positions := make(map[string]int)
	result := make([]updateCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.available.ID()
		position, exists := positions[key]
		if !exists {
			positions[key] = len(result)
			result = append(result, candidate)
			continue
		}
		if candidate.installed.Active && !result[position].installed.Active {
			result[position] = candidate
		}
	}
	return result
}

func pruneCandidates(installed []jdk.Installed) []jdk.Installed {
	newest := make(map[string]jdk.Installed)
	for _, installation := range installed {
		key := installation.Distribution + "@" + strconv.Itoa(jdk.Feature(installation.Version))
		current, exists := newest[key]
		if !exists || jdk.CompareVersions(installation.Version, current.Version) > 0 {
			newest[key] = installation
		}
	}
	var candidates []jdk.Installed
	for _, installation := range installed {
		key := installation.Distribution + "@" + strconv.Itoa(jdk.Feature(installation.Version))
		if installation.ID() != newest[key].ID() && !installation.Active {
			candidates = append(candidates, installation)
		}
	}
	return candidates
}

func findInstalled(installed []jdk.Installed, id string) (jdk.Installed, bool) {
	for _, installation := range installed {
		if installation.ID() == id {
			return installation, true
		}
	}
	return jdk.Installed{}, false
}
