package cataloggen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jvm-switcher/internal/adoptium"
	"jvm-switcher/internal/build"
	"jvm-switcher/internal/catalog"
)

const (
	defaultAdoptiumBaseURL = "https://api.adoptium.net/v3"
	defaultAzulBaseURL     = "https://api.azul.com/metadata/v1/zulu"
	defaultCorrettoURL     = "https://corretto.github.io/corretto-downloads/latest_links/indexmap_with_checksum.json"
	defaultCorrettoBaseURL = "https://corretto.aws"
	defaultMicrosoftURL    = "https://aka.ms/download-jdk"
)

type Generator struct {
	HTTPClient              *http.Client
	AdoptiumBaseURL         string
	AzulBaseURL             string
	CorrettoURL             string
	CorrettoDownloadBaseURL string
	MicrosoftBaseURL        string
}

type platform struct {
	OS   string
	Arch string
}

var platforms = []platform{
	{OS: "darwin", Arch: "amd64"},
	{OS: "darwin", Arch: "arm64"},
	{OS: "linux", Arch: "amd64"},
	{OS: "linux", Arch: "arm64"},
	{OS: "windows", Arch: "amd64"},
	{OS: "windows", Arch: "arm64"},
}

func New(httpClient *http.Client) *Generator {
	return &Generator{
		HTTPClient:              httpClient,
		AdoptiumBaseURL:         defaultAdoptiumBaseURL,
		AzulBaseURL:             defaultAzulBaseURL,
		CorrettoURL:             defaultCorrettoURL,
		CorrettoDownloadBaseURL: defaultCorrettoBaseURL,
		MicrosoftBaseURL:        defaultMicrosoftURL,
	}
}

func (generator *Generator) Generate(ctx context.Context) (catalog.Document, error) {
	adoptiumClient := adoptium.New(generator.httpClient())
	adoptiumClient.BaseURL = generator.AdoptiumBaseURL
	info, err := adoptiumClient.ReleaseInfo(ctx)
	if err != nil {
		return catalog.Document{}, fmt.Errorf("load OpenJDK release policy: %w", err)
	}
	features := catalogFeatures(info)
	if len(features) == 0 {
		return catalog.Document{}, fmt.Errorf("OpenJDK release policy contains no LTS or current feature")
	}

	sources := []struct {
		name string
		load func(context.Context, []int) ([]catalog.Entry, error)
	}{
		{name: "temurin", load: func(ctx context.Context, features []int) ([]catalog.Entry, error) {
			return generator.temurin(ctx, adoptiumClient, features)
		}},
		{name: "corretto", load: generator.corretto},
		{name: "zulu", load: generator.zulu},
		{name: "microsoft", load: generator.microsoft},
	}

	var releases []catalog.Entry
	for _, source := range sources {
		entries, err := source.load(ctx, features)
		if err != nil {
			return catalog.Document{}, fmt.Errorf("load %s releases: %w", source.name, err)
		}
		if len(entries) == 0 {
			return catalog.Document{}, fmt.Errorf("load %s releases: source returned no supported JDK archives", source.name)
		}
		releases = append(releases, entries...)
	}

	sort.Slice(releases, func(left, right int) bool {
		if releases[left].Distribution != releases[right].Distribution {
			return releases[left].Distribution < releases[right].Distribution
		}
		if releases[left].Feature != releases[right].Feature {
			return releases[left].Feature > releases[right].Feature
		}
		if releases[left].OS != releases[right].OS {
			return releases[left].OS < releases[right].OS
		}
		return releases[left].Arch < releases[right].Arch
	})

	return catalog.Document{
		Schema:        "./catalog.schema.json",
		SchemaVersion: 1,
		Releases:      releases,
	}, nil
}

