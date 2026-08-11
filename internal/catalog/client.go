package catalog

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"

	"jvm-switcher/internal/archive"
	"jvm-switcher/internal/build"
	"jvm-switcher/internal/download"
	"jvm-switcher/internal/jdk"
)

// maxCatalogBytes bounds how much of a remote catalog is read into the decoder.
const maxCatalogBytes = 16 << 20

type Client struct {
	HTTPClient *http.Client
	Source     string
	GOOS       string
	GOARCH     string
}

type Document struct {
	Schema        string  `json:"$schema,omitempty"`
	SchemaVersion int     `json:"schema_version"`
	Releases      []Entry `json:"releases"`
}

type Entry struct {
	Distribution string `json:"distribution"`
	Feature      int    `json:"feature"`
	Version      string `json:"version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size,omitempty"`
	FileName     string `json:"file_name,omitempty"`
}

func New(httpClient *http.Client, source string) *Client {
	return &Client{
		HTTPClient: httpClient,
		Source:     source,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

func (client *Client) Available(ctx context.Context) ([]jdk.Remote, error) {
	catalog, err := client.load(ctx)
	if err != nil {
		return nil, err
	}

	releases := make([]jdk.Remote, 0, len(catalog.Releases))
	for _, item := range catalog.Releases {
		if item.OS != client.GOOS || item.Arch != client.GOARCH {
			continue
		}
		release, err := client.release(item)
		if err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	sort.Slice(releases, func(left, right int) bool {
		if releases[left].Feature != releases[right].Feature {
			return releases[left].Feature > releases[right].Feature
		}
		if releases[left].Distribution != releases[right].Distribution {
			return releases[left].Distribution < releases[right].Distribution
		}
		return releases[left].Version > releases[right].Version
	})
	return releases, nil
}

func (client *Client) Validate(ctx context.Context) error {
	_, err := client.load(ctx)
	return err
}

func (client *Client) load(ctx context.Context) (catalog Document, err error) {
	reader, err := client.open(ctx)
	if err != nil {
		return Document{}, err
	}
	defer func() {
		err = errors.Join(err, reader.Close())
	}()

	decoder := json.NewDecoder(io.LimitReader(reader, maxCatalogBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return Document{}, fmt.Errorf("decode JDK catalog %q: %w", client.Source, err)
	}
	if catalog.SchemaVersion != 1 {
		return Document{}, fmt.Errorf("JDK catalog %q uses unsupported schema_version %d; want 1", client.Source, catalog.SchemaVersion)
	}

	seen := make(map[string]struct{})
	for index, item := range catalog.Releases {
		if !supportedPlatform(item.OS, item.Arch) {
			return Document{}, fmt.Errorf("JDK catalog release %d: unsupported platform %q/%q", index+1, item.OS, item.Arch)
		}
		release, err := client.release(item)
		if err != nil {
			return Document{}, fmt.Errorf("JDK catalog release %d: %w", index+1, err)
		}
		key := item.OS + "/" + item.Arch + "/" + release.ID()
		if _, exists := seen[key]; exists {
			return Document{}, fmt.Errorf("JDK catalog contains duplicate release %q for %s/%s", release.ID(), item.OS, item.Arch)
		}
		seen[key] = struct{}{}
	}
	return catalog, nil
}

func (client *Client) Download(ctx context.Context, release jdk.Remote, destination io.Writer) error {
	return download.JDK(ctx, client.httpClient(), release, destination)
}

func (client *Client) release(item Entry) (jdk.Remote, error) {
	distribution := strings.ToLower(strings.TrimSpace(item.Distribution))
	version := strings.TrimSpace(item.Version)
	if distribution == "" || version == "" || item.Feature <= 0 {
		return jdk.Remote{}, fmt.Errorf("distribution, positive feature, and version are required")
	}
	if !validIDPart(distribution) || !validIDPart(version) {
		return jdk.Remote{}, fmt.Errorf("distribution and version may contain only letters, digits, '.', '_', '+', and '-'")
	}
	if item.Size < 0 {
		return jdk.Remote{}, fmt.Errorf("size cannot be negative")
	}
	checksum := strings.ToLower(strings.TrimSpace(item.SHA256))
	decodedChecksum, err := hex.DecodeString(checksum)
	if err != nil || len(decodedChecksum) != 32 {
		return jdk.Remote{}, fmt.Errorf("sha256 must be a 64-character hexadecimal checksum")
	}
	downloadURL, err := client.resolveURL(item.URL)
	if err != nil {
		return jdk.Remote{}, err
	}
	fileName := strings.TrimSpace(item.FileName)
	if fileName == "" {
		parsedURL, _ := url.Parse(downloadURL)
		fileName = path.Base(parsedURL.Path)
	}
	if !archive.ValidName(fileName) {
		return jdk.Remote{}, fmt.Errorf("file_name %q must name a .zip, .tar.gz, or .tgz archive", fileName)
	}
	return jdk.Remote{
		Distribution: distribution,
		Feature:      item.Feature,
		Version:      version,
		FileName:     fileName,
		URL:          downloadURL,
		Checksum:     checksum,
		Size:         item.Size,
	}, nil
}

func (client *Client) open(ctx context.Context) (io.ReadCloser, error) {
	parsedURL, err := url.Parse(client.Source)
	if err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("create JDK catalog request: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", build.UserAgent())
		response, err := client.httpClient().Do(request)
		if err != nil {
			return nil, fmt.Errorf("load JDK catalog %q: %w", client.Source, err)
		}
		if response.StatusCode != http.StatusOK {
			return nil, errors.Join(
				fmt.Errorf("load JDK catalog %q: server returned %s", client.Source, response.Status),
				response.Body.Close(),
			)
		}
		return response.Body, nil
	}
	file, err := os.Open(client.Source)
	if err != nil {
		return nil, fmt.Errorf("load JDK catalog %q: %w", client.Source, err)
	}
	return file, nil
}

func (client *Client) resolveURL(value string) (string, error) {
	downloadURL, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("invalid download URL %q: %w", value, err)
	}
	if downloadURL.IsAbs() && (downloadURL.Scheme == "http" || downloadURL.Scheme == "https") {
		return downloadURL.String(), nil
	}
	catalogURL, err := url.Parse(client.Source)
	if err == nil && catalogURL.IsAbs() && (catalogURL.Scheme == "http" || catalogURL.Scheme == "https") {
		return catalogURL.ResolveReference(downloadURL).String(), nil
	}
	return "", fmt.Errorf("download URL %q must be an absolute HTTP(S) URL", value)
}

func (client *Client) httpClient() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	return http.DefaultClient
}

func supportedPlatform(goos, goarch string) bool {
	operatingSystems := map[string]bool{"darwin": true, "linux": true, "windows": true}
	architectures := map[string]bool{
		"386": true, "amd64": true, "arm": true, "arm64": true,
		"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true,
	}
	return operatingSystems[goos] && architectures[goarch]
}

func validIDPart(value string) bool {
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._+-", character) {
			continue
		}
		return false
	}
	return value != ""
}
