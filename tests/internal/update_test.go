// This file covers behaviors/internal/update.md: katana's self-update
// machinery — comparing version strings, recognising a locally built binary,
// reading releases from GitHub, picking and downloading this platform's binary,
// verifying it, swapping it in, and the once-a-day "a newer version exists"
// hint a finished command prints.
//
// Everything goes through the update package's exported API: Compare,
// IsDevBuild, New, Client (Latest, ByTag, Outdated, Apply), Start and
// Notifier.Notice. GitHub is stood in for by an httptest server that serves one
// release and records every request, so which endpoint katana asked for, with
// which headers, and which of an asset's two urls it chose are all observable
// from outside the package.
//
// Two behaviors can only be seen by being the running binary katana replaces —
// the destination it picks when the caller names none, and resolving a symlink
// before rewriting it. Those tests copy this test binary somewhere disposable
// and re-run it as a katana that updates itself (see
// TestUpdateInstallHelperProcess), the same trick the discovery tests use to
// stand in for a coding agent.

package internal

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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/update"
)

// --- The release every install test installs -------------------------------

const (
	// The platform the asset-naming tests pretend to run on, so asset names can
	// be written out in full whatever machine the suite runs on.
	updateOS   = "darwin"
	updateArch = "arm64"

	updateTag     = "v1.2.0"
	updateCurrent = "v1.1.0"
	updateAsset   = "katana_" + updateTag + "_" + updateOS + "_" + updateArch

	checksumsFile = "checksums.txt"

	newKatana = "the new katana binary"
	oldKatana = "the binary that is running"

	// Where the fake GitHub serves assets from. The two prefixes are what makes
	// katana's choice between an asset's api url and its public download url
	// visible in the request log.
	apiAssets     = "/api/assets/"
	browserAssets = "/downloads/"

	// releasePage is the human-facing page of the served release.
	releasePage = "https://example.test/releases/"
)

// releaseAsset is one file attached to the fake release.
type releaseAsset struct {
	name string
	body string
	// status answers requests for this asset with a failure instead of its body.
	status int
	// noAPIURL and noBrowserURL leave that url out of the release payload, which
	// is how katana's choice between them is observed.
	noAPIURL     bool
	noBrowserURL bool
}

// githubOptions describes the fake GitHub before it starts serving. Everything
// is settled up front so no test writes to the server's own state while a
// request is being handled.
type githubOptions struct {
	tag    string
	assets []releaseAsset
	// releaseStatus answers the release endpoints with a failure, and
	// releaseBody answers them with a body of the test's choosing.
	releaseStatus int
	releaseBody   string
	// hold blocks every release request until the test lets it go, so a check
	// still in flight can be looked at.
	hold bool
	// onAsset runs while an asset request is being served, which is the only
	// moment the staged download exists.
	onAsset func(name string)
}

// seenRequest is one request the fake GitHub was asked for.
type seenRequest struct {
	path   string
	header http.Header
}

// fakeGitHub stands in for the GitHub REST API, serving exactly one release.
type fakeGitHub struct {
	t    *testing.T
	srv  *httptest.Server
	opts githubOptions

	held    chan struct{}
	release func() // lets a held request go; nil unless the release is held

	mu   sync.Mutex
	seen []seenRequest
}

func newFakeGitHub(t *testing.T, o githubOptions) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{t: t, opts: o}
	if o.hold {
		g.held = make(chan struct{})
		var once sync.Once
		g.release = func() { once.Do(func() { close(g.held) }) }
	}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	if g.release != nil {
		// Registered after the server's own cleanup, so it runs first: closing
		// the server waits for a request nothing has let go of.
		t.Cleanup(g.release)
	}
	return g
}

func (g *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.seen = append(g.seen, seenRequest{path: r.URL.Path, header: r.Header.Clone()})
	g.mu.Unlock()

	switch {
	case strings.HasSuffix(r.URL.Path, "/releases/latest"),
		g.opts.tag != "" && strings.HasSuffix(r.URL.Path, "/releases/tags/"+g.opts.tag):
		g.serveRelease(w, r)
	case strings.HasPrefix(r.URL.Path, apiAssets), strings.HasPrefix(r.URL.Path, browserAssets):
		g.serveAsset(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (g *fakeGitHub) serveRelease(w http.ResponseWriter, r *http.Request) {
	if g.held != nil {
		select {
		case <-g.held:
		case <-r.Context().Done(): // katana gave up on us
			return
		}
	}
	switch {
	case g.opts.releaseStatus != 0:
		http.Error(w, http.StatusText(g.opts.releaseStatus), g.opts.releaseStatus)
	case g.opts.releaseBody != "":
		io.WriteString(w, g.opts.releaseBody)
	default:
		json.NewEncoder(w).Encode(g.payload())
	}
}

func (g *fakeGitHub) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, apiAssets), browserAssets)
	if g.opts.onAsset != nil {
		g.opts.onAsset(name)
	}
	for _, a := range g.opts.assets {
		if a.name != name {
			continue
		}
		if a.status != 0 {
			http.Error(w, http.StatusText(a.status), a.status)
			return
		}
		io.WriteString(w, a.body)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (g *fakeGitHub) payload() update.Release {
	rel := update.Release{Tag: g.opts.tag, URL: releasePage + g.opts.tag}
	for _, a := range g.opts.assets {
		asset := update.Asset{Name: a.name}
		if !a.noAPIURL {
			asset.APIURL = g.srv.URL + apiAssets + a.name
		}
		if !a.noBrowserURL {
			asset.DownloadURL = g.srv.URL + browserAssets + a.name
		}
		rel.Assets = append(rel.Assets, asset)
	}
	return rel
}

func (g *fakeGitHub) requests() []seenRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]seenRequest(nil), g.seen...)
}

// asked reports whether any request was made for a path holding sub.
func (g *fakeGitHub) asked(sub string) bool {
	for _, r := range g.requests() {
		if strings.Contains(r.path, sub) {
			return true
		}
	}
	return false
}

// --- Fixtures --------------------------------------------------------------

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// manifestLine is one line of a sha256sum-format checksums.txt.
func manifestLine(body, name string) string { return digestOf(body) + "  " + name + "\n" }

// binaryRelease is the ordinary release: this platform's binary and a manifest
// that matches it.
func binaryRelease(t *testing.T) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, githubOptions{
		tag: updateTag,
		assets: []releaseAsset{
			{name: updateAsset, body: newKatana},
			{name: checksumsFile, body: manifestLine(newKatana, updateAsset)},
		},
	})
}

// manifestRelease is that release with a checksums.txt of the test's choosing,
// which is how the manifest's format rules are observed.
func manifestRelease(t *testing.T, manifest string) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, githubOptions{
		tag: updateTag,
		assets: []releaseAsset{
			{name: updateAsset, body: newKatana},
			{name: checksumsFile, body: manifest},
		},
	})
}

// assetsRelease is a release publishing exactly the given assets, for the rules
// about which one katana picks.
func assetsRelease(t *testing.T, assets ...releaseAsset) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, githubOptions{tag: updateTag, assets: assets})
}

// failingGitHub answers every release request with a failure.
func failingGitHub(t *testing.T, status int) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, githubOptions{tag: updateTag, releaseStatus: status})
}

// rawGitHub answers every release request with a body of the test's choosing.
func rawGitHub(t *testing.T, body string) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, githubOptions{tag: updateTag, releaseBody: body})
}

// clearGitHubEnv takes a developer machine's own GitHub credentials and API
// override out of the picture.
func clearGitHubEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"KATANA_GITHUB_API", "KATANA_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
}

// updateClient points a client at the fake GitHub. The repository is left at
// katana's own so error messages read as the specification states, and the
// platform is fixed so asset names can be written out in full.
func updateClient(t *testing.T, g *fakeGitHub, current string) *update.Client {
	t.Helper()
	clearGitHubEnv(t)
	c := update.New(current)
	c.APIBase = g.srv.URL
	c.OS, c.Arch = updateOS, updateArch
	return c
}

// updateDest writes the binary katana will replace into a directory of its own
// and returns its path.
func updateDest(t *testing.T) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "katana")
	if err := os.WriteFile(dest, []byte(oldKatana), 0o755); err != nil {
		t.Fatal(err)
	}
	return dest
}

// installer is a client that installs the fake release over a disposable
// binary, plus the path to that binary.
func installer(t *testing.T, g *fakeGitHub, current string) (*update.Client, string) {
	t.Helper()
	c := updateClient(t, g, current)
	c.Dest = updateDest(t)
	return c, c.Dest
}

func latestRelease(t *testing.T, c *update.Client) update.Release {
	t.Helper()
	rel, err := c.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	return rel
}

// installLatest installs the fake release, failing the test if it does not
// install, and returns the path katana reported writing.
func installLatest(t *testing.T, c *update.Client, log io.Writer) string {
	t.Helper()
	path, err := c.Apply(context.Background(), latestRelease(t, c), log)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return path
}

