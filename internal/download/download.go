package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"jvm-switcher/internal/build"
	"jvm-switcher/internal/jdk"
)

// maxArchiveBytes bounds downloads whose catalog entry declares no size.
const maxArchiveBytes = 2 << 30

func JDK(ctx context.Context, httpClient *http.Client, release jdk.Remote, destination io.Writer) (err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	request.Header.Set("User-Agent", build.UserAgent())
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", release.ID(), err)
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: server returned %s", release.ID(), response.Status)
	}

	limit := release.Size
	if limit <= 0 {
		limit = maxArchiveBytes
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", release.ID(), err)
	}
	if written > limit {
		return fmt.Errorf("download %s: server sent more than the expected %d bytes", release.ID(), limit)
	}
	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actualChecksum, release.Checksum) {
		return fmt.Errorf("download %s: checksum mismatch: got %s, want %s", release.ID(), actualChecksum, release.Checksum)
	}
	return nil
}
