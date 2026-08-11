package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func TestAvailableFiltersPlatformAndKeepsDistributionsDistinct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/catalog.json" {
			http.NotFound(response, request)
			return
		}
		_, _ = fmt.Fprint(response, `{
  "schema_version": 1,
  "releases": [
    {"distribution":"Temurin","feature":21,"version":"21.0.4+7","os":"windows","arch":"amd64","url":"jdk/temurin.zip","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
    {"distribution":"corretto","feature":21,"version":"21.0.4+7","os":"windows","arch":"amd64","url":"https://example.com/corretto.zip","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
    {"distribution":"temurin","feature":17,"version":"17.0.12+7","os":"linux","arch":"amd64","url":"https://example.com/linux.tar.gz","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
  ]
}`)
	}))
	t.Cleanup(server.Close)

	client := New(server.Client(), server.URL+"/catalog.json")
	client.GOOS = "windows"
	client.GOARCH = "amd64"
	releases, err := client.Available(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{releases[0].ID(), releases[1].ID()}
	if want := []string{"corretto@21.0.4+7", "temurin@21.0.4+7"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("release IDs = %v, want %v", ids, want)
	}
	if got, want := releases[1].URL, server.URL+"/jdk/temurin.zip"; got != want {
		t.Fatalf("resolved URL = %q, want %q", got, want)
	}
}

func TestAvailableRejectsInvalidChecksum(t *testing.T) {
	catalogPath := writeCatalog(t, `{"schema_version":1,"releases":[{"distribution":"zulu","feature":21,"version":"21.0.4","os":"windows","arch":"amd64","url":"https://example.com/zulu.zip","sha256":"bad"}]}`)
	client := New(nil, catalogPath)
	client.GOOS = "windows"
	client.GOARCH = "amd64"
	if _, err := client.Available(context.Background()); err == nil {
		t.Fatal("Available() succeeded with invalid checksum")
	}
}

func TestAvailableValidatesEntriesForOtherPlatforms(t *testing.T) {
	catalogPath := writeCatalog(t, `{"schema_version":1,"releases":[{"distribution":"zulu","feature":21,"version":"21.0.4","os":"linux","arch":"amd64","url":"https://example.com/zulu.tar.gz","sha256":"bad"}]}`)
	client := New(nil, catalogPath)
	client.GOOS = "windows"
	client.GOARCH = "amd64"
	if _, err := client.Available(context.Background()); err == nil {
		t.Fatal("Available() succeeded with an invalid entry for another platform")
	}
}

func writeCatalog(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/catalog.json"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
