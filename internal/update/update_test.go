package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release serves a fake GitHub release: the API endpoints, one binary asset per
// platform and a checksum manifest.
type release struct {
	tag    string
	assets map[string][]byte // asset name -> body
}

func newRelease(t *testing.T, tag string, binaries map[string][]byte) (*release, *httptest.Server) {
	t.Helper()
	r := &release{tag: tag, assets: map[string][]byte{}}
	var sums strings.Builder
	for name, body := range binaries {
		r.assets[name] = body
		sum := sha256.Sum256(body)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	r.assets[checksumsAsset] = []byte(sums.String())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/repos/o/r/releases/latest",
			req.URL.Path == "/repos/o/r/releases/tags/"+tag:
			json.NewEncoder(w).Encode(r.payload(req.Host))
		case strings.HasPrefix(req.URL.Path, "/assets/"):
			body, ok := r.assets[strings.TrimPrefix(req.URL.Path, "/assets/")]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Write(body)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *release) payload(host string) Release {
	rel := Release{Tag: r.tag, URL: "https://example.test/releases/" + r.tag}
	for name := range r.assets {
		rel.Assets = append(rel.Assets, Asset{
			Name:        name,
			APIURL:      "http://" + host + "/assets/" + name,
			DownloadURL: "http://" + host + "/assets/" + name,
		})
	}
	return rel
}

func testClient(t *testing.T, srv *httptest.Server, current string) *Client {
	t.Helper()
	c := New(current)
	c.Repo = "o/r"
	c.APIBase = srv.URL
	c.Token = ""
	c.OS, c.Arch = "darwin", "arm64"
	return c
}

func TestApplyReplacesBinary(t *testing.T) {
	want := []byte("new katana binary")
	_, srv := newRelease(t, "v1.2.0", map[string][]byte{
		"katana_v1.2.0_darwin_arm64": want,
		"katana_v1.2.0_linux_amd64":  []byte("wrong platform"),
	})

	dir := t.TempDir()
	dest := filepath.Join(dir, "katana")
	if err := os.WriteFile(dest, []byte("old katana binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := testClient(t, srv, "v1.1.0")
	c.Dest = dest

	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Tag != "v1.2.0" {
		t.Fatalf("tag = %q, want v1.2.0", rel.Tag)
	}
	if !c.Outdated(rel) {
		t.Fatal("Outdated = false, want true for v1.1.0 against v1.2.0")
	}

	var log strings.Builder
	path, err := c.Apply(context.Background(), rel, &log)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if path != dest {
		t.Errorf("installed to %q, want %q", path, dest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("installed body = %q, want %q", got, want)
	}
	if !strings.Contains(log.String(), "checksum verified") {
		t.Errorf("log did not report checksum verification:\n%s", log.String())
	}

	// The staged download must not survive the swap.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".katana-update-") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
}

func TestApplyRejectsBadChecksum(t *testing.T) {
	r, srv := newRelease(t, "v1.2.0", map[string][]byte{
		"katana_v1.2.0_darwin_arm64": []byte("new katana binary"),
	})
	// Corrupt the asset after the manifest was computed over the original.
	r.assets["katana_v1.2.0_darwin_arm64"] = []byte("tampered")

	dest := filepath.Join(t.TempDir(), "katana")
	if err := os.WriteFile(dest, []byte("old katana binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := testClient(t, srv, "v1.1.0")
	c.Dest = dest
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, err := c.Apply(context.Background(), rel, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Apply error = %v, want a checksum mismatch", err)
	}
	// A rejected download must leave the working binary alone.
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old katana binary" {
		t.Errorf("binary was replaced despite the bad checksum: %q", got)
	}
}

func TestApplyReportsMissingPlatformAsset(t *testing.T) {
	_, srv := newRelease(t, "v1.2.0", map[string][]byte{
		"katana_v1.2.0_linux_amd64": []byte("linux only"),
	})
	c := testClient(t, srv, "v1.1.0")
	c.Dest = filepath.Join(t.TempDir(), "katana")

	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, err := c.Apply(context.Background(), rel, io.Discard); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("Apply error = %v, want ErrNoAsset", err)
	}
}

func TestByTagAndUnknownTag(t *testing.T) {
	_, srv := newRelease(t, "v1.2.0", map[string][]byte{
		"katana_v1.2.0_darwin_arm64": []byte("body"),
	})
	c := testClient(t, srv, "v1.2.0")

	rel, err := c.ByTag(context.Background(), "v1.2.0")
	if err != nil {
		t.Fatalf("ByTag: %v", err)
	}
	if rel.Tag != "v1.2.0" {
		t.Errorf("tag = %q, want v1.2.0", rel.Tag)
	}
	if c.Outdated(rel) {
		t.Error("Outdated = true, want false when running the newest tag")
	}

	// Without a token a 404 is ambiguous, so the error points at the likely
	// cause rather than claiming the release does not exist.
	_, err = c.ByTag(context.Background(), "v9.9.9")
	if !errors.Is(err, ErrNoRelease) {
		t.Fatalf("ByTag error = %v, want ErrNoRelease", err)
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("error %q does not mention GITHUB_TOKEN", err)
	}
}

func TestAssetForFallsBackToPlatformSuffix(t *testing.T) {
	c := &Client{OS: "darwin", Arch: "arm64"}
	rel := Release{Tag: "v1.2.0", Assets: []Asset{
		{Name: "katana_1.2.0_darwin_arm64"}, // stamped without the "v"
		{Name: "katana_1.2.0_darwin_amd64"},
		{Name: checksumsAsset},
	}}
	got, ok := c.assetFor(rel)
	if !ok || got.Name != "katana_1.2.0_darwin_arm64" {
		t.Fatalf("assetFor = %q, %v; want the darwin/arm64 asset", got.Name, ok)
	}

	// An exact name wins over the suffix fallback.
	rel.Assets = append(rel.Assets, Asset{Name: "katana_v1.2.0_darwin_arm64"})
	if got, _ := c.assetFor(rel); got.Name != "katana_v1.2.0_darwin_arm64" {
		t.Errorf("assetFor = %q, want the exactly named asset", got.Name)
	}

	c.OS, c.Arch = "windows", "amd64"
	rel.Assets = append(rel.Assets, Asset{Name: "katana_v1.2.0_windows_amd64.exe"})
	if got, _ := c.assetFor(rel); got.Name != "katana_v1.2.0_windows_amd64.exe" {
		t.Errorf("assetFor = %q, want the windows asset", got.Name)
	}
}

func TestChecksumFor(t *testing.T) {
	digest := strings.Repeat("a", 64)
	manifest := "not a checksum line\n" +
		digest + "  katana_v1.0.0_linux_amd64\n" +
		strings.Repeat("b", 64) + " *katana_v1.0.0_darwin_arm64\n"

	if got, ok := checksumFor(manifest, "katana_v1.0.0_linux_amd64"); !ok || got != digest {
		t.Errorf("checksumFor = %q, %v; want %q", got, ok, digest)
	}
	// shasum's binary-mode "*" prefix is part of the format, not the name.
	if got, ok := checksumFor(manifest, "katana_v1.0.0_darwin_arm64"); !ok || got != strings.Repeat("b", 64) {
		t.Errorf("checksumFor (binary mode) = %q, %v", got, ok)
	}
	if _, ok := checksumFor(manifest, "katana_v1.0.0_windows_amd64.exe"); ok {
		t.Error("checksumFor found an entry that is not in the manifest")
	}
}
