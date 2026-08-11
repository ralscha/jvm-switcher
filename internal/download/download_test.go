package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jvm-switcher/internal/jdk"
)

func TestJDKWritesVerifiedArchive(t *testing.T) {
	body := "archive"
	checksum := sha256.Sum256([]byte(body))
	server := serve(t, body, http.StatusOK)

	var destination strings.Builder
	release := jdk.Remote{Version: "21.0.4+7", URL: server.URL, Size: int64(len(body)), Checksum: hex.EncodeToString(checksum[:])}
	if err := JDK(context.Background(), server.Client(), release, &destination); err != nil {
		t.Fatal(err)
	}
	if destination.String() != body {
		t.Fatalf("downloaded content = %q, want %q", destination.String(), body)
	}
}

func TestJDKRejectsChecksumMismatch(t *testing.T) {
	server := serve(t, "tampered", http.StatusOK)

	release := jdk.Remote{Version: "21.0.4+7", URL: server.URL, Checksum: strings.Repeat("a", 64)}
	err := JDK(context.Background(), server.Client(), release, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("JDK() error = %v, want checksum mismatch", err)
	}
}

func TestJDKRejectsOversizedResponse(t *testing.T) {
	server := serve(t, strings.Repeat("a", 512), http.StatusOK)

	release := jdk.Remote{Version: "21.0.4+7", URL: server.URL, Size: 8, Checksum: strings.Repeat("a", 64)}
	err := JDK(context.Background(), server.Client(), release, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "more than the expected 8 bytes") {
		t.Fatalf("JDK() error = %v, want oversized response error", err)
	}
}

func TestJDKRejectsErrorStatus(t *testing.T) {
	server := serve(t, "missing", http.StatusNotFound)

	release := jdk.Remote{Version: "21.0.4+7", URL: server.URL, Checksum: strings.Repeat("a", 64)}
	err := JDK(context.Background(), server.Client(), release, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "server returned") {
		t.Fatalf("JDK() error = %v, want status error", err)
	}
}

func serve(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(status)
		_, _ = io.WriteString(response, body)
	}))
	t.Cleanup(server.Close)
	return server
}