// installErr returns the error an install fails with, failing the test if it
// succeeds.
func installErr(t *testing.T, c *update.Client, log io.Writer) error {
	t.Helper()
	path, err := c.Apply(context.Background(), latestRelease(t, c), log)
	if err == nil {
		t.Fatalf("Apply succeeded, want an error; it wrote %s", path)
	}
	return err
}

func installedBody(t *testing.T, dest string) string {
	t.Helper()
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading the installed binary: %v", err)
	}
	return string(body)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func hasPrefixIn(names []string, prefix string) bool {
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// --- Comparing two version strings -----------------------------------------

// assertOlder states that a comes before b, and that the comparison says the
// same thing when the two are swapped.
func assertOlder(t *testing.T, a, b string) {
	t.Helper()
	if got := update.Compare(a, b); got != -1 {
		t.Errorf("Compare(%q, %q) = %d, want -1", a, b, got)
	}
	if got := update.Compare(b, a); got != 1 {
		t.Errorf("Compare(%q, %q) = %d, want 1", b, a, got)
	}
}

func assertSamePrecedence(t *testing.T, a, b string) {
	t.Helper()
	if got := update.Compare(a, b); got != 0 {
		t.Errorf("Compare(%q, %q) = %d, want 0", a, b, got)
	}
	if got := update.Compare(b, a); got != 0 {
		t.Errorf("Compare(%q, %q) = %d, want 0", b, a, got)
	}
}

// unparseable is a version katana cannot read, used to observe the rules about
// versions it cannot read: they compare equal to each other.
const unparseable = "not-a-version"

// assertUnparseable states that v cannot be parsed. From outside the package
// that shows as two facts at once: v sorts before a release, and it compares
// equal to another version that cannot be parsed either — which no parseable
// version does.
func assertUnparseable(t *testing.T, v string) {
	t.Helper()
	if got := update.Compare(v, "0.0.0"); got != -1 {
		t.Errorf("Compare(%q, %q) = %d, want -1: %q should not parse", v, "0.0.0", got, v)
	}
	if got := update.Compare(v, unparseable); got != 0 {
		t.Errorf("Compare(%q, %q) = %d, want 0: %q should not parse", v, unparseable, got, v)
	}
}

func TestComparingVersionsReportsWhetherTheFirstIsOlderEqualOrNewer(t *testing.T) {
	if got := update.Compare("1.0.0", "2.0.0"); got != -1 {
		t.Errorf("Compare(1.0.0, 2.0.0) = %d, want -1", got)
	}
	if got := update.Compare("2.0.0", "2.0.0"); got != 0 {
		t.Errorf("Compare(2.0.0, 2.0.0) = %d, want 0", got)
	}
	if got := update.Compare("2.0.0", "1.0.0"); got != 1 {
		t.Errorf("Compare(2.0.0, 1.0.0) = %d, want 1", got)
	}
}

func TestALeadingVIsIgnoredWhenComparingVersions(t *testing.T) {
	assertSamePrecedence(t, "v1.2.3", "1.2.3")
	assertOlder(t, "v1.2.3", "1.2.4")
}

func TestBuildMetadataIsIgnoredWhenComparingVersions(t *testing.T) {
	assertSamePrecedence(t, "1.2.3+build9", "1.2.3")
	assertSamePrecedence(t, "1.2.3+build9", "1.2.3+build10")
}

func TestSurroundingWhitespaceIsIgnoredWhenComparingVersions(t *testing.T) {
	assertSamePrecedence(t, "  1.2.3\n", "1.2.3")
	assertOlder(t, " 1.2.3 ", "\t1.2.4\t")
}

func TestMissingTrailingNumberSegmentsCountAsZero(t *testing.T) {
	assertSamePrecedence(t, "1.2", "1.2.0")
	assertSamePrecedence(t, "1", "1.0.0")
	assertOlder(t, "1.2", "1.2.1")
}

func TestMoreThanThreeNumberSegmentsAreComparedSegmentBySegment(t *testing.T) {
	assertOlder(t, "1.2.3.4", "1.2.3.5")
	assertOlder(t, "1.2.3", "1.2.3.1")
	assertSamePrecedence(t, "1.2.3.0", "1.2.3")
}

func TestAVersionWhoseSegmentsAreNotWholeNumbersCannotBeParsed(t *testing.T) {
	for _, v := range []string{
		"1.2.x",     // a segment that is not a number
		"1.two.3",   // spelled out
		"1..3",      // a segment with nothing in it
		"abcdef123", // a bare commit sha
		"1.2.-3",    // a negative segment
	} {
		t.Run(v, func(t *testing.T) { assertUnparseable(t, v) })
	}
}

func TestAVersionWithNoNumberSegmentsAtAllCannotBeParsed(t *testing.T) {
	for _, v := range []string{"v", "-rc.1", "-dirty", ""} {
		t.Run("version "+v, func(t *testing.T) { assertUnparseable(t, v) })
	}
}

func TestAnUnparseableVersionSortsBeforeEveryRelease(t *testing.T) {
	// An unstamped build is therefore always treated as behind a release, even
	// the oldest prerelease of the oldest version.
	assertOlder(t, unparseable, "0.0.1")
	assertOlder(t, unparseable, "0.0.0-rc.1")
	assertOlder(t, "", "v1.0.0")
}

func TestTwoUnparseableVersionsCompareAsEqual(t *testing.T) {
	assertSamePrecedence(t, unparseable, "also not a version")
	assertSamePrecedence(t, "", "abcdef1")
}

func TestAFinalReleaseOutranksItsPrereleases(t *testing.T) {
	assertOlder(t, "1.2.0-rc.1", "1.2.0")
	assertOlder(t, "1.2.0-alpha", "1.2.0")
}

func TestPrereleaseIdentifiersAreComparedLeftToRight(t *testing.T) {
	// The first identifier that differs decides, whatever follows it.
	assertOlder(t, "1.0.0-alpha.99", "1.0.0-beta.1")
	// Identical leading identifiers hand the decision to the next one.
	assertOlder(t, "1.0.0-rc.2", "1.0.0-rc.3")
}

func TestTwoNumericPrereleaseIdentifiersCompareNumerically(t *testing.T) {
	// Not as text, which would put rc.10 before rc.2.
	assertOlder(t, "1.0.0-rc.2", "1.0.0-rc.10")
}

func TestANumericPrereleaseIdentifierRanksBelowANonNumericOne(t *testing.T) {
	assertOlder(t, "1.0.0-1", "1.0.0-alpha")
	assertOlder(t, "1.0.0-rc.1", "1.0.0-rc.alpha")
}

func TestTwoNonNumericPrereleaseIdentifiersCompareAsText(t *testing.T) {
	assertOlder(t, "1.0.0-alpha", "1.0.0-beta")
	assertOlder(t, "1.0.0-rc", "1.0.0-release")
}

func TestAPrereleaseWithFewerIdentifiersIsOlderThanTheOneItPrefixes(t *testing.T) {
	assertOlder(t, "1.0.0-rc", "1.0.0-rc.1")
	assertOlder(t, "1.0.0-alpha.1", "1.0.0-alpha.1.2")
}

// --- Recognising a locally built binary ------------------------------------

func assertDevBuild(t *testing.T, v string) {
	t.Helper()
	if !update.IsDevBuild(v) {
		t.Errorf("IsDevBuild(%q) = false, want true", v)
	}
}

func TestAnEmptyOrWhitespaceOnlyVersionIsADevelopmentBuild(t *testing.T) {
	assertDevBuild(t, "")
	assertDevBuild(t, "   ")
	assertDevBuild(t, "\t\n ")
}

func TestTheVersionDevIsADevelopmentBuild(t *testing.T) {
	assertDevBuild(t, "dev")
}

func TestAGitDescribeCommitSuffixMarksADevelopmentBuild(t *testing.T) {
	assertDevBuild(t, "v1.2.3-4-gab12cd")
	assertDevBuild(t, "1.0.0-12-gabcdef12")

	// Four hexadecimal characters is the least that counts as a commit; three
	// leaves an ordinary prerelease.
	assertDevBuild(t, "v1.2.3-4-gab12")
	if update.IsDevBuild("v1.2.3-4-gab1") {
		t.Error("IsDevBuild(v1.2.3-4-gab1) = true, want false: three characters is not a commit suffix")
	}
}

func TestAVersionEndingInDirtyIsADevelopmentBuild(t *testing.T) {
	assertDevBuild(t, "v1.2.3-dirty")
	assertDevBuild(t, "v1.2.3-4-gab12cd-dirty")
}

func TestAVersionThatCannotBeParsedIsADevelopmentBuild(t *testing.T) {
	assertDevBuild(t, unparseable)
	assertDevBuild(t, "abcdef1")
	assertDevBuild(t, "1.2.x")
}

func TestAPublishedVersionOrPrereleaseIsNotADevelopmentBuild(t *testing.T) {
	for _, v := range []string{"v1.2.3", "1.2.3", "v1.2.3-rc.1", "v1.2.3+build9"} {
		if update.IsDevBuild(v) {
			t.Errorf("IsDevBuild(%q) = true, want false", v)
		}
	}
}

// --- Where releases are read from ------------------------------------------

func TestReleasesAreReadFromTheKatanaRepositoryByDefault(t *testing.T) {
	clearGitHubEnv(t)

	if update.DefaultRepo != "adaptive-scale/katana" {
		t.Errorf("DefaultRepo = %q, want adaptive-scale/katana", update.DefaultRepo)
	}
	if got := update.New("v1.0.0").Repo; got != "adaptive-scale/katana" {
		t.Errorf("Repo = %q, want adaptive-scale/katana", got)
	}
}

func TestRequestsGoToTheGithubApiUnlessTheEnvironmentNamesAnother(t *testing.T) {
	clearGitHubEnv(t)
	if got := update.New("v1.0.0").APIBase; got != "https://api.github.com" {
		t.Errorf("APIBase = %q, want https://api.github.com", got)
	}

	t.Setenv("KATANA_GITHUB_API", "https://ghe.example.com/api/v3")
	if got := update.New("v1.0.0").APIBase; got != "https://ghe.example.com/api/v3" {
		t.Errorf("APIBase = %q, want the environment's value", got)
	}
}

func TestTheApiBaseFromTheEnvironmentLosesSurroundingSpaceAndATrailingSlash(t *testing.T) {
	clearGitHubEnv(t)
	t.Setenv("KATANA_GITHUB_API", "  https://ghe.example.com/api/v3/  ")

	if got := update.New("v1.0.0").APIBase; got != "https://ghe.example.com/api/v3" {
		t.Errorf("APIBase = %q, want https://ghe.example.com/api/v3", got)
	}
}

func TestTheTokenIsTakenFromTheFirstNonEmptyOfTheThreeVariables(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"katana's own comes first", map[string]string{
			"KATANA_GITHUB_TOKEN": "katana", "GITHUB_TOKEN": "github", "GH_TOKEN": "gh",
		}, "katana"},
		{"then GITHUB_TOKEN", map[string]string{
			"GITHUB_TOKEN": "github", "GH_TOKEN": "gh",
		}, "github"},
		{"then GH_TOKEN", map[string]string{"GH_TOKEN": "gh"}, "gh"},
		{"an empty one is passed over", map[string]string{
			"KATANA_GITHUB_TOKEN": "", "GITHUB_TOKEN": "  ", "GH_TOKEN": "gh",
		}, "gh"},
		{"surrounding whitespace is removed", map[string]string{
			"KATANA_GITHUB_TOKEN": "  spaced  ",
		}, "spaced"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearGitHubEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}

			if got := update.New("v1.0.0").Token; got != c.want {
				t.Errorf("Token = %q, want %q", got, c.want)
			}
		})
	}
}

