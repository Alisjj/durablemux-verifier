// Package updater discovers and installs dmux-verify releases from GitHub.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/Alisjj/durablemux-verifier/releases/latest"
	maxMetadataSize  = 2 << 20
	maxBinarySize    = 128 << 20
)

// Release describes the latest binary available for the current platform.
type Release struct {
	Version     string
	Tag         string
	PageURL     string
	AssetName   string
	DownloadURL string
	SHA256      string
}

// Client retrieves release metadata and binaries.
type Client struct {
	HTTPClient       *http.Client
	LatestReleaseURL string
	GOOS             string
	GOARCH           string
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

// NewClient returns a client configured for the public release repository and
// the platform running dmux-verify.
func NewClient() *Client {
	return &Client{
		HTTPClient:       &http.Client{Timeout: 2 * time.Minute},
		LatestReleaseURL: latestReleaseURL,
		GOOS:             runtime.GOOS,
		GOARCH:           runtime.GOARCH,
	}
}

// Latest returns the latest stable release and its matching platform binary.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	endpoint := c.LatestReleaseURL
	if endpoint == "" {
		endpoint = latestReleaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	setHeaders(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("check latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("check latest release", resp)
	}

	var payload githubRelease
	reader := io.LimitReader(resp.Body, maxMetadataSize+1)
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode latest release: %w", err)
	}
	if payload.TagName == "" {
		return nil, fmt.Errorf("latest release has no tag")
	}

	goos := c.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := c.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	assetName := fmt.Sprintf("dmux-verify-%s-%s", goos, goarch)
	for _, asset := range payload.Assets {
		if asset.Name != assetName {
			continue
		}
		if asset.BrowserDownloadURL == "" {
			return nil, fmt.Errorf("release asset %s has no download URL", assetName)
		}
		digest, err := parseSHA256(asset.Digest)
		if err != nil {
			return nil, fmt.Errorf("release asset %s: %w", assetName, err)
		}
		return &Release{
			Version:     strings.TrimPrefix(payload.TagName, "v"),
			Tag:         payload.TagName,
			PageURL:     payload.HTMLURL,
			AssetName:   assetName,
			DownloadURL: asset.BrowserDownloadURL,
			SHA256:      digest,
		}, nil
	}
	return nil, fmt.Errorf("release %s has no binary for %s/%s (%s)", payload.TagName, goos, goarch, assetName)
}

// Install downloads, verifies, and atomically replaces executable with the
// release binary. The temporary file is created beside executable so rename
// remains atomic.
func (c *Client) Install(ctx context.Context, release *Release, executable string) error {
	if release == nil {
		return fmt.Errorf("release is required")
	}
	expected, err := parseSHA256("sha256:" + release.SHA256)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve executable %s: %w", executable, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("inspect executable %s: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("executable is not a regular file: %s", resolved)
	}

	tmp, err := os.CreateTemp(filepath.Dir(resolved), ".dmux-verify-update-*")
	if err != nil {
		return fmt.Errorf("create update beside %s: %w", resolved, err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	hash := sha256.New()
	if err := c.download(ctx, release.DownloadURL, io.MultiWriter(tmp, hash)); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", release.AssetName, expected, actual)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync downloaded update: %w", err)
	}
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode |= 0o755
	}
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("make downloaded update executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close downloaded update: %w", err)
	}
	if err := os.Rename(tmpPath, resolved); err != nil {
		return fmt.Errorf("replace executable %s: %w", resolved, err)
	}
	keep = true
	return nil
}

func (c *Client) download(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	setHeaders(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError("download update", resp)
	}
	if resp.ContentLength > maxBinarySize {
		return fmt.Errorf("downloaded update exceeds %d bytes", maxBinarySize)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, maxBinarySize+1))
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	if n > maxBinarySize {
		return fmt.Errorf("downloaded update exceeds %d bytes", maxBinarySize)
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dmux-verify-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

func responseError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return fmt.Errorf("%s: HTTP %s", action, resp.Status)
	}
	return fmt.Errorf("%s: HTTP %s: %s", action, resp.Status, detail)
}

func parseSHA256(value string) (string, error) {
	algorithm, digest, ok := strings.Cut(value, ":")
	if !ok || !strings.EqualFold(algorithm, "sha256") {
		return "", fmt.Errorf("missing SHA-256 digest")
	}
	digest = strings.ToLower(digest)
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("invalid SHA-256 digest")
	}
	return digest, nil
}

// CompareVersions compares semantic release versions. It returns -1 when
// left is older, 0 when they are equal, and 1 when left is newer.
func CompareVersions(left, right string) (int, error) {
	l, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	r, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for i := range l.numbers {
		if l.numbers[i] < r.numbers[i] {
			return -1, nil
		}
		if l.numbers[i] > r.numbers[i] {
			return 1, nil
		}
	}
	if l.prerelease == r.prerelease {
		return 0, nil
	}
	if l.prerelease == "" {
		return 1, nil
	}
	if r.prerelease == "" {
		return -1, nil
	}
	return strings.Compare(l.prerelease, r.prerelease), nil
}

type version struct {
	numbers    [3]int
	prerelease string
}

func parseVersion(value string) (version, error) {
	original := value
	value = strings.TrimPrefix(value, "v")
	value, _, _ = strings.Cut(value, "+")
	core, prerelease, _ := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, fmt.Errorf("invalid version %q", original)
	}
	var parsed version
	parsed.prerelease = prerelease
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return version{}, fmt.Errorf("invalid version %q", original)
		}
		parsed.numbers[i] = n
	}
	return parsed, nil
}
