package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jvm-switcher/internal/jdk"
)

func TestUpdateInstallsNewPatchAndKeepsActive(t *testing.T) {
	old := jdk.Installed{Distribution: "temurin", Version: "21.0.4+7", Active: true}
	latest := jdk.Remote{Distribution: "temurin", Feature: 21, Version: "21.0.5+11", FileName: "jdk.zip", Size: 7}
	provider := &fakeProvider{releases: []jdk.Remote{latest}}
	localStore := &fakeStore{installed: []jdk.Installed{old}}
	var output bytes.Buffer
	app := application{out: &output, provider: provider, store: localStore}

	if err := app.execute(context.Background(), []string{"update", "temurin@21"}); err != nil {
		t.Fatal(err)
	}
	if provider.downloaded.ID() != latest.ID() {
		t.Fatalf("downloaded = %q, want %q", provider.downloaded.ID(), latest.ID())
	}
	if localStore.switchSelector != latest.ID() {
		t.Fatalf("switch selector = %q, want %q", localStore.switchSelector, latest.ID())
	}
	if !strings.Contains(output.String(), "kept it active") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestOutdatedJSONReportsOnlyNewerVersions(t *testing.T) {
	provider := &fakeProvider{releases: []jdk.Remote{
		{Distribution: "temurin", Feature: 21, Version: "21.0.5+11"},
		{Distribution: "corretto", Feature: 17, Version: "17.0.12.7.1"},
	}}
	localStore := &fakeStore{installed: []jdk.Installed{
		{Distribution: "temurin", Version: "21.0.4+7", Active: true},
		{Distribution: "corretto", Version: "17.0.12.7.1"},
	}}
	var output bytes.Buffer
	app := application{out: &output, provider: provider, store: localStore}

	if err := app.execute(context.Background(), []string{"outdated", "--json"}); err != nil {
		t.Fatal(err)
	}
	var records []outdatedRecord
	if err := json.Unmarshal(output.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].InstalledID != "temurin@21.0.4+7" || records[0].AvailableVersion != "21.0.5+11" {
		t.Fatalf("records = %+v", records)
	}
}

func TestDeduplicateUpdatesPrefersActiveInstallation(t *testing.T) {
	latest := jdk.Remote{Distribution: "temurin", Feature: 21, Version: "21.0.5+11"}
	candidates := []updateCandidate{
		{installed: jdk.Installed{Distribution: "temurin", Version: "21.0.3+9"}, available: latest},
		{installed: jdk.Installed{Distribution: "temurin", Version: "21.0.4+7", Active: true}, available: latest},
	}

	result := deduplicateUpdates(candidates)
	if len(result) != 1 || !result[0].installed.Active {
		t.Fatalf("deduplicated updates = %+v", result)
	}
}

func TestUpdateForDoesNotDowngrade(t *testing.T) {
	installed := jdk.Installed{Distribution: "temurin", Version: "21.0.6+1"}
	releases := []jdk.Remote{{Distribution: "temurin", Feature: 21, Version: "21.0.5+11"}}
	if _, ok := updateFor(installed, releases); ok {
		t.Fatal("updateFor() offered a downgrade")
	}
}

func TestPruneCandidatesKeepsNewestAndActive(t *testing.T) {
	installed := []jdk.Installed{
		{Distribution: "temurin", Version: "21.0.5+11"},
		{Distribution: "temurin", Version: "21.0.4+7", Active: true},
		{Distribution: "temurin", Version: "21.0.3+9"},
		{Distribution: "temurin", Version: "17.0.12+7"},
	}

	candidates := pruneCandidates(installed)
	if len(candidates) != 1 || candidates[0].Version != "21.0.3+9" {
		t.Fatalf("prune candidates = %+v", candidates)
	}
}
