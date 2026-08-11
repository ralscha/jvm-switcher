package store

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jvm-switcher/internal/jdk"
)

func TestInstallListSwitchAndRemove(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	installTestJDK(t, store, "temurin", "17.0.12+7")
	installTestJDK(t, store, "temurin", "21.0.4+7")

	installed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := installed[0].Version, "21.0.4+7"; got != want {
		t.Fatalf("first version = %q, want %q", got, want)
	}
	active, err := store.Switch("2")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := active.Version, "17.0.12+7"; got != want {
		t.Fatalf("active version = %q, want %q", got, want)
	}
	if _, err := store.Remove("17.0.12+7"); err == nil {
		t.Fatal("Remove(active) succeeded, want error")
	}
	removed, err := store.Remove("1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := removed.Version, "21.0.4+7"; got != want {
		t.Fatalf("removed version = %q, want %q", got, want)
	}
}

func installTestJDK(t *testing.T, store *Store, distribution, version string) {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), version+".zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("jdk-" + version + "/bin/java.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("java")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallArchive(jdk.Remote{Distribution: distribution, Version: version}, archivePath); err != nil {
		t.Fatal(err)
	}
}

func TestSameVersionFromDifferentDistributionsCanCoexist(t *testing.T) {
	store := New(t.TempDir())
	installTestJDK(t, store, "corretto", "21.0.4+7")
	installTestJDK(t, store, "temurin", "21.0.4+7")

	installed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 {
		t.Fatalf("installed count = %d, want 2", len(installed))
	}
	if _, err := store.Switch("21.0.4+7"); err == nil {
		t.Fatal("Switch(version) succeeded for ambiguous version")
	}
	active, err := store.Switch("corretto@21.0.4+7")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := active.ID(), "corretto@21.0.4+7"; got != want {
		t.Fatalf("active ID = %q, want %q", got, want)
	}
}

func TestListReportsActiveAfterSwitch(t *testing.T) {
	store := New(t.TempDir())
	installTestJDK(t, store, "temurin", "17.0.12+7")
	installTestJDK(t, store, "temurin", "21.0.4+7")
	if _, err := store.Switch("temurin@17.0.12+7"); err != nil {
		t.Fatal(err)
	}

	installed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, installation := range installed {
		if want := installation.ID() == "temurin@17.0.12+7"; installation.Active != want {
			t.Fatalf("%s active = %t, want %t", installation.ID(), installation.Active, want)
		}
	}
}

func TestListToleratesDanglingCurrentLink(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	installTestJDK(t, store, "temurin", "21.0.4+7")
	active, err := store.Switch("temurin@21.0.4+7")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(active.Path); err != nil {
		t.Fatal(err)
	}

	installed, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v, want nil for a dangling current link", err)
	}
	if len(installed) != 0 {
		t.Fatalf("installed count = %d, want 0", len(installed))
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "21.0.4+7", right: "17.0.12+7", want: 1},
		{left: "17.0.12+7", right: "21.0.4+7", want: -1},
		{left: "21.0.4+7", right: "21.0.4+7", want: 0},
		{left: "21.0.10+7", right: "21.0.9+7", want: 1},
		{left: "8.0.422+5", right: "11.0.24+8", want: -1},
	}
	for _, test := range tests {
		got := jdk.CompareVersions(test.left, test.right)
		if sign(got) != test.want {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", test.left, test.right, got, test.want)
		}
	}
}

func TestResolveFeatureSelectsNewestPatchAndDetectsDistributionAmbiguity(t *testing.T) {
	store := New(t.TempDir())
	installTestJDK(t, store, "temurin", "21.0.3+9")
	installTestJDK(t, store, "temurin", "21.0.4+7")

	resolved, err := store.Resolve("temurin@21")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != "21.0.4+7" {
		t.Fatalf("resolved version = %q, want %q", resolved.Version, "21.0.4+7")
	}
	resolved, err = store.Resolve("21")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != "21.0.4+7" {
		t.Fatalf("bare feature resolved version = %q, want %q", resolved.Version, "21.0.4+7")
	}

	installTestJDK(t, store, "corretto", "21.0.4.7.1")
	if _, err := store.Resolve("21"); err == nil {
		t.Fatal("Resolve(21) succeeded across multiple distributions")
	}
}

func TestLockSerializesStoreOperations(t *testing.T) {
	root := t.TempDir()
	first := New(root)
	second := New(root)
	unlockFirst, err := first.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := second.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock() error = %v, want context deadline exceeded", err)
	}
	if err := unlockFirst(); err != nil {
		t.Fatal(err)
	}
	unlockSecond, err := second.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := unlockSecond(); err != nil {
		t.Fatal(err)
	}
}

func sign(value int) int {
	switch {
	case value > 0:
		return 1
	case value < 0:
		return -1
	default:
		return 0
	}
}