func TestWithNoTokenConfiguredRequestsAreSentUnauthenticated(t *testing.T) {
	clearGitHubEnv(t)
	if got := update.New("v1.0.0").Token; got != "" {
		t.Errorf("Token = %q, want none", got)
	}

	g := binaryRelease(t)
	c, _ := installer(t, g, updateCurrent)
	installLatest(t, c, io.Discard)

	for _, r := range g.requests() {
		if auth := r.header.Get("Authorization"); auth != "" {
			t.Errorf("%s carried Authorization %q, want none", r.path, auth)
		}
	}
}

func TestAskingForTheLatestReleaseReadsTheLatestReleaseEndpoint(t *testing.T) {
	g := binaryRelease(t)
	c := updateClient(t, g, updateCurrent)

	if rel := latestRelease(t, c); rel.Tag != updateTag {
		t.Errorf("tag = %q, want %q", rel.Tag, updateTag)
	}

	want := "/repos/" + update.DefaultRepo + "/releases/latest"
	if !g.asked(want) {
		t.Errorf("requests = %+v, want one for %q", g.requests(), want)
	}
}

func TestAskingByTagReadsTheReleasePublishedUnderThatTag(t *testing.T) {
	g := binaryRelease(t)
	c := updateClient(t, g, updateCurrent)

	rel, err := c.ByTag(context.Background(), updateTag)
	if err != nil {
		t.Fatalf("ByTag: %v", err)
	}
	if rel.Tag != updateTag {
		t.Errorf("tag = %q, want %q", rel.Tag, updateTag)
	}

	want := "/repos/" + update.DefaultRepo + "/releases/tags/" + updateTag
	if !g.asked(want) {
		t.Errorf("requests = %+v, want one for %q", g.requests(), want)
	}
}

func TestEveryRequestAnnouncesKatanaAndTheGithubApiVersion(t *testing.T) {
	g := binaryRelease(t)
	c, _ := installer(t, g, updateCurrent)
	installLatest(t, c, io.Discard)

	seen := g.requests()
	if len(seen) < 2 {
		t.Fatalf("requests = %+v, want the release and its assets", seen)
	}
	for _, r := range seen {
		if got, want := r.header.Get("User-Agent"), "katana/"+updateCurrent; got != want {
			t.Errorf("%s announced itself as %q, want %q", r.path, got, want)
		}
		if got := r.header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("%s asked for API version %q, want 2022-11-28", r.path, got)
		}
	}
}

func TestABearerTokenIsSentOnlyWhenATokenWasFound(t *testing.T) {
	g := binaryRelease(t)
	c, _ := installer(t, g, updateCurrent)
	c.Token = "secret-token"

	installLatest(t, c, io.Discard)

	for _, r := range g.requests() {
		if got := r.header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("%s carried Authorization %q, want %q", r.path, got, "Bearer secret-token")
		}
	}
}

func TestARequestThatTakesLongerThanTwoMinutesIsAbandoned(t *testing.T) {
	// The bound is the client's own deadline, which covers both the API calls
	// and the download made through it.
	clearGitHubEnv(t)

	c := update.New("v1.0.0")

	if c.HTTP == nil {
		t.Fatal("no http client, so nothing bounds a request")
	}
	if c.HTTP.Timeout != 2*time.Minute {
		t.Errorf("timeout = %v, want 2m", c.HTTP.Timeout)
	}
}

// --- Errors reported when a release cannot be read -------------------------

