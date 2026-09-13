package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestSelectsPlatformAsset(t *testing.T) {
	binary := []byte("new dmux-verify")
	digest := sha256.Sum256(binary)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{
  "tag_name": "v1.2.3",
  "html_url": %q,
  "assets": [
    {"name":"dmux-verify-darwin-arm64","browser_download_url":"%s/wrong","digest":"sha256:%s"},
    {"name":"dmux-verify-linux-amd64","browser_download_url":"%s/binary","digest":"sha256:%s"}
  ]
}`, server.URL+"/release", server.URL, hex.EncodeToString(digest[:]), server.URL, hex.EncodeToString(digest[:]))
	}))
	defer server.Close()

	client := &Client{
		HTTPClient:       server.Client(),
		LatestReleaseURL: server.URL + "/latest",
		GOOS:             "linux",
		GOARCH:           "amd64",
	}
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "1.2.3" || release.Tag != "v1.2.3" {
		t.Fatalf("unexpected release version: %+v", release)
	}
	if release.AssetName != "dmux-verify-linux-amd64" || release.DownloadURL != server.URL+"/binary" {
		t.Fatalf("unexpected release asset: %+v", release)
	}
	if release.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected digest: %s", release.SHA256)
	}
}

func TestLatestRejectsMissingPlatformAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v1.2.3","assets":[]}`)
	}))
	defer server.Close()
	client := &Client{HTTPClient: server.Client(), LatestReleaseURL: server.URL, GOOS: "freebsd", GOARCH: "amd64"}
	_, err := client.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no binary for freebsd/amd64") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInstallReplacesExecutable(t *testing.T) {
	binary := []byte("new dmux-verify")
	digest := sha256.Sum256(binary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(binary)
	}))
	defer server.Close()

	dir := t.TempDir()
	executable := filepath.Join(dir, "dmux-verify")
	if err := os.WriteFile(executable, []byte("old dmux-verify"), 0o750); err != nil {
		t.Fatal(err)
	}
	client := &Client{HTTPClient: server.Client()}
	release := &Release{
		AssetName:   "dmux-verify-test",
		DownloadURL: server.URL,
		SHA256:      hex.EncodeToString(digest[:]),
	}
	if err := client.Install(context.Background(), release, executable); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Fatalf("unexpected executable contents: %q", got)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("unexpected executable mode: %o", info.Mode().Perm())
	}
}

func TestInstallChecksumMismatchPreservesExecutable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "tampered")
	}))
	defer server.Close()

	dir := t.TempDir()
	executable := filepath.Join(dir, "dmux-verify")
	if err := os.WriteFile(executable, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte("expected"))
	client := &Client{HTTPClient: server.Client()}
	err := client.Install(context.Background(), &Release{
		AssetName:   "dmux-verify-test",
		DownloadURL: server.URL,
		SHA256:      hex.EncodeToString(expected[:]),
	}, executable)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("original executable was changed: %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dmux-verify" {
		t.Fatalf("temporary update was not removed: %v", entries)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"0.1.4", "0.1.5", -1},
		{"v1.2.3", "1.2.3", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0", "1.0.0-beta", 1},
	}
	for _, tt := range tests {
		t.Run(tt.left+"_"+tt.right, func(t *testing.T) {
			got, err := CompareVersions(tt.left, tt.right)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("CompareVersions(%q, %q) = %d; want %d", tt.left, tt.right, got, tt.want)
			}
		})
	}
}
