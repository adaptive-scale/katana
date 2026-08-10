// Package update replaces the running katana binary with a newer published
// release, and tells the user when one exists.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultRepo is the GitHub repository katana releases are published to.
	DefaultRepo = "adaptive-scale/katana"

	// defaultAPIBase is GitHub's REST API. Assets are fetched through it rather
	// than through github.com so a token can reach a private repository.
	defaultAPIBase = "https://api.github.com"

	// binaryName is both the installed command and the prefix of every release
	// asset, which the `release` make target names
	// katana_<tag>_<os>_<arch>[.exe].
	binaryName = "katana"

	// checksumsAsset is the sha256 manifest published beside the binaries. It
	// is optional: a release without one still installs.
	checksumsAsset = "checksums.txt"
)

// ErrNoRelease reports that the repository has no release matching the request.
var ErrNoRelease = errors.New("no published release found")

// ErrNoAsset reports that a release exists but publishes no binary for this
// operating system and architecture.
var ErrNoAsset = errors.New("no release binary for this platform")

// Client reads katana releases from the GitHub API and installs them.
type Client struct {
	// Repo is the "owner/name" the releases live in.
	Repo string
	// Current is the version of the running binary.
	Current string
	// APIBase is the GitHub REST API root.
	APIBase string
	// Token authenticates API calls. It is required only for a private
	// repository, whose releases are otherwise indistinguishable from a
	// repository that does not exist.
	Token string
	// Dest is the binary to replace. Empty means the running executable.
	Dest string

	OS, Arch string
	HTTP     *http.Client
}