func TestA404WithNoTokenSuggestsSettingGithubToken(t *testing.T) {
	g := failingGitHub(t, http.StatusNotFound)
	c := updateClient(t, g, updateCurrent)

	_, err := c.Latest(context.Background())

	if !errors.Is(err, update.ErrNoRelease) {
		t.Fatalf("error = %v, want ErrNoRelease", err)
	}
	want := "no published release found for adaptive-scale/katana: if the repository is private, set GITHUB_TOKEN"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestA404WithATokenReportsNoReleaseWithoutTheTokenAdvice(t *testing.T) {
	g := failingGitHub(t, http.StatusNotFound)
	c := updateClient(t, g, updateCurrent)
	c.Token = "secret-token"

	_, err := c.Latest(context.Background())

	if !errors.Is(err, update.ErrNoRelease) {
		t.Fatalf("error = %v, want ErrNoRelease", err)
	}
	want := "no published release found for adaptive-scale/katana"
	if err.Error() != want {
		t.Errorf("error = %q, want %q, with no advice about a token that is already set", err.Error(), want)
	}
}

func TestA401ReportsThatGithubRefusedTheRequest(t *testing.T) {
	g := failingGitHub(t, http.StatusUnauthorized)
	c := updateClient(t, g, updateCurrent)

	_, err := c.Latest(context.Background())

	want := "github refused the request (Unauthorized): check GITHUB_TOKEN and its scopes"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestA403ReportsThatGithubRefusedTheRequest(t *testing.T) {
	g := failingGitHub(t, http.StatusForbidden)
	c := updateClient(t, g, updateCurrent)

	_, err := c.Latest(context.Background())

	want := "github refused the request (Forbidden): check GITHUB_TOKEN and its scopes"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestAnyOtherStatusIsReportedWithItsCodeAndTheUrlAsked(t *testing.T) {
	g := failingGitHub(t, http.StatusInternalServerError)
	c := updateClient(t, g, updateCurrent)

	_, err := c.Latest(context.Background())

	if err == nil {
		t.Fatal("Latest succeeded, want an error")
	}
	url := g.srv.URL + "/repos/" + update.DefaultRepo + "/releases/latest"
	for _, want := range []string{"unexpected status", "500", url} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

func TestABodyThatIsNotReleaseJsonIsReportedAsAReadFailure(t *testing.T) {
	g := rawGitHub(t, "<html>not json at all</html>")
	c := updateClient(t, g, updateCurrent)

	_, err := c.Latest(context.Background())

	if err == nil {
		t.Fatal("Latest succeeded, want an error")
	}
	if !strings.HasPrefix(err.Error(), "reading release: ") {
		t.Errorf("error = %q, want it to start with %q", err.Error(), "reading release: ")
	}
}

func TestAReleaseWithAnEmptyTagIsReportedAsNoPublishedRelease(t *testing.T) {
	g := rawGitHub(t, `{"tag_name":"","assets":[]}`)
	c := updateClient(t, g, updateCurrent)

	_, err := c.Latest(context.Background())

	if !errors.Is(err, update.ErrNoRelease) {
		t.Fatalf("error = %v, want ErrNoRelease", err)
	}
	if err.Error() != "no published release found" {
		t.Errorf("error = %q, want %q", err.Error(), "no published release found")
	}
}

// --- Choosing the binary for this platform ---------------------------------

func TestAReleaseIsOutdatedWhenTheRunningVersionIsOlderThanItsTag(t *testing.T) {
	g := binaryRelease(t)
	rel := update.Release{Tag: updateTag}

	for _, c := range []struct {
		current string
		want    bool
	}{
		{"v1.1.0", true},
		{"v1.2.0", false},
		{"v1.3.0", false},
	} {
		client := updateClient(t, g, c.current)
		if got := client.Outdated(rel); got != c.want {
			t.Errorf("Outdated(%s) from %s = %v, want %v", rel.Tag, c.current, got, c.want)
		}
	}
}

func TestTheExactlyNamedPlatformAssetIsPreferred(t *testing.T) {
	// The differently stamped asset also matches the platform suffix and is
	// listed first, so only the preference for the exact name can pick the other.
	g := assetsRelease(t,
		releaseAsset{name: "katana_1.2.0_darwin_arm64", body: "the fallback"},
		releaseAsset{name: updateAsset, body: newKatana},
	)
	c, dest := installer(t, g, updateCurrent)

	installLatest(t, c, io.Discard)

	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want the exactly named asset %q", got, newKatana)
	}
}

func TestOnWindowsTheExpectedAssetNameEndsInExe(t *testing.T) {
	g := assetsRelease(t,
		releaseAsset{name: "katana_" + updateTag + "_windows_amd64", body: "no extension"},
		releaseAsset{name: "katana_" + updateTag + "_windows_amd64.exe", body: newKatana},
	)
	c, dest := installer(t, g, updateCurrent)
	c.OS, c.Arch = "windows", "amd64"

	installLatest(t, c, io.Discard)

	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want the .exe asset %q", got, newKatana)
	}
}

func TestOnEveryOtherPlatformTheExpectedAssetNameHasNoExtension(t *testing.T) {
	// An .exe is not this platform's binary, so a release publishing only that
	// one publishes nothing usable.
	exeOnly := assetsRelease(t, releaseAsset{name: "katana_" + updateTag + "_linux_amd64.exe", body: newKatana})
	c, _ := installer(t, exeOnly, updateCurrent)
	c.OS, c.Arch = "linux", "amd64"

	if err := installErr(t, c, io.Discard); !errors.Is(err, update.ErrNoAsset) {
		t.Errorf("error = %v, want ErrNoAsset for an .exe on linux", err)
	}

	g := assetsRelease(t, releaseAsset{name: "katana_" + updateTag + "_linux_amd64", body: newKatana})
	plain, dest := installer(t, g, updateCurrent)
	plain.OS, plain.Arch = "linux", "amd64"

	installLatest(t, plain, io.Discard)

	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestAnAssetWithThePlatformSuffixIsUsedWhenNoNameMatchesExactly(t *testing.T) {
	// The release is tagged v1.2.0 but its binaries were stamped 1.2.0, which
	// still installs.
	g := assetsRelease(t,
		releaseAsset{name: "katana_1.2.0_darwin_amd64", body: "the other architecture"},
		releaseAsset{name: "katana_1.2.0_darwin_arm64", body: newKatana},
	)
	c, dest := installer(t, g, updateCurrent)

	installLatest(t, c, io.Discard)

	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want the platform's asset %q", got, newKatana)
	}
}

func TestTheFirstListedAssetWinsWhenSeveralMatchTheFallback(t *testing.T) {
	g := assetsRelease(t,
		releaseAsset{name: "katana_nightly_darwin_arm64", body: "listed first"},
		releaseAsset{name: "katana_1.2.0_darwin_arm64", body: "listed second"},
	)
	c, dest := installer(t, g, updateCurrent)

	installLatest(t, c, io.Discard)

	if got := installedBody(t, dest); got != "listed first" {
		t.Errorf("installed %q, want the first asset the release lists", got)
	}
}

func TestNoAssetForThisPlatformFailsTheUpdate(t *testing.T) {
	g := assetsRelease(t, releaseAsset{name: "katana_" + updateTag + "_linux_amd64", body: newKatana})
	c, dest := installer(t, g, updateCurrent)

	err := installErr(t, c, io.Discard)

	if !errors.Is(err, update.ErrNoAsset) {
		t.Fatalf("error = %v, want ErrNoAsset", err)
	}
	want := "no release binary for this platform: " + updateOS + "/" + updateArch + " at " + updateTag
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("the running binary was replaced anyway, with %q", got)
	}
}

// --- Installing an update --------------------------------------------------

func TestTheCallerSuppliedDestinationIsTheFileReplaced(t *testing.T) {
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	path := installLatest(t, c, io.Discard)

	if path != dest {
		t.Errorf("installed to %q, want the destination it was given, %q", path, dest)
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestTheDownloadIsStagedBesideTheDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "katana")
	if err := os.WriteFile(dest, []byte(oldKatana), 0o755); err != nil {
		t.Fatal(err)
	}

	// The staged file only exists while the download is running, so the
	// directory is looked at from inside the request that serves the bytes.
	var mu sync.Mutex
	var staged []string
	g := newFakeGitHub(t, githubOptions{
		tag: updateTag,
		assets: []releaseAsset{
			{name: updateAsset, body: newKatana},
			{name: checksumsFile, body: manifestLine(newKatana, updateAsset)},
		},
		onAsset: func(string) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range entries {
				staged = append(staged, e.Name())
			}
		},
	})
	c := updateClient(t, g, updateCurrent)
	c.Dest = dest

	installLatest(t, c, io.Discard)

	mu.Lock()
	defer mu.Unlock()
	if !hasPrefixIn(staged, ".katana-update-") {
		t.Errorf("the destination's directory held %q while downloading, want a .katana-update- file", staged)
	}
}

func TestTheStagedFileIsRemovedWhenTheUpdateSucceeds(t *testing.T) {
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	installLatest(t, c, io.Discard)

	if names := dirNames(t, filepath.Dir(dest)); hasPrefixIn(names, ".katana-update-") {
		t.Errorf("the directory holds %q, want no staged download left behind", names)
	}
}

func TestTheStagedFileIsRemovedWhenTheUpdateFails(t *testing.T) {
	// A digest that does not match fails the update after the download has
	// already been staged.
	g := manifestRelease(t, manifestLine("something else entirely", updateAsset))
	c, dest := installer(t, g, updateCurrent)

	installErr(t, c, io.Discard)

	if names := dirNames(t, filepath.Dir(dest)); hasPrefixIn(names, ".katana-update-") {
		t.Errorf("the directory holds %q after a failed update, want no staged download left behind", names)
	}
}

func TestADestinationDirectoryThatCannotBeWrittenAdvisesSudo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not stop a write on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory with no write permission")
	}
	g := binaryRelease(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "katana")
	if err := os.WriteFile(dest, []byte(oldKatana), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	c := updateClient(t, g, updateCurrent)
	c.Dest = dest

	err := installErr(t, c, io.Discard)

	want := "cannot write to " + dir + ": re-run with sudo, or install elsewhere with the installer's --dir"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestTheDownloadIsAnnouncedBeforeItStarts(t *testing.T) {
	g := binaryRelease(t)
	c, _ := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	want := "==> downloading katana " + updateTag + " (" + updateOS + "/" + updateArch + ")\n"
	if !strings.Contains(log.String(), want) {
		t.Fatalf("progress = %q, want it to hold %q", log.String(), want)
	}
	// "Before downloading" — the announcement comes ahead of anything the
	// download itself reports.
	if at, verified := strings.Index(log.String(), want), strings.Index(log.String(), "checksum verified"); verified >= 0 && at > verified {
		t.Errorf("the download was announced at %d, after the checksum was verified at %d:\n%s", at, verified, log.String())
	}
}

func TestProgressOutputIsOptional(t *testing.T) {
	// Nothing is printed when there is nowhere to print it: an install with no
	// progress writer still installs.
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	path, err := c.Apply(context.Background(), latestRelease(t, c), nil)
	if err != nil {
		t.Fatalf("Apply with no progress writer: %v", err)
	}
	if path != dest {
		t.Errorf("installed to %q, want %q", path, dest)
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestTheStagedFileIsMadeExecutableBeforeItIsPutInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no executable permission bit")
	}
	g := binaryRelease(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "katana")
	// Deliberately not executable to begin with, so the bit can only come from
	// the staged file katana installs.
	if err := os.WriteFile(dest, []byte(oldKatana), 0o600); err != nil {
		t.Fatal(err)
	}
	c := updateClient(t, g, updateCurrent)
	c.Dest = dest

	installLatest(t, c, io.Discard)

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("installed binary is %v, want -rwxr-xr-x", got)
	}
}

