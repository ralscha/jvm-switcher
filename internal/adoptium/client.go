package adoptium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"sync"

	"jvm-switcher/internal/archive"
	"jvm-switcher/internal/build"
	"jvm-switcher/internal/download"
	"jvm-switcher/internal/jdk"
)

const defaultBaseURL = "https://api.adoptium.net/v3"

// maxConcurrentLookups keeps the per-feature fan-out polite towards the public API.
const maxConcurrentLookups = 6

var ErrNotAvailable = errors.New("temurin JDK is not available")

type Client struct {
	HTTPClient *http.Client
	BaseURL    string
	GOOS       string
	GOARCH     string
}

type ReleaseInfo struct {
	AvailableLTSReleases     []int `json:"available_lts_releases"`
	AvailableReleases        []int `json:"available_releases"`
	MostRecentFeatureRelease int   `json:"most_recent_feature_release"`
}

type asset struct {
	Binary struct {
		Package struct {
			Checksum string `json:"checksum"`
			Link     string `json:"link"`
			Name     string `json:"name"`
			Size     int64  `json:"size"`
		} `json:"package"`
	} `json:"binary"`
	ReleaseName string `json:"release_name"`
	Version     struct {
		Major int `json:"major"`
	} `json:"version"`
}

func New(httpClient *http.Client) *Client {
	return &Client{
		HTTPClient: httpClient,
		BaseURL:    defaultBaseURL,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
	}
}

func (client *Client) Available(ctx context.Context) ([]jdk.Remote, error) {
	info, err := client.ReleaseInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("list available releases: %w", err)
	}

	found := make([]jdk.Remote, len(info.AvailableReleases))
	failures := make([]error, len(info.AvailableReleases))
	limiter := make(chan struct{}, maxConcurrentLookups)
	var lookups sync.WaitGroup
	for index, feature := range info.AvailableReleases {
		lookups.Go(func() {
			limiter <- struct{}{}
			defer func() { <-limiter }()
			found[index], failures[index] = client.Latest(ctx, feature)
		})
	}
	lookups.Wait()

	available := make([]jdk.Remote, 0, len(found))
	for index, release := range found {
		if errors.Is(failures[index], ErrNotAvailable) {
			continue
		}
		if failures[index] != nil {
			return nil, failures[index]
		}
		available = append(available, release)
	}
	sort.Slice(available, func(left, right int) bool {
		return available[left].Feature > available[right].Feature
	})
	return available, nil
}

func (client *Client) ReleaseInfo(ctx context.Context) (ReleaseInfo, error) {
	var info ReleaseInfo
	if err := client.getJSON(ctx, "/info/available_releases", nil, &info); err != nil {
		return ReleaseInfo{}, err
	}
	return info, nil
}

func (client *Client) Download(ctx context.Context, release jdk.Remote, destination io.Writer) error {
	return download.JDK(ctx, client.httpClient(), release, destination)
}

func (client *Client) Latest(ctx context.Context, feature int) (jdk.Remote, error) {
	operatingSystem, architecture, err := platform(client.GOOS, client.GOARCH)
	if err != nil {
		return jdk.Remote{}, err
	}
	query := url.Values{
		"architecture": {architecture},
		"heap_size":    {"normal"},
		"image_type":   {"jdk"},
		"os":           {operatingSystem},
		"project":      {"jdk"},
		"vendor":       {"eclipse"},
	}
	var assets []asset
	path := fmt.Sprintf("/assets/latest/%d/hotspot", feature)
	if err := client.getJSON(ctx, path, query, &assets); err != nil {
		return jdk.Remote{}, fmt.Errorf("find latest Java %d release: %w", feature, err)
	}
	if len(assets) == 0 {
		return jdk.Remote{}, fmt.Errorf("%w for Java %d on %s/%s", ErrNotAvailable, feature, client.GOOS, client.GOARCH)
	}
	sort.Slice(assets, func(left, right int) bool {
		if assets[left].ReleaseName != assets[right].ReleaseName {
			return assets[left].ReleaseName > assets[right].ReleaseName
		}
		return assets[left].Binary.Package.Name < assets[right].Binary.Package.Name
	})
	selected := assets[0]
	if !archive.ValidName(selected.Binary.Package.Name) {
		return jdk.Remote{}, fmt.Errorf("latest Java %d release offers unsupported archive %q", feature, selected.Binary.Package.Name)
	}
	return jdk.Remote{
		Distribution: "temurin",
		Feature:      selected.Version.Major,
		Version:      strings.TrimPrefix(selected.ReleaseName, "jdk-"),
		FileName:     selected.Binary.Package.Name,
		URL:          selected.Binary.Package.Link,
		Checksum:     selected.Binary.Package.Checksum,
		Size:         selected.Binary.Package.Size,
	}, nil
}

func (client *Client) getJSON(ctx context.Context, path string, query url.Values, target any) (err error) {
	endpoint, err := url.Parse(strings.TrimRight(client.BaseURL, "/") + path)
	if err != nil {
		return fmt.Errorf("parse API URL: %w", err)
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", build.UserAgent())
	response, err := client.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (client *Client) httpClient() *http.Client {
	if client.HTTPClient != nil {
		return client.HTTPClient
	}
	return http.DefaultClient
}

func platform(goos, goarch string) (string, string, error) {
	operatingSystems := map[string]string{
		"darwin":  "mac",
		"linux":   "linux",
		"windows": "windows",
	}
	architectures := map[string]string{
		"386":     "x32",
		"amd64":   "x64",
		"arm":     "arm",
		"arm64":   "aarch64",
		"ppc64":   "ppc64",
		"ppc64le": "ppc64le",
		"riscv64": "riscv64",
		"s390x":   "s390x",
	}
	operatingSystem, ok := operatingSystems[goos]
	if !ok {
		return "", "", fmt.Errorf("unsupported operating system %q", goos)
	}
	architecture, ok := architectures[goarch]
	if !ok {
		return "", "", fmt.Errorf("unsupported architecture %q", goarch)
	}
	return operatingSystem, architecture, nil
}