// New returns a client for the running binary, reading GitHub credentials from
// the environment so private repositories work without extra flags.
func New(current string) *Client {
	base := defaultAPIBase
	if v := strings.TrimSpace(os.Getenv("KATANA_GITHUB_API")); v != "" {
		base = strings.TrimSuffix(v, "/")
	}
	return &Client{
		Repo:    DefaultRepo,
		Current: current,
		APIBase: base,
		Token:   envToken(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
	}
}

func envToken() string {
	for _, key := range []string{"KATANA_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// Release is one published katana release.
type Release struct {
	Tag    string  `json:"tag_name"`
	URL    string  `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset is a file attached to a release.
type Asset struct {
	Name string `json:"name"`
	// APIURL serves the bytes to authenticated clients and is the only way to
	// reach an asset of a private repository.
	APIURL      string `json:"url"`
	DownloadURL string `json:"browser_download_url"`
}

// Latest returns the newest published release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	return c.release(ctx, c.APIBase+"/repos/"+c.Repo+"/releases/latest")
}

// ByTag returns the release published under tag.
func (c *Client) ByTag(ctx context.Context, tag string) (Release, error) {
	return c.release(ctx, c.APIBase+"/repos/"+c.Repo+"/releases/tags/"+tag)
}

// Outdated reports whether rel is newer than the running binary.
func (c *Client) Outdated(rel Release) bool {
	return Compare(c.Current, rel.Tag) < 0
}

func (c *Client) release(ctx context.Context, url string) (Release, error) {
	body, err := c.get(ctx, url, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	defer body.Close()

	var rel Release
	if err := json.NewDecoder(body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("reading release: %w", err)
	}
	if rel.Tag == "" {
		return Release{}, ErrNoRelease
	}
	return rel, nil
}

// get performs an authenticated GET and returns the response body, which the
// caller closes.
func (c *Client) get(ctx context.Context, url, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", binaryName+"/"+c.Current)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, c.statusError(resp.StatusCode, url)
	}
	return resp.Body, nil
}

// statusError turns a failed request into advice. A 404 is the interesting
// case: GitHub returns it both for a repository that does not exist and for one
// the caller cannot see, so an unauthenticated 404 is most often a missing
// token rather than a missing release.
func (c *Client) statusError(code int, url string) error {
	switch {
	case code == http.StatusNotFound && c.Token == "":
		return fmt.Errorf("%w for %s: if the repository is private, set GITHUB_TOKEN", ErrNoRelease, c.Repo)
	case code == http.StatusNotFound:
		return fmt.Errorf("%w for %s", ErrNoRelease, c.Repo)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("github refused the request (%s): check GITHUB_TOKEN and its scopes", http.StatusText(code))
	}
	return fmt.Errorf("GET %s: unexpected status %d", url, code)
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// assetFor picks rel's binary for this platform. It prefers the exact
// katana_<tag>_<os>_<arch> name the release target produces, then falls back to
// any asset carrying the platform suffix so a release stamped with a different
// version string still installs.
func (c *Client) assetFor(rel Release) (Asset, bool) {
	suffix := fmt.Sprintf("_%s_%s%s", c.OS, c.Arch, exeExt(c.OS))
	exact := binaryName + "_" + rel.Tag + suffix
	for _, a := range rel.Assets {
		if a.Name == exact {
			return a, true
		}
	}
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, binaryName+"_") && strings.HasSuffix(a.Name, suffix) {
			return a, true
		}
	}
	return Asset{}, false
}

func (rel Release) asset(name string) (Asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Apply installs rel over the running binary and returns the path it wrote.
// Progress goes to log, which may be nil.
func (c *Client) Apply(ctx context.Context, rel Release, log io.Writer) (string, error) {
	asset, ok := c.assetFor(rel)
	if !ok {
		return "", fmt.Errorf("%w: %s/%s at %s", ErrNoAsset, c.OS, c.Arch, rel.Tag)
	}
	dest, err := c.destination()
	if err != nil {
		return "", err
	}

	// Staging inside the destination's directory keeps the download on the
	// same filesystem, so the swap at the end is a rename and never a
	// half-written binary.
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+binaryName+"-update-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("cannot write to %s: re-run with sudo, or install elsewhere with the installer's --dir", filepath.Dir(dest))
		}
		return "", err
	}
	staged := tmp.Name()
	defer os.Remove(staged)

	logf(log, "==> downloading %s %s (%s/%s)\n", binaryName, rel.Tag, c.OS, c.Arch)
	sum, err := c.download(ctx, asset, tmp)
	tmp.Close()
	if err != nil {
		return "", err
	}

	if err := c.verify(ctx, rel, asset, sum, log); err != nil {
		return "", err
	}
	if err := replace(dest, staged); err != nil {
		return "", fmt.Errorf("installing to %s: %w", dest, err)
	}
	return dest, nil
}

// download copies the asset into w and returns its sha256.
func (c *Client) download(ctx context.Context, a Asset, w io.Writer) (string, error) {
	// The API endpoint is the only one that honours credentials, so prefer it
	// whenever katana has a token; browser_download_url is the fallback for
	// public releases and for API responses that omit the asset URL.
	url := a.DownloadURL
	if url == "" || (c.Token != "" && a.APIURL != "") {
		url = a.APIURL
	}
	if url == "" {
		return "", fmt.Errorf("release asset %s has no download url", a.Name)
	}

	body, err := c.get(ctx, url, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", a.Name, err)
	}
	defer body.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), body); err != nil {
		return "", fmt.Errorf("downloading %s: %w", a.Name, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verify checks the download against the release's checksum manifest. A
// release that publishes no manifest, or lists no line for this asset, is
// installed unverified — the same latitude install.sh allows.
func (c *Client) verify(ctx context.Context, rel Release, a Asset, sum string, log io.Writer) error {
	manifest, ok := rel.asset(checksumsAsset)
	if !ok {
		logf(log, "==> release publishes no %s, skipping checksum verification\n", checksumsAsset)
		return nil
	}

	var buf strings.Builder
	if _, err := c.download(ctx, manifest, &buf); err != nil {
		return err
	}
	want, ok := checksumFor(buf.String(), a.Name)
	if !ok {
		logf(log, "==> %s lists no entry for %s, skipping checksum verification\n", checksumsAsset, a.Name)
		return nil
	}
	if want != sum {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", a.Name, want, sum)
	}
	logf(log, "==> checksum verified\n")
	return nil
}

// checksumFor finds name's digest in a sha256sum-format manifest, whose lines
// are "<digest>  <name>" with an optional "*" marking binary mode.
func checksumFor(manifest, name string) (string, bool) {
	for line := range strings.Lines(manifest) {
		digest, file, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || len(digest) != sha256.Size*2 {
			continue
		}
		if strings.TrimPrefix(strings.TrimSpace(file), "*") == name {
			return strings.ToLower(digest), true
		}
	}
	return "", false
}

// destination is the file Apply replaces.
func (c *Client) destination() (string, error) {
	if c.Dest != "" {
		return c.Dest, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the running binary: %w", err)
	}
	// Resolve symlinks so an update rewrites the real binary rather than
	// turning a package manager's link into a regular file.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// replace swaps dest for the staged binary at src. On Unix the rename is
// atomic and safe while the old binary is running: the process holds the old
// inode open and finishes on it.
func replace(dest, src string) error {
	if err := os.Chmod(src, 0o755); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(src, dest)
	}
	// Windows refuses to overwrite a running image, so move it aside first and
	// put it back if the swap fails.
	old := dest + ".old"
	os.Remove(old)
	if err := os.Rename(dest, old); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err != nil {
		os.Rename(old, dest)
		return err
	}
	// Deleting the displaced image fails while it is still mapped; the next
	// update cleans it up.
	os.Remove(old)
	return nil
}

func exeExt(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

func logf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format, args...)
}
