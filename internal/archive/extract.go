package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Extraction budgets bound the damage a decompression bomb can do; real JDK images are far smaller.
const (
	maxExtractedBytes   = 8 << 30
	maxExtractedEntries = 500_000
)

// ValidName reports whether fileName is a bare file name naming a supported archive format.
func ValidName(fileName string) bool {
	if fileName == "" || fileName == "." || fileName == ".." || strings.ContainsAny(fileName, `/\`) {
		return false
	}
	lowerName := strings.ToLower(fileName)
	return strings.HasSuffix(lowerName, ".zip") || strings.HasSuffix(lowerName, ".tar.gz") || strings.HasSuffix(lowerName, ".tgz")
}

func Extract(archivePath, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create extraction directory: %w", err)
	}
	budget := newBudget(maxExtractedBytes, maxExtractedEntries)
	lowerName := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lowerName, ".zip"):
		return extractZIP(archivePath, destination, budget)
	case strings.HasSuffix(lowerName, ".tar.gz"), strings.HasSuffix(lowerName, ".tgz"):
		return extractTarGZ(archivePath, destination, budget)
	default:
		return fmt.Errorf("unsupported JDK archive %q", filepath.Base(archivePath))
	}
}

type budget struct {
	maxBytes         int64
	remainingBytes   int64
	maxEntries       int
	remainingEntries int
}

func newBudget(maxBytes int64, maxEntries int) *budget {
	return &budget{maxBytes: maxBytes, remainingBytes: maxBytes, maxEntries: maxEntries, remainingEntries: maxEntries}
}

func (budget *budget) entry() error {
	if budget.remainingEntries == 0 {
		return fmt.Errorf("archive holds more than %d entries", budget.maxEntries)
	}
	budget.remainingEntries--
	return nil
}

func (budget *budget) copy(destination io.Writer, source io.Reader) error {
	written, err := io.Copy(destination, io.LimitReader(source, budget.remainingBytes+1))
	budget.remainingBytes -= written
	if err != nil {
		return err
	}
	if budget.remainingBytes < 0 {
		return fmt.Errorf("archive expands to more than %d bytes", budget.maxBytes)
	}
	return nil
}

func extractZIP(archivePath, destination string, budget *budget) (err error) {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer func() {
		err = errors.Join(err, reader.Close())
	}()

	for _, entry := range reader.File {
		if err := budget.entry(); err != nil {
			return err
		}
		target, err := securePath(destination, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("zip archive contains unsupported symbolic link %q", entry.Name)
		}
		if err := writeZIPFile(entry, target, budget); err != nil {
			return err
		}
	}
	return nil
}

func writeZIPFile(entry *zip.File, target string, budget *budget) (err error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, source.Close())
	}()
	mode := entry.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	copyErr := budget.copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func extractTarGZ(archivePath, destination string, budget *budget) (err error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar archive: %w", err)
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() {
		err = errors.Join(err, gzipReader.Close())
	}()

	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if err := budget.entry(); err != nil {
			return err
		}
		target, err := securePath(destination, header.Name)
		if err != nil {
			return err
		}
		if err := extractTarEntry(reader, header, target, destination, budget); err != nil {
			return err
		}
	}
}

func extractTarEntry(reader io.Reader, header *tar.Header, target, destination string, budget *budget) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, os.FileMode(header.Mode).Perm())
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode).Perm())
		if err != nil {
			return err
		}
		copyErr := budget.copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	case tar.TypeSymlink:
		linkTarget, err := secureLinkTarget(destination, filepath.Dir(target), header.Linkname)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Symlink(linkTarget, target)
	case tar.TypeLink:
		linkTarget, err := securePath(destination, header.Linkname)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Link(linkTarget, target)
	default:
		return nil
	}
}

func securePath(destination, name string) (string, error) {
	cleanName := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	target := filepath.Join(destination, cleanName)
	relative, err := filepath.Rel(destination, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return target, nil
}

func secureLinkTarget(destination, linkDirectory, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("archive link %q escapes the destination", name)
	}
	target := filepath.Clean(filepath.Join(linkDirectory, filepath.FromSlash(name)))
	relative, err := filepath.Rel(destination, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive link %q escapes the destination", name)
	}
	return filepath.FromSlash(name), nil
}
