package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jvm-switcher/internal/jdk"
)

func TestCommandAliases(t *testing.T) {
	tests := []struct {
		args       []string
		wantSwitch string
		wantRemove string
	}{
		{args: []string{"ls"}},
		{args: []string{"s", "2"}, wantSwitch: "2"},
		{args: []string{"rm", "1"}, wantRemove: "1"},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			localStore := &fakeStore{installed: []jdk.Installed{{Version: "21.0.4+7", Active: true}}}
			app := application{out: io.Discard, provider: &fakeProvider{}, store: localStore}
			if err := app.execute(context.Background(), test.args); err != nil {
				t.Fatal(err)
			}
			if localStore.switchSelector != test.wantSwitch {
				t.Fatalf("switch selector = %q, want %q", localStore.switchSelector, test.wantSwitch)
			}
			if localStore.removeSelector != test.wantRemove {
				t.Fatalf("remove selector = %q, want %q", localStore.removeSelector, test.wantRemove)
			}
		})
	}
}

func TestInstallResolvesFeatureBeforeIndex(t *testing.T) {
	provider := &fakeProvider{releases: []jdk.Remote{
		{Feature: 21, Version: "21.0.4+7", FileName: "jdk21.zip", Size: 4096},
		{Feature: 17, Version: "17.0.12+7", FileName: "jdk17.zip", Size: 2048},
	}}
	localStore := &fakeStore{}
	var output bytes.Buffer
	app := application{out: &output, provider: provider, store: localStore}

	if err := app.execute(context.Background(), []string{"i", "17"}); err != nil {
		t.Fatal(err)
	}
	if got, want := provider.downloaded.Version, "17.0.12+7"; got != want {
		t.Fatalf("downloaded version = %q, want %q", got, want)
	}
	if got, want := localStore.installedVersion, "17.0.12+7"; got != want {
		t.Fatalf("installed version = %q, want %q", got, want)
	}
	if localStore.archiveContent != "archive" {
		t.Fatalf("archive content = %q, want %q", localStore.archiveContent, "archive")
	}
	if !strings.Contains(output.String(), "Installed JDK 17.0.12+7") {
		t.Fatalf("output = %q, want installation confirmation", output.String())
	}
	if got, want := localStore.switchSelector, "17.0.12+7"; got != want {
		t.Fatalf("switch selector = %q, want the first install to be activated", got)
	}
}

func TestVersionCommand(t *testing.T) {
	for _, argument := range []string{"version", "--version", "-v"} {
		var output bytes.Buffer
		app := application{out: &output}
		if err := app.execute(context.Background(), []string{argument}); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(output.String()) == "" {
			t.Fatalf("%s produced no version output", argument)
		}
	}
}

func TestChildExitCode(t *testing.T) {
	if os.Getenv("JVM_SWITCHER_EXIT_HELPER") == "1" {
		os.Exit(7)
	}
	command := exec.Command(os.Args[0], "-test.run=TestChildExitCode")
	command.Env = append(os.Environ(), "JVM_SWITCHER_EXIT_HELPER=1")
	err := command.Run()
	code, ok := childExitCode(err)
	if !ok || code != 7 {
		t.Fatalf("childExitCode() = %d, %t, want 7, true", code, ok)
	}
}

func TestHelpForAlias(t *testing.T) {
	var output bytes.Buffer
	app := application{out: &output}
	if err := app.execute(context.Background(), []string{"help", "s"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), "Usage: jvm-switcher switch") {
		t.Fatalf("help output = %q", output.String())
	}
}

func TestResolveRemoteRequiresDistributionWhenFeatureIsAmbiguous(t *testing.T) {
	releases := []jdk.Remote{
		{Distribution: "corretto", Feature: 21, Version: "21.0.4+7"},
		{Distribution: "temurin", Feature: 21, Version: "21.0.4+7"},
	}
	if _, err := resolveRemote(releases, "21"); err == nil {
		t.Fatal("resolveRemote(21) succeeded for ambiguous feature")
	}
	resolved, err := resolveRemote(releases, "corretto@21")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.ID(), "corretto@21.0.4+7"; got != want {
		t.Fatalf("resolved ID = %q, want %q", got, want)
	}
}

