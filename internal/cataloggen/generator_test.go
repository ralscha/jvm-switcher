package cataloggen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGenerateCombinesOfficialSources(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/adoptium/info/available_releases", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(response, `{"available_lts_releases":[21],"available_releases":[21,22],"most_recent_feature_release":22}`)
	})
	mux.HandleFunc("/adoptium/assets/latest/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("os") != "windows" || request.URL.Query().Get("architecture") != "x64" {
			_, _ = fmt.Fprint(response, `[]`)
			return
		}
		featureText := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/adoptium/assets/latest/"), "/hotspot")
		feature, _ := strconv.Atoi(featureText)
		_, _ = fmt.Fprintf(response, `[{"binary":{"package":{"checksum":"%s","link":%q,"name":%q,"size":123}},"release_name":%q,"version":{"major":%d}}]`,
			strings.Repeat("a", 64), server.URL+"/downloads/temurin-"+featureText+".zip", "temurin-"+featureText+".zip", "jdk-"+featureText+".0.1+1", feature)
	})
	mux.HandleFunc("/corretto/index.json", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(response, `{"linux":{},"macos":{},"windows":{"x64":{"jdk":{"21":{"zip":{"resource":"/corretto-downloads/resources/21.0.1.1.1/corretto-21.zip","checksum_sha256":"%s"}}}}}}`, strings.Repeat("b", 64))
	})
	mux.HandleFunc("/corretto-downloads/", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			t.Errorf("Corretto artifact method = %s, want HEAD", request.Method)
		}
		response.Header().Set("Content-Length", "321")
	})
	mux.HandleFunc("/azul/packages/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("os") != "windows" {
			_, _ = fmt.Fprint(response, `[]`)
			return
		}
		_, _ = fmt.Fprintf(response, `[{"arch":"x86","archive_type":"zip","availability_type":"CA","certifications":["tck"],"crac_supported":false,"distro_version":[21,52,15,0],"download_url":%q,"hw_bitness":64,"java_package_features":["jdk"],"java_version":[21,0,12],"javafx_bundled":false,"latest":true,"lib_c_type":null,"name":"zulu21.zip","openjdk_build_number":8,"os":"windows","release_status":"ga","sha256_hash":"%s","size":456},{"arch":"x86","archive_type":"zip","availability_type":"CA","certifications":["tck"],"crac_supported":false,"distro_version":[21,40,0,0],"download_url":%q,"hw_bitness":64,"java_package_features":["jdk"],"java_version":[21,0,1],"javafx_bundled":false,"latest":true,"lib_c_type":null,"name":"old-zulu21.zip","openjdk_build_number":1,"os":"windows","release_status":"ga","sha256_hash":"%s","size":400}]`, server.URL+"/downloads/zulu21.zip", strings.Repeat("c", 64), server.URL+"/downloads/old-zulu21.zip", strings.Repeat("e", 64))
	})
	mux.HandleFunc("/microsoft/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/microsoft/microsoft-jdk-21-windows-x64.zip" && request.Method == http.MethodHead {
			http.Redirect(response, request, server.URL+"/microsoft-downloads/microsoft-jdk-21.0.1-windows-x64.zip", http.StatusFound)
			return
		}
		if request.URL.Path == "/microsoft/microsoft-jdk-21-windows-x64.zip.sha256sum.txt" {
			_, _ = fmt.Fprintf(response, "%s  microsoft-jdk-21.0.1-windows-x64.zip", strings.Repeat("d", 64))
			return
		}
		http.NotFound(response, request)
	})
	mux.HandleFunc("/microsoft-downloads/microsoft-jdk-21.0.1-windows-x64.zip", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead {
			t.Fatalf("Microsoft artifact method = %s, want HEAD", request.Method)
		}
		response.Header().Set("Content-Length", "789")
	})

	generator := New(server.Client())
	generator.AdoptiumBaseURL = server.URL + "/adoptium"
	generator.AzulBaseURL = server.URL + "/azul"
	generator.CorrettoURL = server.URL + "/corretto/index.json"
	generator.CorrettoDownloadBaseURL = server.URL
	generator.MicrosoftBaseURL = server.URL + "/microsoft"

	document, err := generator.Generate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var distributions []string
	for _, release := range document.Releases {
		distributions = append(distributions, release.Distribution)
		if release.Distribution == "corretto" && release.Size != 321 {
			t.Fatalf("corretto size = %d, want 321", release.Size)
		}
	}
	if want := []string{"corretto", "microsoft", "temurin", "temurin", "zulu"}; !reflect.DeepEqual(distributions, want) {
		t.Fatalf("distributions = %v, want %v", distributions, want)
	}
	if _, err := json.Marshal(document); err != nil {
		t.Fatalf("marshal generated catalog: %v", err)
	}
}