func TestTheStagedFileIsRenamedOverTheDestinationInOneStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot overwrite a running image, so it swaps in two steps")
	}
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)
	// A handle opened before the swap stands in for the running process: a
	// rename leaves it reading the old binary, where a rewrite in place would
	// pull the ground out from under it.
	running, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	installLatest(t, c, io.Discard)

	still, err := io.ReadAll(running)
	if err != nil {
		t.Fatalf("reading the binary that was running: %v", err)
	}
	if string(still) != oldKatana {
		t.Errorf("the running binary now reads %q, want the swap to have left it alone", still)
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
	if names := dirNames(t, filepath.Dir(dest)); hasPrefixIn(names, "katana.old") {
		t.Errorf("the directory holds %q, want no moved-aside binary off Windows", names)
	}
}

func TestOnWindowsAPreviousOldFileIsRemovedBeforeTheSwap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the move-aside swap only runs on Windows")
	}
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)
	if err := os.WriteFile(dest+".old", []byte("left by an earlier update"), 0o755); err != nil {
		t.Fatal(err)
	}

	installLatest(t, c, io.Discard)

	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q; a leftover .old file should not stop the swap", got, newKatana)
	}
}

func TestOnWindowsAFailedSwapRestoresTheMovedAsideBinary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the move-aside swap only runs on Windows")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "katana.exe")
	if err := os.WriteFile(dest, []byte(oldKatana), 0o755); err != nil {
		t.Fatal(err)
	}

	// Holding the closed staged download open prevents Windows from renaming it.
	// That reaches the failure after dest has already been moved to dest.old.
	var mu sync.Mutex
	var stagedLock *os.File
	var lockErr error
	g := newFakeGitHub(t, githubOptions{
		tag: updateTag,
		assets: []releaseAsset{
			{name: updateAsset, body: newKatana},
			{name: checksumsFile, body: manifestLine(newKatana, updateAsset)},
		},
		onAsset: func(name string) {
			if name != checksumsFile {
				return
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				mu.Lock()
				lockErr = err
				mu.Unlock()
				return
			}
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), ".katana-update-") {
					continue
				}
				f, err := os.Open(filepath.Join(dir, entry.Name()))
				mu.Lock()
				stagedLock, lockErr = f, err
				mu.Unlock()
				return
			}
			mu.Lock()
			lockErr = errors.New("the staged download was not found")
			mu.Unlock()
		},
	})
	c := updateClient(t, g, updateCurrent)
	c.Dest = dest

	_, err := c.Apply(context.Background(), latestRelease(t, c), io.Discard)
	mu.Lock()
	lock, observedErr := stagedLock, lockErr
	mu.Unlock()
	if lock != nil {
		if closeErr := lock.Close(); closeErr != nil {
			t.Errorf("closing the staged-file lock: %v", closeErr)
		}
	}
	if observedErr != nil {
		t.Fatalf("locking the staged download: %v", observedErr)
	}
	if err == nil {
		t.Fatal("Apply succeeded, want the locked staged file to stop the swap")
	}
	if prefix := "installing to " + dest + ": "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("restored binary = %q, want %q", got, oldKatana)
	}
	if _, statErr := os.Stat(dest + ".old"); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("the restored binary also remains at %s.old (stat gave %v)", dest, statErr)
	}
}

func TestOnWindowsTheDisplacedOldFileIsDeletedAfterASuccessfulSwap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the move-aside swap only runs on Windows")
	}
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	installLatest(t, c, io.Discard)

	if _, err := os.Stat(dest + ".old"); err == nil {
		t.Errorf("%s.old is still there, want the displaced binary deleted", dest)
	}
}

func TestAFailureToPutTheNewBinaryInPlaceNamesTheDestination(t *testing.T) {
	g := binaryRelease(t)
	c := updateClient(t, g, updateCurrent)
	// A non-empty directory sitting where the binary belongs: the rename that
	// completes the install cannot replace it.
	dest := filepath.Join(t.TempDir(), "katana")
	if err := os.MkdirAll(filepath.Join(dest, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.Dest = dest

	path, err := c.Apply(context.Background(), latestRelease(t, c), io.Discard)
	if err == nil {
		t.Skipf("this platform renamed over a non-empty directory at %s; the install did not fail", path)
	}

	if prefix := "installing to " + dest + ": "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
}

func TestOnSuccessThePathThatWasWrittenIsReturned(t *testing.T) {
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	path := installLatest(t, c, io.Discard)

	if path != dest {
		t.Errorf("Apply returned %q, want the path it wrote, %q", path, dest)
	}
	if got := installedBody(t, path); got != newKatana {
		t.Errorf("the returned path holds %q, want %q", got, newKatana)
	}
}

// --- Replacing the running binary ------------------------------------------
//
// The destination katana picks when the caller names none can only be seen by
// being the binary that is running. These tests copy this test binary somewhere
// disposable and re-run it as a katana updating itself.
//
// The remaining case — the running binary's location cannot be determined — has
// no handle from outside the package: os.Executable only fails in situations a
// test cannot arrange.

const installHelperEnv = "KATANA_UPDATE_INSTALL_HELPER"

// TestUpdateInstallHelperProcess is not a test of its own: it is a katana that
// updates itself, run only as a child process of the tests below.
func TestUpdateInstallHelperProcess(t *testing.T) {
	if os.Getenv(installHelperEnv) != "1" {
		t.Skip("the self-updating katana, run only as a child process of an install test")
	}
	// The api base and the (absent) token come from the environment the parent
	// set; the destination is left empty, which is the case under test.
	c := update.New("v1.0.0")
	rel, err := c.Latest(context.Background())
	if err != nil {
		fmt.Println("ERR reading the release:", err)
		os.Exit(70)
	}
	path, err := c.Apply(context.Background(), rel, nil)
	if err != nil {
		fmt.Println("ERR installing:", err)
		os.Exit(71)
	}
	fmt.Println("INSTALLED", path)
	os.Exit(0) // exit before the testing package prints its own report
}

func hostExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// selfUpdateRelease publishes a binary for the machine the suite is running on,
// which is the platform the child process will ask for.
func selfUpdateRelease(t *testing.T) *fakeGitHub {
	t.Helper()
	name := "katana_" + updateTag + "_" + runtime.GOOS + "_" + runtime.GOARCH + hostExeSuffix()
	return newFakeGitHub(t, githubOptions{
		tag: updateTag,
		assets: []releaseAsset{
			{name: name, body: newKatana},
			{name: checksumsFile, body: manifestLine(newKatana, name)},
		},
	})
}

// copyTestBinary puts a runnable copy of this test binary at dest.
func copyTestBinary(t *testing.T, dest string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot find the test binary to copy: %v", err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Skipf("cannot read the test binary to copy: %v", err)
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// runSelfUpdate runs the katana at runPath, which updates itself from g, and
// returns the path it reported writing.
func runSelfUpdate(t *testing.T, runPath string, g *fakeGitHub) string {
	t.Helper()
	cmd := exec.Command(runPath, "-test.run=^TestUpdateInstallHelperProcess$")
	cmd.Env = append(os.Environ(),
		installHelperEnv+"=1",
		"KATANA_GITHUB_API="+g.srv.URL,
		"KATANA_GITHUB_TOKEN=",
		"GITHUB_TOKEN=",
		"GH_TOKEN=",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the self-updating katana failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if path, ok := strings.CutPrefix(strings.TrimSpace(line), "INSTALLED "); ok {
			return path
		}
	}
	t.Fatalf("the self-updating katana reported no destination:\n%s", out)
	return ""
}

// samePath reports whether two paths name the same file once symlinks such as
// macOS's /var are resolved.
func samePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}

func TestWithNoDestinationTheRunningExecutableIsReplaced(t *testing.T) {
	g := selfUpdateRelease(t)
	exe := filepath.Join(t.TempDir(), "katana"+hostExeSuffix())
	copyTestBinary(t, exe)

	got := runSelfUpdate(t, exe, g)

	if body := installedBody(t, exe); body != newKatana {
		t.Errorf("the running binary holds %q, want %q", body, newKatana)
	}
	if !samePath(got, exe) {
		t.Errorf("installed to %q, want the running binary %q", got, exe)
	}
}

func TestOnWindowsFailureToDeleteTheDisplacedBinaryDoesNotFailTheUpdate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows keeps the running image mapped after moving it aside")
	}
	g := selfUpdateRelease(t)
	exe := filepath.Join(t.TempDir(), "katana.exe")
	copyTestBinary(t, exe)

	got := runSelfUpdate(t, exe, g)

	if !samePath(got, exe) {
		t.Errorf("installed to %q, want the running binary %q", got, exe)
	}
	if body := installedBody(t, exe); body != newKatana {
		t.Errorf("installed %q, want %q", body, newKatana)
	}
	// The child tried to remove its displaced, still-mapped image before it
	// exited. Its presence proves that failure was ignored on the success path.
	info, err := os.Stat(exe + ".old")
	if err != nil {
		t.Fatalf("the mapped binary was not left at .old: %v", err)
	}
	if info.Size() == 0 {
		t.Error("the mapped binary left at .old is empty")
	}
}

