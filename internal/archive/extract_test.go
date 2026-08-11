package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractZIPRejectsPathTraversal(t *testing.T) {
	archivePath := writeZIP(t, map[string]string{"../outside.txt": "escape"})

	err := Extract(archivePath, filepath.Join(t.TempDir(), "destination"))
	if err == nil || !strings.Contains(err.Error(), "escapes the destination") {
		t.Fatalf("Extract() error = %v, want path traversal error", err)
	}
}

func TestExtractZIPWritesFiles(t *testing.T) {
	archivePath := writeZIP(t, map[string]string{"jdk-21/bin/java": "binary"})
	destination := filepath.Join(t.TempDir(), "destination")

	if err := Extract(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "jdk-21", "bin", "java"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "binary" {
		t.Fatalf("extracted content = %q, want %q", content, "binary")
	}
}

func TestExtractTarGZWritesFiles(t *testing.T) {
	archivePath := writeTarGZ(t, []tarEntry{
		{header: tar.Header{Name: "jdk-21/bin", Mode: 0o755, Typeflag: tar.TypeDir}},
		{header: tar.Header{Name: "jdk-21/bin/java", Mode: 0o755, Typeflag: tar.TypeReg}, body: "binary"},
	})
	destination := filepath.Join(t.TempDir(), "destination")

	if err := Extract(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "jdk-21", "bin", "java"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "binary" {
		t.Fatalf("extracted content = %q, want %q", content, "binary")
	}
}

func TestExtractTarGZRejectsPathTraversal(t *testing.T) {
	archivePath := writeTarGZ(t, []tarEntry{
		{header: tar.Header{Name: "../outside.txt", Mode: 0o644, Typeflag: tar.TypeReg}, body: "escape"},
	})

	err := Extract(archivePath, filepath.Join(t.TempDir(), "destination"))
	if err == nil || !strings.Contains(err.Error(), "escapes the destination") {
		t.Fatalf("Extract() error = %v, want path traversal error", err)
	}
}

func TestExtractTarGZRejectsAbsolutePaths(t *testing.T) {
	absoluteName := filepath.ToSlash(filepath.Join(t.TempDir(), "outside.txt"))
	archivePath := writeTarGZ(t, []tarEntry{
		{header: tar.Header{Name: absoluteName, Mode: 0o644, Typeflag: tar.TypeReg}, body: "escape"},
	})

	err := Extract(archivePath, filepath.Join(t.TempDir(), "destination"))
	if err == nil || !strings.Contains(err.Error(), "escapes the destination") {
		t.Fatalf("Extract() error = %v, want absolute path error", err)
	}
}

func TestExtractTarGZRejectsEscapingSymlink(t *testing.T) {
	archivePath := writeTarGZ(t, []tarEntry{
		{header: tar.Header{Name: "jdk-21/bin/escape", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "../../../outside"}},
	})

	err := Extract(archivePath, filepath.Join(t.TempDir(), "destination"))
	if err == nil || !strings.Contains(err.Error(), "escapes the destination") {
		t.Fatalf("Extract() error = %v, want symbolic link escape error", err)
	}
}

func TestExtractRejectsUnsupportedArchive(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "jdk.7z")
	if err := os.WriteFile(archivePath, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Extract(archivePath, filepath.Join(t.TempDir(), "destination"))
	if err == nil || !strings.Contains(err.Error(), "unsupported JDK archive") {
		t.Fatalf("Extract() error = %v, want unsupported archive error", err)
	}
}

func TestExtractStopsAtByteBudget(t *testing.T) {
	archivePath := writeZIP(t, map[string]string{"jdk-21/bin/java": strings.Repeat("a", 1024)})

	err := extractZIP(archivePath, filepath.Join(t.TempDir(), "destination"), newBudget(16, 10))
	if err == nil || !strings.Contains(err.Error(), "expands to more than 16 bytes") {
		t.Fatalf("extractZIP() error = %v, want byte budget error", err)
	}
}

func TestExtractStopsAtEntryBudget(t *testing.T) {
	archivePath := writeTarGZ(t, []tarEntry{
		{header: tar.Header{Name: "jdk-21/bin/java", Mode: 0o755, Typeflag: tar.TypeReg}, body: "binary"},
		{header: tar.Header{Name: "jdk-21/bin/javac", Mode: 0o755, Typeflag: tar.TypeReg}, body: "binary"},
	})

	err := extractTarGZ(archivePath, filepath.Join(t.TempDir(), "destination"), newBudget(1024, 1))
	if err == nil || !strings.Contains(err.Error(), "more than 1 entries") {
		t.Fatalf("extractTarGZ() error = %v, want entry budget error", err)
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"jdk.zip", "jdk.tar.gz", "jdk.TGZ", "OpenJDK21U-jdk_x64_windows_hotspot_21.0.4_7.zip"}
	for _, name := range valid {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", ".", "..", "jdk.msi", "jdk.tar", "sub/jdk.zip", `sub\jdk.zip`, "/jdk.zip"}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true, want false", name)
		}
	}
}

func writeZIP(t *testing.T, files map[string]string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "jdk.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

type tarEntry struct {
	header tar.Header
	body   string
}

func writeTarGZ(t *testing.T, entries []tarEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "jdk.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	writer := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := entry.header
		header.Size = int64(len(entry.body))
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}
