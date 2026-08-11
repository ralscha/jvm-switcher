package adoptium

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"jvm-switcher/internal/jdk"
)

func TestAvailableReturnsLatestReleasesNewestFirst(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/info/available_releases", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(response, `{"available_releases":[17,21]}`)
	})
	mux.HandleFunc("/assets/latest/17/hotspot", assetHandler(server.URL, 17, "17.0.12+7"))
	mux.HandleFunc("/assets/latest/21/hotspot", assetHandler(server.URL, 21, "21.0.4+7"))

	client := New(server.Client())
	client.BaseURL = server.URL
	client.GOOS = "windows"
	client.GOARCH = "amd64"

	releases, err := client.Available(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{releases[0].Version, releases[1].Version}
	if want := []string{"21.0.4+7", "17.0.12+7"}; !reflect.DeepEqual(versions, want) {
		t.Fatalf("versions = %v, want %v", versions, want)
	}
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	content := []byte("jdk archive")
	checksum := sha256.Sum256(content)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(content)
	}))
	t.Cleanup(server.Close)

	client := New(server.Client())
	var destination bytes.Buffer
	err := client.Download(context.Background(), remote(server.URL, hex.EncodeToString(checksum[:])), &destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destination.Bytes(), content) {
		t.Fatalf("downloaded %q, want %q", destination.Bytes(), content)
	}
}

func assetHandler(baseURL string, feature int, version string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("architecture") != "x64" || request.URL.Query().Get("os") != "windows" {
			http.Error(response, "missing platform query", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(response, `[{"binary":{"package":{"checksum":"abc","link":%q,"name":"jdk.zip","size":123}},"release_name":%q,"version":{"major":%d}}]`, baseURL+"/jdk.zip", "jdk-"+version, feature)
	}
}

func remote(url, checksum string) jdk.Remote {
	return jdk.Remote{URL: url, Version: "21.0.4+7", Checksum: checksum}
}