func TestASymlinkedRunningExecutableIsResolvedBeforeItIsReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privileged account on Windows")
	}
	g := selfUpdateRelease(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "versions", "katana")
	link := filepath.Join(dir, "bin", "katana")
	copyTestBinary(t, binary)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	got := runSelfUpdate(t, link, g)

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("the link is gone: %v", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file, want the binary it points at rewritten")
	}
	if body := installedBody(t, binary); body != newKatana {
		t.Errorf("the real binary holds %q, want %q", body, newKatana)
	}
	if !samePath(got, binary) {
		t.Errorf("installed to %q, want the resolved binary %q", got, binary)
	}
}

// --- Fetching asset bytes --------------------------------------------------

func TestWithATokenTheAssetIsFetchedThroughTheApiUrl(t *testing.T) {
	// The api url is the only route that works for a private repository, so it
	// wins whenever katana has credentials to send.
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)
	c.Token = "secret-token"

	installLatest(t, c, io.Discard)

	if !g.asked(apiAssets + updateAsset) {
		t.Errorf("requests = %+v, want the asset fetched through its api url", g.requests())
	}
	if g.asked(browserAssets) {
		t.Errorf("requests = %+v, want no public download while a token is configured", g.requests())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestWithNoTokenThePublicDownloadUrlIsUsed(t *testing.T) {
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	installLatest(t, c, io.Discard)

	if !g.asked(browserAssets + updateAsset) {
		t.Errorf("requests = %+v, want the public download url", g.requests())
	}
	if g.asked(apiAssets) {
		t.Errorf("requests = %+v, want no api download without a token", g.requests())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestTheApiUrlIsUsedWhenThereIsNoBrowserDownloadUrl(t *testing.T) {
	for _, token := range []string{"", "secret-token"} {
		name := "with no token"
		if token != "" {
			name = "with a token"
		}
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t, githubOptions{
				tag: updateTag,
				assets: []releaseAsset{
					{name: updateAsset, body: newKatana, noBrowserURL: true},
					{name: checksumsFile, body: manifestLine(newKatana, updateAsset), noBrowserURL: true},
				},
			})
			c, dest := installer(t, g, updateCurrent)
			c.Token = token

			installLatest(t, c, io.Discard)

			if !g.asked(apiAssets + updateAsset) {
				t.Errorf("requests = %+v, want the api url", g.requests())
			}
			if got := installedBody(t, dest); got != newKatana {
				t.Errorf("installed %q, want %q", got, newKatana)
			}
		})
	}
}

func TestAnAssetWithNoUrlAtAllFailsTheUpdate(t *testing.T) {
	g := assetsRelease(t, releaseAsset{
		name: updateAsset, body: newKatana, noAPIURL: true, noBrowserURL: true,
	})
	c, dest := installer(t, g, updateCurrent)

	err := installErr(t, c, io.Discard)

	want := "release asset " + updateAsset + " has no download url"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("the running binary was replaced anyway, with %q", got)
	}
}

func TestAFailedAssetRequestNamesTheAsset(t *testing.T) {
	g := assetsRelease(t, releaseAsset{
		name: updateAsset, body: newKatana, status: http.StatusInternalServerError,
	})
	c, dest := installer(t, g, updateCurrent)

	err := installErr(t, c, io.Discard)

	if prefix := "downloading " + updateAsset + ": "; !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), prefix)
	}
	if !strings.Contains(err.Error(), "unexpected status 500") {
		t.Errorf("error = %q, want it to carry the underlying reason", err.Error())
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("the running binary was replaced anyway, with %q", got)
	}
}

// --- Verifying the download ------------------------------------------------