func TestExecUsesProjectSelectorAndChildEnvironment(t *testing.T) {
	projectDirectory := t.TempDir()
	nested := filepath.Join(projectDirectory, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDirectory, ".java-version"), []byte("temurin@21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	javaHome := filepath.Join(t.TempDir(), "temurin@21.0.4+7")
	if err := os.MkdirAll(filepath.Join(javaHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	executableName := "java"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	executablePath := filepath.Join(javaHome, "bin", executableName)
	if err := os.WriteFile(executablePath, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}

	localStore := &fakeStore{installed: []jdk.Installed{{Distribution: "temurin", Version: "21.0.4+7", Path: javaHome}}}
	var gotExecutable string
	var gotArguments []string
	var gotEnvironment []string
	app := application{
		out:       io.Discard,
		errOut:    io.Discard,
		store:     localStore,
		directory: nested,
		environ:   func() []string { return []string{"JAVA_HOME=old", "PATH=old", "KEEP=value"} },
		runChild: func(_ context.Context, executable string, arguments, environment []string, _ string, _ io.Reader, _, _ io.Writer) error {
			gotExecutable = executable
			gotArguments = arguments
			gotEnvironment = environment
			return nil
		},
	}
	if err := app.execute(context.Background(), []string{"exec", "--", "java", "-version"}); err != nil {
		t.Fatal(err)
	}
	if localStore.resolvedSelector != "temurin@21" {
		t.Fatalf("resolved selector = %q, want %q", localStore.resolvedSelector, "temurin@21")
	}
	if gotExecutable != executablePath {
		t.Fatalf("executable = %q, want %q", gotExecutable, executablePath)
	}
	if len(gotArguments) != 1 || gotArguments[0] != "-version" {
		t.Fatalf("arguments = %v, want [-version]", gotArguments)
	}
	environment := strings.Join(gotEnvironment, "\n")
	if !strings.Contains(environment, "JAVA_HOME="+javaHome) || !strings.Contains(environment, "KEEP=value") {
		t.Fatalf("environment = %q, want selected JAVA_HOME and inherited variables", environment)
	}
	if strings.Contains(environment, "JAVA_HOME=old") {
		t.Fatalf("environment = %q, contains old JAVA_HOME", environment)
	}
	wantPath := filepath.Join(javaHome, "bin") + string(os.PathListSeparator) + "old"
	if !strings.Contains(environment, "PATH="+wantPath) {
		t.Fatalf("environment = %q, want PATH=%q", environment, wantPath)
	}
}

func TestCurrentAndWhichReportActiveJDK(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "current")
	if err := os.MkdirAll(filepath.Join(current, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable := "java"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	executablePath := filepath.Join(current, "bin", executable)
	if err := os.WriteFile(executablePath, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	localStore := &fakeStore{
		root: root,
		installed: []jdk.Installed{{
			Distribution: "temurin",
			Version:      "21.0.4+7",
			Path:         filepath.Join(root, "jdks", "temurin@21.0.4+7"),
			Active:       true,
		}},
	}
	var output bytes.Buffer
	app := application{out: &output, store: localStore}
	if err := app.execute(context.Background(), []string{"current"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "temurin@21.0.4+7") {
		t.Fatalf("current output = %q", output.String())
	}
	output.Reset()
	if err := app.execute(context.Background(), []string{"current", "--quiet"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "temurin@21.0.4+7" {
		t.Fatalf("quiet current output = %q", output.String())
	}
	output.Reset()
	if err := app.execute(context.Background(), []string{"which", "java"}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != executablePath {
		t.Fatalf("which output = %q, want %q", strings.TrimSpace(output.String()), executablePath)
	}
}

func TestShowJSONFiltersAndPreservesCatalogIndex(t *testing.T) {
	releases := []jdk.Remote{
		{Distribution: "temurin", Feature: 25, Version: "25.0.1+8", FileName: "25.zip"},
		{Distribution: "corretto", Feature: 21, Version: "21.0.9.10.1", FileName: "corretto.zip"},
		{Distribution: "temurin", Feature: 21, Version: "21.0.9+10", FileName: "temurin.zip"},
	}
	localStore := &fakeStore{installed: []jdk.Installed{{Distribution: "temurin", Version: "21.0.9+10"}}}
	var output bytes.Buffer
	app := application{out: &output, provider: &fakeProvider{releases: releases}, store: localStore, source: "test-catalog"}
	args := []string{"show", "--json", "--distribution", "temurin", "--feature", "21", "--installed"}
	if err := app.execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	var document showDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Source != "test-catalog" || len(document.Releases) != 1 {
		t.Fatalf("show document = %+v", document)
	}
	if document.Releases[0].Index != 3 || document.Releases[0].ID != "temurin@21.0.9+10" {
		t.Fatalf("release = %+v, want original index 3", document.Releases[0])
	}
}

func TestListJSONUsesStableRecords(t *testing.T) {
	localStore := &fakeStore{installed: []jdk.Installed{{Distribution: "temurin", Version: "21.0.9+10", Path: "jdk-path", Active: true}}}
	var output bytes.Buffer
	app := application{out: &output, store: localStore}
	if err := app.execute(context.Background(), []string{"list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var records []installedRecord
	if err := json.Unmarshal(output.Bytes(), &records); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Index != 1 || records[0].ID != "temurin@21.0.9+10" || !records[0].Active {
		t.Fatalf("records = %+v", records)
	}
}

func TestDoctorChecksHealthySetup(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "current")
	bin := filepath.Join(current, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := "java"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, executable), []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	localStore := &fakeStore{
		root: root,
		installed: []jdk.Installed{{
			Distribution: "temurin",
			Version:      "21.0.4+7",
			Active:       true,
		}},
	}
	var output bytes.Buffer
	run := false
	app := application{
		out:   &output,
		store: localStore,
		environ: func() []string {
			return []string{"JAVA_HOME=" + current, "PATH=" + bin + string(os.PathListSeparator) + "other"}
		},
		runChild: func(_ context.Context, _ string, arguments, _ []string, _ string, _ io.Reader, _, _ io.Writer) error {
			run = len(arguments) == 1 && arguments[0] == "-version"
			return nil
		},
	}
	if err := app.execute(context.Background(), []string{"doctor"}); err != nil {
		t.Fatal(err)
	}
	if !run || strings.Contains(output.String(), "[fail]") {
		t.Fatalf("doctor output = %q, ran java = %t", output.String(), run)
	}
}

type fakeProvider struct {
	releases   []jdk.Remote
	downloaded jdk.Remote
}

func (provider *fakeProvider) Available(context.Context) ([]jdk.Remote, error) {
	return provider.releases, nil
}

func (provider *fakeProvider) Download(_ context.Context, release jdk.Remote, destination io.Writer) error {
	provider.downloaded = release
	_, err := io.WriteString(destination, "archive")
	return err
}

type fakeStore struct {
	root             string
	installed        []jdk.Installed
	resolvedSelector string
	switchSelector   string
	removeSelector   string
	installedVersion string
	archiveContent   string
}

func (store *fakeStore) Root() string {
	if store.root != "" {
		return store.root
	}
	return "test-store"
}

func (store *fakeStore) Lock(context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

func (store *fakeStore) List() ([]jdk.Installed, error) {
	return store.installed, nil
}

func (store *fakeStore) Resolve(selector string) (jdk.Installed, error) {
	store.resolvedSelector = selector
	if len(store.installed) == 0 {
		return jdk.Installed{}, nil
	}
	return store.installed[0], nil
}

func (store *fakeStore) InstallArchive(release jdk.Remote, archivePath string) error {
	content, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	store.installedVersion = release.Version
	store.archiveContent = string(content)
	return nil
}

func (store *fakeStore) Switch(selector string) (jdk.Installed, error) {
	store.switchSelector = selector
	return jdk.Installed{Version: "17.0.12+7", Active: true}, nil
}

func (store *fakeStore) Remove(selector string) (jdk.Installed, error) {
	store.removeSelector = selector
	return jdk.Installed{Version: "17.0.12+7"}, nil
}