func (generator *Generator) temurin(ctx context.Context, client *adoptium.Client, features []int) ([]catalog.Entry, error) {
	var entries []catalog.Entry
	for _, feature := range features {
		for _, target := range platforms {
			client.GOOS = target.OS
			client.GOARCH = target.Arch
			release, err := client.Latest(ctx, feature)
			if errors.Is(err, adoptium.ErrNotAvailable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			entries = append(entries, catalog.Entry{
				Distribution: release.Distribution,
				Feature:      release.Feature,
				Version:      release.Version,
				OS:           target.OS,
				Arch:         target.Arch,
				URL:          release.URL,
				SHA256:       release.Checksum,
				Size:         release.Size,
				FileName:     release.FileName,
			})
		}
	}
	return entries, nil
}

type correttoArtifact struct {
	Resource       string `json:"resource"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

func (generator *Generator) corretto(ctx context.Context, features []int) ([]catalog.Entry, error) {
	var index map[string]json.RawMessage
	if err := generator.getJSON(ctx, generator.CorrettoURL, &index); err != nil {
		return nil, err
	}

	wanted := featureSet(features)
	var entries []catalog.Entry
	for vendorOS, goos := range map[string]string{"linux": "linux", "macos": "darwin", "windows": "windows"} {
		var architectures map[string]map[string]map[string]map[string]correttoArtifact
		if err := json.Unmarshal(index[vendorOS], &architectures); err != nil {
			return nil, fmt.Errorf("decode %s index: %w", vendorOS, err)
		}
		archiveType := "tar.gz"
		if goos == "windows" {
			archiveType = "zip"
		}
		for vendorArch, goarch := range map[string]string{"aarch64": "arm64", "x64": "amd64"} {
			for featureText, archives := range architectures[vendorArch]["jdk"] {
				feature, err := strconv.Atoi(featureText)
				if err != nil || !wanted[feature] {
					continue
				}
				artifact, ok := archives[archiveType]
				if !ok {
					continue
				}
				version, err := resourceVersion(artifact.Resource)
				if err != nil {
					return nil, err
				}
				downloadURL, err := resolveURL(generator.CorrettoDownloadBaseURL, artifact.Resource)
				if err != nil {
					return nil, err
				}
				size, err := generator.contentLength(ctx, downloadURL)
				if err != nil {
					return nil, err
				}
				entries = append(entries, catalog.Entry{
					Distribution: "corretto",
					Feature:      feature,
					Version:      version,
					OS:           goos,
					Arch:         goarch,
					URL:          downloadURL,
					SHA256:       artifact.ChecksumSHA256,
					Size:         size,
					FileName:     path.Base(artifact.Resource),
				})
			}
		}
	}
	return entries, nil
}

type zuluPackage struct {
	Arch                string   `json:"arch"`
	ArchiveType         string   `json:"archive_type"`
	AvailabilityType    string   `json:"availability_type"`
	Certifications      []string `json:"certifications"`
	CracSupported       bool     `json:"crac_supported"`
	DistroVersion       []int    `json:"distro_version"`
	DownloadURL         string   `json:"download_url"`
	HWBitness           int      `json:"hw_bitness"`
	JavaPackageFeatures []string `json:"java_package_features"`
	JavaVersion         []int    `json:"java_version"`
	JavaFXBundled       bool     `json:"javafx_bundled"`
	Latest              bool     `json:"latest"`
	LibCType            *string  `json:"lib_c_type"`
	Name                string   `json:"name"`
	OpenJDKBuildNumber  int      `json:"openjdk_build_number"`
	OS                  string   `json:"os"`
	ReleaseStatus       string   `json:"release_status"`
	SHA256              string   `json:"sha256_hash"`
	Size                int64    `json:"size"`
}

type zuluCandidate struct {
	item   zuluPackage
	goos   string
	goarch string
}

func (generator *Generator) zulu(ctx context.Context, features []int) ([]catalog.Entry, error) {
	wanted := featureSet(features)
	requests := []struct {
		vendorOS string
		archive  string
	}{
		{vendorOS: "linux", archive: "tar.gz"},
		{vendorOS: "macos", archive: "tar.gz"},
		{vendorOS: "windows", archive: "zip"},
	}
	candidates := make(map[string]zuluCandidate)
	for _, request := range requests {
		query := url.Values{
			"archive_type":       {request.archive},
			"availability_types": {"CA"},
			"certifications":     {"tck"},
			"include_fields": {"arch,archive_type,certifications,crac_supported,distro_version,hw_bitness," +
				"java_package_features,javafx_bundled,lib_c_type,os,release_status,sha256_hash,size"},
			"java_package_type": {"jdk"},
			"javafx_bundled":    {"false"},
			"latest":            {"true"},
			"os":                {request.vendorOS},
			"page_size":         {"1000"},
			"release_status":    {"ga"},
		}
		endpoint := strings.TrimRight(generator.AzulBaseURL, "/") + "/packages/?" + query.Encode()
		var packages []zuluPackage
		if err := generator.getJSON(ctx, endpoint, &packages); err != nil {
			return nil, err
		}
		for _, item := range packages {
			feature := first(item.JavaVersion)
			goarch, ok := zuluArchitecture(item.Arch, item.HWBitness)
			if !ok || !wanted[feature] || !standardZuluPackage(item) {
				continue
			}
			goos := map[string]string{"linux": "linux", "macos": "darwin", "windows": "windows"}[item.OS]
			if goos == "" {
				continue
			}
			key := fmt.Sprintf("%d/%s/%s", feature, goos, goarch)
			if current, ok := candidates[key]; ok && compareZuluPackages(item, current.item) <= 0 {
				continue
			}
			candidates[key] = zuluCandidate{item: item, goos: goos, goarch: goarch}
		}
	}

	entries := make([]catalog.Entry, 0, len(candidates))
	for _, candidate := range candidates {
		item := candidate.item
		version := versionString(item.JavaVersion, item.OpenJDKBuildNumber)
		if distroVersion := integerVersion(item.DistroVersion); distroVersion != "" {
			version += "-zulu" + distroVersion
		}
		entries = append(entries, catalog.Entry{
			Distribution: "zulu",
			Feature:      first(item.JavaVersion),
			Version:      version,
			OS:           candidate.goos,
			Arch:         candidate.goarch,
			URL:          item.DownloadURL,
			SHA256:       item.SHA256,
			Size:         item.Size,
			FileName:     item.Name,
		})
	}
	return entries, nil
}

func (generator *Generator) microsoft(ctx context.Context, features []int) ([]catalog.Entry, error) {
	var entries []catalog.Entry
	for _, feature := range features {
		if feature < 11 {
			continue
		}
		for _, target := range platforms {
			vendorOS, archive := microsoftPlatform(target.OS)
			vendorArch := map[string]string{"amd64": "x64", "arm64": "aarch64"}[target.Arch]
			fileName := fmt.Sprintf("microsoft-jdk-%d-%s-%s.%s", feature, vendorOS, vendorArch, archive)
			alias := strings.TrimRight(generator.MicrosoftBaseURL, "/") + "/" + fileName
			response, err := generator.request(ctx, http.MethodHead, alias)
			if err != nil {
				return nil, err
			}
			if err := response.Body.Close(); err != nil {
				return nil, fmt.Errorf("close HEAD %s response: %w", alias, err)
			}
			if response.StatusCode == http.StatusNotFound {
				continue
			}
			if response.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("HEAD %s: server returned %s", alias, response.Status)
			}
			resolvedURL := response.Request.URL.String()
			resolvedName := path.Base(response.Request.URL.Path)
			version, err := microsoftVersion(resolvedName, vendorOS, vendorArch, archive)
			if err != nil {
				continue
			}
			checksum, err := generator.checksum(ctx, alias+".sha256sum.txt")
			if err != nil {
				return nil, err
			}
			size := max(response.ContentLength, 0)
			entries = append(entries, catalog.Entry{
				Distribution: "microsoft",
				Feature:      feature,
				Version:      version,
				OS:           target.OS,
				Arch:         target.Arch,
				URL:          resolvedURL,
				SHA256:       checksum,
				Size:         size,
				FileName:     resolvedName,
			})
		}
	}
	return entries, nil
}

func (generator *Generator) contentLength(ctx context.Context, source string) (int64, error) {
	response, err := generator.request(ctx, http.MethodHead, source)
	if err != nil {
		return 0, err
	}
	if err := response.Body.Close(); err != nil {
		return 0, fmt.Errorf("close HEAD %s response: %w", source, err)
	}
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD %s: server returned %s", source, response.Status)
	}
	if response.ContentLength < 0 {
		return 0, nil
	}
	return response.ContentLength, nil
}

func (generator *Generator) checksum(ctx context.Context, source string) (checksum string, err error) {
	response, err := generator.request(ctx, http.MethodGet, source)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: server returned %s", source, response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", source, err)
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return "", fmt.Errorf("read %s: checksum file is empty", source)
	}
	return strings.ToLower(fields[0]), nil
}

func (generator *Generator) getJSON(ctx context.Context, source string, target any) (err error) {
	response, err := generator.request(ctx, http.MethodGet, source)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: server returned %s", source, response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", source, err)
	}
	return nil
}

func (generator *Generator) request(ctx context.Context, method, source string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, source, nil)
	if err != nil {
		return nil, fmt.Errorf("create %s request: %w", method, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", build.UserAgent())
	response, err := generator.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, source, err)
	}
	return response, nil
}

func (generator *Generator) httpClient() *http.Client {
	if generator.HTTPClient != nil {
		return generator.HTTPClient
	}
	return http.DefaultClient
}

func catalogFeatures(info adoptium.ReleaseInfo) []int {
	wanted := featureSet(info.AvailableLTSReleases)
	if info.MostRecentFeatureRelease > 0 {
		wanted[info.MostRecentFeatureRelease] = true
	}
	features := make([]int, 0, len(wanted))
	for feature := range wanted {
		features = append(features, feature)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(features)))
	return features
}

func featureSet(features []int) map[int]bool {
	wanted := make(map[int]bool, len(features))
	for _, feature := range features {
		if feature > 0 {
			wanted[feature] = true
		}
	}
	return wanted
}

func resourceVersion(resource string) (string, error) {
	parts := strings.Split(strings.Trim(resource, "/"), "/")
	for index, part := range parts {
		if part == "resources" && index+1 < len(parts) {
			return parts[index+1], nil
		}
	}
	return "", fmt.Errorf("cannot determine Corretto version from resource %q", resource)
}

func resolveURL(baseURL, reference string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	relative, err := url.Parse(reference)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(relative).String(), nil
}

func standardZuluPackage(item zuluPackage) bool {
	if item.AvailabilityType != "CA" || item.ReleaseStatus != "ga" || !item.Latest || item.JavaFXBundled || item.CracSupported {
		return false
	}
	if item.LibCType != nil && *item.LibCType != "glibc" {
		return false
	}
	return len(item.JavaPackageFeatures) == 1 && item.JavaPackageFeatures[0] == "jdk" && contains(item.Certifications, "tck")
}

func zuluArchitecture(architecture string, bitness int) (string, bool) {
	if bitness != 64 {
		return "", false
	}
	switch architecture {
	case "x86":
		return "amd64", true
	case "arm":
		return "arm64", true
	default:
		return "", false
	}
}

func compareZuluPackages(left, right zuluPackage) int {
	if comparison := compareIntegerVersions(left.JavaVersion, right.JavaVersion); comparison != 0 {
		return comparison
	}
	if left.OpenJDKBuildNumber != right.OpenJDKBuildNumber {
		return left.OpenJDKBuildNumber - right.OpenJDKBuildNumber
	}
	return compareIntegerVersions(left.DistroVersion, right.DistroVersion)
}

func compareIntegerVersions(left, right []int) int {
	length := max(len(left), len(right))
	for index := range length {
		leftPart, rightPart := 0, 0
		if index < len(left) {
			leftPart = left[index]
		}
		if index < len(right) {
			rightPart = right[index]
		}
		if leftPart < rightPart {
			return -1
		}
		if leftPart > rightPart {
			return 1
		}
	}
	return 0
}

func versionString(version []int, build int) string {
	value := integerVersion(version)
	if build > 0 {
		value += "+" + strconv.Itoa(build)
	}
	return value
}

func integerVersion(version []int) string {
	parts := make([]string, len(version))
	for index, number := range version {
		parts[index] = strconv.Itoa(number)
	}
	for len(parts) > 0 && parts[len(parts)-1] == "0" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, ".")
}

func microsoftPlatform(goos string) (string, string) {
	if goos == "windows" {
		return "windows", "zip"
	}
	if goos == "darwin" {
		return "macos", "tar.gz"
	}
	return "linux", "tar.gz"
}

func microsoftVersion(fileName, operatingSystem, architecture, archive string) (string, error) {
	prefix := "microsoft-jdk-"
	suffix := "-" + operatingSystem + "-" + architecture + "." + archive
	if !strings.HasPrefix(fileName, prefix) || !strings.HasSuffix(fileName, suffix) {
		return "", fmt.Errorf("cannot determine Microsoft JDK version from file name %q", fileName)
	}
	return strings.TrimSuffix(strings.TrimPrefix(fileName, prefix), suffix), nil
}

func contains(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}

func first(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}