func TestTheDownloadIsCheckedAgainstTheReleasesChecksumsManifest(t *testing.T) {
	g := binaryRelease(t)
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	if !g.asked(checksumsFile) {
		t.Errorf("requests = %+v, want checksums.txt read", g.requests())
	}
	if !strings.Contains(log.String(), "==> checksum verified\n") {
		t.Errorf("progress = %q, want it to report the checksum verified", log.String())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestAReleaseWithNoChecksumsManifestStillInstalls(t *testing.T) {
	g := assetsRelease(t, releaseAsset{name: updateAsset, body: newKatana})
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	want := "==> release publishes no checksums.txt, skipping checksum verification\n"
	if !strings.Contains(log.String(), want) {
		t.Errorf("progress = %q, want it to say %q", log.String(), want)
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want the update to proceed unverified", got)
	}
}

func TestAManifestWithNoEntryForTheAssetStillInstalls(t *testing.T) {
	g := manifestRelease(t, manifestLine(newKatana, "katana_v1.2.0_plan9_386"))
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	want := "==> checksums.txt lists no entry for " + updateAsset + ", skipping checksum verification\n"
	if !strings.Contains(log.String(), want) {
		t.Errorf("progress = %q, want it to say %q", log.String(), want)
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want the update to proceed unverified", got)
	}
}

func TestAManifestThatCannotBeDownloadedStopsTheUpdate(t *testing.T) {
	g := assetsRelease(t,
		releaseAsset{name: updateAsset, body: newKatana},
		releaseAsset{name: checksumsFile, status: http.StatusInternalServerError},
	)
	c, dest := installer(t, g, updateCurrent)

	err := installErr(t, c, io.Discard)

	if !strings.Contains(err.Error(), checksumsFile) {
		t.Errorf("error = %q, want it to name the manifest it could not read", err.Error())
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("installed %q, want nothing installed when the manifest is unreadable", got)
	}
}

func TestADigestThatDoesNotMatchFailsTheUpdateAndLeavesTheBinaryAlone(t *testing.T) {
	published := digestOf("what the release meant to publish")
	g := manifestRelease(t, published+"  "+updateAsset+"\n")
	c, dest := installer(t, g, updateCurrent)

	err := installErr(t, c, io.Discard)

	want := "checksum mismatch for " + updateAsset + ": expected " + published + ", got " + digestOf(newKatana)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("installed %q, want the running binary left alone", got)
	}
}

func TestABinaryModeStarOnTheFilenameIsIgnored(t *testing.T) {
	g := manifestRelease(t, digestOf(newKatana)+"  *"+updateAsset+"\n")
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	if !strings.Contains(log.String(), "==> checksum verified") {
		t.Errorf("progress = %q, want the entry matched despite the binary-mode marker", log.String())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestAManifestLineWhoseFirstFieldIsNotSixtyFourCharactersIsIgnored(t *testing.T) {
	for name, digest := range map[string]string{
		"too short": strings.Repeat("a", 63),
		"too long":  strings.Repeat("a", 65),
	} {
		t.Run(name, func(t *testing.T) {
			g := manifestRelease(t, digest+"  "+updateAsset+"\n")
			c, dest := installer(t, g, updateCurrent)

			var log strings.Builder
			installLatest(t, c, &log)

			if !strings.Contains(log.String(), "lists no entry for "+updateAsset) {
				t.Errorf("progress = %q, want the malformed line ignored", log.String())
			}
			if got := installedBody(t, dest); got != newKatana {
				t.Errorf("installed %q, want %q", got, newKatana)
			}
		})
	}
}

func TestAManifestLineWithNoSpaceSeparatingDigestFromFilenameIsIgnored(t *testing.T) {
	g := manifestRelease(t, digestOf(newKatana)+updateAsset+"\n")
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	if !strings.Contains(log.String(), "lists no entry for "+updateAsset) {
		t.Errorf("progress = %q, want the line with no separator ignored", log.String())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestWhitespaceAroundAManifestLineAndItsFilenameIsIgnored(t *testing.T) {
	g := manifestRelease(t, "   "+digestOf(newKatana)+"    "+updateAsset+"   \n")
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	if !strings.Contains(log.String(), "==> checksum verified") {
		t.Errorf("progress = %q, want the padded entry matched", log.String())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestAnUpperCaseDigestInTheManifestStillMatches(t *testing.T) {
	g := manifestRelease(t, strings.ToUpper(digestOf(newKatana))+"  "+updateAsset+"\n")
	c, dest := installer(t, g, updateCurrent)

	var log strings.Builder
	installLatest(t, c, &log)

	if !strings.Contains(log.String(), "==> checksum verified") {
		t.Errorf("progress = %q, want the upper-case entry matched", log.String())
	}
	if got := installedBody(t, dest); got != newKatana {
		t.Errorf("installed %q, want %q", got, newKatana)
	}
}

func TestTheFirstManifestLineNamingTheAssetWins(t *testing.T) {
	// The first line is wrong and the second is right: reporting the first as
	// the expected digest is what shows which one was read.
	first := digestOf("an earlier build")
	g := manifestRelease(t, first+"  "+updateAsset+"\n"+manifestLine(newKatana, updateAsset))
	c, dest := installer(t, g, updateCurrent)

	err := installErr(t, c, io.Discard)

	want := "checksum mismatch for " + updateAsset + ": expected " + first + ", got " + digestOf(newKatana)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := installedBody(t, dest); got != oldKatana {
		t.Errorf("installed %q, want the running binary left alone", got)
	}
}

// --- The background update check -------------------------------------------

// cacheFile is the name of the record katana keeps of its last check.
const cacheFile = "update.json"

// cachedCheck is that record. The field names are the file's own: seeding a
// check that happened yesterday is the only way to observe the once-a-day rule
// without waiting a day.
type cachedCheck struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

// isolateChecks points the update check at a cache directory of its own and at
// the fake GitHub, and clears the environment that would otherwise turn
// checking off on a developer machine or in CI. It returns the cache directory.
func isolateChecks(t *testing.T, g *fakeGitHub) string {
	t.Helper()
	dir := t.TempDir()
	clearGitHubEnv(t)
	t.Setenv("KATANA_GITHUB_API", g.srv.URL)
	t.Setenv("KATANA_CACHE_DIR", dir)
	t.Setenv("KATANA_NO_UPDATE_CHECK", "")
	t.Setenv("CI", "")
	return dir
}

// seedCache records a check made age ago that saw tag.
func seedCache(t *testing.T, dir string, age time.Duration, tag string) {
	t.Helper()
	body, err := json.Marshal(cachedCheck{CheckedAt: time.Now().Add(-age), LatestTag: tag})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cacheFile), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// readCache returns the check katana recorded in dir.
func readCache(t *testing.T, dir string) cachedCheck {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, cacheFile))
	if err != nil {
		t.Fatalf("reading the update cache: %v", err)
	}
	var cached cachedCheck
	if err := json.Unmarshal(body, &cached); err != nil {
		t.Fatalf("decoding the update cache: %v", err)
	}
	return cached
}

// checkAndNotice runs katana's release check beside a command and returns what
// the user sees once that command has finished.
func checkAndNotice(t *testing.T, current string) string {
	t.Helper()
	n := update.Start(current)
	var out strings.Builder
	n.Notice(&out)
	return out.String()
}

// wantNotice is the hint a finished command prints, without the release page
// line that only a fresh check knows.
func wantNotice(latest, current string) string {
	return "\nkatana " + latest + " is available (you have " + current + "). Run `katana update` to install it.\n"
}

func TestTheCheckIsSkippedWhenUpdateCheckingIsTurnedOff(t *testing.T) {
	for _, value := range []string{"1", "yes", "false"} { // any non-empty value
		t.Run("KATANA_NO_UPDATE_CHECK="+value, func(t *testing.T) {
			g := binaryRelease(t)
			dir := isolateChecks(t, g)
			seedCache(t, dir, time.Hour, "v9.9.9") // news that would otherwise be reported
			t.Setenv("KATANA_NO_UPDATE_CHECK", value)

			out := checkAndNotice(t, "v1.0.0")

			if out != "" {
				t.Errorf("notice = %q, want silence", out)
			}
			if reqs := g.requests(); len(reqs) != 0 {
				t.Errorf("requests = %+v, want none", reqs)
			}
		})
	}
}

func TestTheCheckIsSkippedOnContinuousIntegration(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	seedCache(t, dir, time.Hour, "v9.9.9")
	t.Setenv("CI", "true")

	out := checkAndNotice(t, "v1.0.0")

	if out != "" {
		t.Errorf("notice = %q, want silence: nobody can act on it in a log", out)
	}
	if reqs := g.requests(); len(reqs) != 0 {
		t.Errorf("requests = %+v, want none", reqs)
	}
}

func TestTheCheckIsSkippedForADevelopmentBuild(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	seedCache(t, dir, time.Hour, "v9.9.9")

	for _, current := range []string{"dev", "", "v1.0.0-3-gabc1234", "v1.0.0-dirty"} {
		t.Run("version "+current, func(t *testing.T) {
			out := checkAndNotice(t, current)

			if out != "" {
				t.Errorf("notice = %q, want silence: a local build would be offered a downgrade", out)
			}
		})
	}
	if reqs := g.requests(); len(reqs) != 0 {
		t.Errorf("requests = %+v, want none", reqs)
	}
}

func TestACachedReleaseTagIsReportedWithoutAskingGithubAgain(t *testing.T) {
	// News from an earlier check is repeated every run until the user upgrades.
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	seedCache(t, dir, time.Hour, "v9.9.9")

	out := checkAndNotice(t, "v1.0.0")

	if !strings.Contains(out, "v9.9.9") {
		t.Errorf("notice = %q, want the cached release reported", out)
	}
	if reqs := g.requests(); len(reqs) != 0 {
		t.Errorf("requests = %+v, want the cached answer used instead", reqs)
	}
}

func TestGithubIsAskedOnlyWhenTheLastCheckIsMoreThanADayOld(t *testing.T) {
	t.Run("a check from today is left alone", func(t *testing.T) {
		g := binaryRelease(t)
		dir := isolateChecks(t, g)
		seedCache(t, dir, 23*time.Hour, "v9.9.9")

		checkAndNotice(t, "v1.0.0")

		if reqs := g.requests(); len(reqs) != 0 {
			t.Errorf("requests = %+v, want none within the day", reqs)
		}
	})

	t.Run("a check from yesterday is made again", func(t *testing.T) {
		g := binaryRelease(t)
		dir := isolateChecks(t, g)
		seedCache(t, dir, 25*time.Hour, "")

		out := checkAndNotice(t, "v1.0.0")

		if !g.asked("/releases/latest") {
			t.Errorf("requests = %+v, want the newest release asked for", g.requests())
		}
		if !strings.Contains(out, updateTag) {
			t.Errorf("notice = %q, want it to report %q", out, updateTag)
		}
	})
}

func TestASuccessfulCheckRemembersTheTagAndItsPageUrl(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)

	started := time.Now()
	out := checkAndNotice(t, "v1.0.0")
	finished := time.Now()

	if !strings.Contains(out, updateTag) {
		t.Errorf("notice = %q, want the tag that was found", out)
	}
	if !strings.Contains(out, releasePage+updateTag) {
		t.Errorf("notice = %q, want the release page %q", out, releasePage+updateTag)
	}
	cached := readCache(t, dir)
	if cached.LatestTag != updateTag {
		t.Errorf("cached tag = %q, want %q", cached.LatestTag, updateTag)
	}
	if cached.CheckedAt.Before(started) || cached.CheckedAt.After(finished) {
		t.Errorf("cached check time = %v, want a time between %v and %v", cached.CheckedAt, started, finished)
	}
	// The check was stamped, so the next run reports the same news from the
	// cache without asking again.
	before := len(g.requests())
	if again := checkAndNotice(t, "v1.0.0"); !strings.Contains(again, updateTag) {
		t.Errorf("notice = %q, want the remembered tag", again)
	}
	if got := len(g.requests()); got != before {
		t.Errorf("the second run made %d more requests, want the stamped cache used", got-before)
	}
}

func TestAFailedCheckRecordsNothingAndTriesAgainOnTheNextRun(t *testing.T) {
	g := failingGitHub(t, http.StatusInternalServerError)
	dir := isolateChecks(t, g)

	out := checkAndNotice(t, "v1.0.0")

	if out != "" {
		t.Errorf("notice = %q, want silence when the check failed", out)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a cache file was written after a failed check (stat gave %v)", err)
	}
	before := len(g.requests())
	checkAndNotice(t, "v1.0.0")
	if got := len(g.requests()); got <= before {
		t.Errorf("the next run made no request, want it to try again")
	}
}

func TestTheBackgroundRequestIsAbandonedAfterFiveSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("the five second window has to be waited out")
	}
	g := newFakeGitHub(t, githubOptions{tag: updateTag, hold: true})
	dir := isolateChecks(t, g)

	start := time.Now()
	n := update.Start("v1.0.0")
	// Each notice waits a second for the check; the first that returns at once
	// is the one that found it already given up.
	var waited time.Duration
	for {
		asked := time.Now()
		n.Notice(io.Discard)
		if time.Since(asked) < 500*time.Millisecond {
			waited = time.Since(start)
			break
		}
		if time.Since(start) > 20*time.Second {
			t.Fatal("the background request was never abandoned")
		}
	}

	if waited < 4*time.Second || waited > 10*time.Second {
		t.Errorf("the check gave up after %v, want about five seconds", waited)
	}
	var out strings.Builder
	n.Notice(&out)
	if out.String() != "" {
		t.Errorf("notice = %q, want silence after the request timed out", out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a cache file was written after a timeout (stat gave %v)", err)
	}
	before := len(g.requests())
	g.release()
	if again := checkAndNotice(t, "v1.0.0"); !strings.Contains(again, updateTag) {
		t.Errorf("notice on retry = %q, want it to report %q", again, updateTag)
	}
	if got := len(g.requests()); got <= before {
		t.Errorf("the next run made no request after the timeout, requests = %+v", g.requests())
	}
}

// --- Printing the notice ---------------------------------------------------

func TestANoticeWaitsAtMostASecondForACheckStillInFlight(t *testing.T) {
	g := newFakeGitHub(t, githubOptions{tag: updateTag, hold: true})
	isolateChecks(t, g)

	n := update.Start("v1.0.0")
	var out strings.Builder
	start := time.Now()
	n.Notice(&out)
	waited := time.Since(start)

	if waited < 700*time.Millisecond {
		t.Errorf("the notice gave up after %v, want it to wait about a second for the check", waited)
	}
	if waited > 3*time.Second {
		t.Errorf("the notice waited %v, want it to give up after about a second", waited)
	}
	if out.String() != "" {
		t.Errorf("notice = %q, want nothing from a check that never answered", out.String())
	}

	// Let the check finish before the cache directory is taken away.
	g.release()
	n.Notice(io.Discard)
}

func TestNoNoticeIsPrintedWhenNoReleaseTagIsKnown(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	// A recent check that found nothing: no news, and none is asked for.
	seedCache(t, dir, time.Hour, "")

	if out := checkAndNotice(t, "v1.0.0"); out != "" {
		t.Errorf("notice = %q, want silence with no release to name", out)
	}
}

func TestNoNoticeIsPrintedWhenTheKnownReleaseIsNotNewer(t *testing.T) {
	for _, current := range []string{"v2.0.0", "v2.1.0"} {
		t.Run("running "+current, func(t *testing.T) {
			g := binaryRelease(t)
			dir := isolateChecks(t, g)
			seedCache(t, dir, time.Hour, "v2.0.0")

			if out := checkAndNotice(t, current); out != "" {
				t.Errorf("notice = %q, want silence when %s is not behind v2.0.0", out, current)
			}
		})
	}
}

func TestTheNoticeNamesTheNewReleaseAndTheRunningVersion(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	seedCache(t, dir, time.Hour, "v2.0.0")

	out := checkAndNotice(t, "v1.0.0")

	if out != wantNotice("v2.0.0", "v1.0.0") {
		t.Errorf("notice = %q, want %q", out, wantNotice("v2.0.0", "v1.0.0"))
	}
}

func TestTheReleasePageIsPrintedOnlyWhenItIsKnown(t *testing.T) {
	t.Run("a fresh check knows the page", func(t *testing.T) {
		g := binaryRelease(t)
		isolateChecks(t, g)

		out := checkAndNotice(t, "v1.0.0")

		want := wantNotice(updateTag, "v1.0.0") + releasePage + updateTag + "\n"
		if out != want {
			t.Errorf("notice = %q, want %q", out, want)
		}
	})

	t.Run("a tag recovered from the cache has no page", func(t *testing.T) {
		g := binaryRelease(t)
		dir := isolateChecks(t, g)
		seedCache(t, dir, time.Hour, "v2.0.0")

		out := checkAndNotice(t, "v1.0.0")

		if out != wantNotice("v2.0.0", "v1.0.0") {
			t.Errorf("notice = %q, want only the first line %q", out, wantNotice("v2.0.0", "v1.0.0"))
		}
	})
}

func TestANotifierThatWasNeverStartedPrintsNothing(t *testing.T) {
	var n *update.Notifier

	var out strings.Builder
	n.Notice(&out)

	if out.String() != "" {
		t.Errorf("notice = %q, want nothing from a check that never began", out.String())
	}
}

// --- The check cache -------------------------------------------------------

func TestTheCacheIsUpdateJsonInsideTheDirectoryTheEnvironmentNames(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)

	checkAndNotice(t, "v1.0.0")

	if _, err := os.Stat(filepath.Join(dir, "update.json")); err != nil {
		t.Errorf("no update.json in the cache directory: %v", err)
	}
}

func TestWithoutAnOverrideTheCacheLivesInAKatanaFolderUnderTheUserCacheDirectory(t *testing.T) {
	g := binaryRelease(t)
	isolateChecks(t, g)
	home := t.TempDir()
	t.Setenv("KATANA_CACHE_DIR", "")
	// The per-user cache directory is the operating system's own idea of one,
	// which these are the variables behind on the platforms Go supports.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("LocalAppData", filepath.Join(home, "local-app-data"))
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("this platform has no per-user cache directory: %v", err)
	}

	checkAndNotice(t, "v1.0.0")

	want := filepath.Join(base, "katana", "update.json")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("no cache at %s: %v", want, err)
	}
}

func TestAMissingCacheTriggersAFreshCheck(t *testing.T) {
	g := binaryRelease(t)
	isolateChecks(t, g) // an empty directory: no check has ever run

	out := checkAndNotice(t, "v1.0.0")

	if !g.asked("/releases/latest") {
		t.Errorf("requests = %+v, want a fresh check", g.requests())
	}
	if !strings.Contains(out, updateTag) {
		t.Errorf("notice = %q, want it to report %q", out, updateTag)
	}
}

func TestAnUnreadableCacheTriggersAFreshCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not enforced this way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits set")
	}
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	seedCache(t, dir, time.Hour, "v9.9.9")
	path := filepath.Join(dir, cacheFile)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	checkAndNotice(t, "v1.0.0")

	if !g.asked("/releases/latest") {
		t.Errorf("requests = %+v, want an unreadable cache to trigger a fresh check", g.requests())
	}
}

func TestAMalformedCacheTriggersAFreshCheck(t *testing.T) {
	g := binaryRelease(t)
	dir := isolateChecks(t, g)
	if err := os.WriteFile(filepath.Join(dir, cacheFile), []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	checkAndNotice(t, "v1.0.0")

	if !g.asked("/releases/latest") {
		t.Errorf("requests = %+v, want a malformed cache to trigger a fresh check", g.requests())
	}
}

func TestTheCacheDirectoryIsCreatedIfNeeded(t *testing.T) {
	g := binaryRelease(t)
	isolateChecks(t, g)
	dir := filepath.Join(t.TempDir(), "not", "there", "yet")
	t.Setenv("KATANA_CACHE_DIR", dir)

	checkAndNotice(t, "v1.0.0")

	if _, err := os.Stat(filepath.Join(dir, cacheFile)); err != nil {
		t.Errorf("no cache written into a directory that did not exist: %v", err)
	}
}

func TestTheCacheFileIsReadableByEveryoneAndWritableByItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not carry these permission bits")
	}
	g := binaryRelease(t)
	dir := isolateChecks(t, g)

	checkAndNotice(t, "v1.0.0")

	info, err := os.Stat(filepath.Join(dir, cacheFile))
	if err != nil {
		t.Fatalf("no cache file: %v", err)
	}
	// A control file written with the same permissions shows what this process's
	// umask allows, so the comparison is about what katana asked for.
	control := filepath.Join(dir, "control")
	if err := os.WriteFile(control, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(control)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want.Mode().Perm() {
		t.Errorf("cache file is %v, want %v", got, want.Mode().Perm())
	}
}

func TestACacheThatCannotBeWrittenIsNotAnError(t *testing.T) {
	g := binaryRelease(t)
	isolateChecks(t, g)
	// A regular file where the cache directory should be: nothing can be
	// written under it.
	blocked := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KATANA_CACHE_DIR", blocked)

	out := checkAndNotice(t, "v1.0.0")

	if !strings.Contains(out, updateTag) {
		t.Errorf("notice = %q, want the check to be reported even though it could not be cached", out)
	}
	before := len(g.requests())
	if again := checkAndNotice(t, "v1.0.0"); !strings.Contains(again, updateTag) {
		t.Errorf("notice on retry = %q, want the uncached check to still be reported", again)
	}
	if got := len(g.requests()); got <= before {
		t.Errorf("the next run made no request after the cache write failed, requests = %+v", g.requests())
	}
}
