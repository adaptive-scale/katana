package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseGoTest(t *testing.T) {
	out := `=== RUN   TestCheckoutAppliesDiscount
--- PASS: TestCheckoutAppliesDiscount (0.01s)
=== RUN   TestCheckoutRejectsExpiredCode
    checkout_test.go:42: total = 100, want 90
--- FAIL: TestCheckoutRejectsExpiredCode (0.02s)
=== RUN   TestCheckoutSkipsWithoutNetwork
--- SKIP: TestCheckoutSkipsWithoutNetwork (0.00s)
FAIL
FAIL	github.com/x/checkout	0.312s
ok  	github.com/x/cart	0.104s
`
	cases := Parse("go-test", out)
	if len(cases) != 3 {
		t.Fatalf("got %d cases, want 3: %+v", len(cases), cases)
	}
	want := []struct {
		name   string
		status Status
	}{
		{"TestCheckoutAppliesDiscount", StatusPass},
		{"TestCheckoutRejectsExpiredCode", StatusFail},
		{"TestCheckoutSkipsWithoutNetwork", StatusSkip},
	}
	for i, w := range want {
		if cases[i].Name != w.name || cases[i].Status != w.status {
			t.Errorf("case %d = %q/%s, want %q/%s", i, cases[i].Name, cases[i].Status, w.name, w.status)
		}
		if cases[i].Suite != "github.com/x/checkout" {
			t.Errorf("case %d suite = %q, want the package that reported it", i, cases[i].Suite)
		}
	}
	if cases[0].Duration != 10*time.Millisecond {
		t.Errorf("duration = %s, want 10ms", cases[0].Duration)
	}
	if !strings.Contains(cases[1].Output, "total = 100, want 90") {
		t.Errorf("failure detail not attached: %q", cases[1].Output)
	}
	if cases[0].Output != "" {
		t.Errorf("passing case carried failure detail: %q", cases[0].Output)
	}
}

func TestParseGoSubtestsAndBuildFailure(t *testing.T) {
	out := `--- FAIL: TestParent (0.00s)
    --- FAIL: TestParent/child (0.00s)
FAIL
# github.com/x/broken
./broken_test.go:9:2: undefined: Missing
FAIL	github.com/x/broken [build failed]
`
	cases := Parse("go-test", out)
	if len(cases) != 3 {
		t.Fatalf("got %d cases, want parent, child and the build failure: %+v", len(cases), cases)
	}
	if cases[1].Name != "TestParent/child" {
		t.Errorf("subtest name = %q", cases[1].Name)
	}
	build := cases[2]
	if build.Name != "build failed" || build.Status != StatusFail || build.Suite != "github.com/x/broken" {
		t.Errorf("build failure recorded as %+v", build)
	}
	if !strings.Contains(build.Output, "undefined: Missing") {
		t.Errorf("build error not captured: %q", build.Output)
	}
}

func TestParsePytest(t *testing.T) {
	out := `tests/test_cart.py::test_applies_discount PASSED                    [ 33%]
tests/test_cart.py::TestCheckout::test_rejects_expired FAILED       [ 66%]
tests/test_cart.py::test_needs_network SKIPPED                      [100%]

=================================== FAILURES ===================================
_________________ TestCheckout.test_rejects_expired ____________________
    assert total == 90
E   AssertionError: assert 100 == 90
=========================== short test summary info ============================
FAILED tests/test_cart.py::TestCheckout::test_rejects_expired - AssertionError
`
	cases := Parse("pytest", out)
	if len(cases) != 3 {
		t.Fatalf("got %d cases, want 3: %+v", len(cases), cases)
	}
	if cases[0].Suite != "tests/test_cart.py" {
		t.Errorf("suite = %q, want the test file", cases[0].Suite)
	}
	if cases[1].Status != StatusFail || cases[2].Status != StatusSkip {
		t.Errorf("statuses = %s, %s", cases[1].Status, cases[2].Status)
	}
	if !strings.Contains(cases[1].Output, "AssertionError") {
		t.Errorf("traceback not attached: %q", cases[1].Output)
	}
}

func TestParseJest(t *testing.T) {
	out := ` PASS  src/cart.test.js
  checkout
    ✓ applies a discount (3 ms)
    ✕ rejects an expired code (2 ms)
    ○ skipped needs network
`
	cases := Parse("jest", out)
	if len(cases) != 3 {
		t.Fatalf("got %d cases, want 3: %+v", len(cases), cases)
	}
	if cases[0].Suite != "src/cart.test.js" {
		t.Errorf("suite = %q", cases[0].Suite)
	}
	if cases[0].Status != StatusPass || cases[1].Status != StatusFail || cases[2].Status != StatusSkip {
		t.Errorf("statuses = %s, %s, %s", cases[0].Status, cases[1].Status, cases[2].Status)
	}
	if cases[0].Duration != 3*time.Millisecond {
		t.Errorf("duration = %s, want 3ms", cases[0].Duration)
	}
}

// A vitest run prints a per-file roll-up with the same tick as a case; counting
// it would inflate every report.
func TestParseJSIgnoresFileRollups(t *testing.T) {
	out := ` ✓ src/cart.test.ts (2 tests) 5ms
   ✓ applies a discount
   ✓ rejects an expired code
`
	cases := Parse("vitest", out)
	if len(cases) != 2 {
		t.Fatalf("got %d cases, want 2: %+v", len(cases), cases)
	}
}

func TestParseCargo(t *testing.T) {
	out := `     Running unittests src/lib.rs (target/debug/deps/cart-9f2)
test cart::applies_discount ... ok
test cart::rejects_expired ... FAILED
test cart::needs_network ... ignored

failures:

---- cart::rejects_expired stdout ----
thread 'cart::rejects_expired' panicked at src/lib.rs:12:5:
assertion failed
`
	cases := Parse("cargo-test", out)
	if len(cases) != 3 {
		t.Fatalf("got %d cases, want 3: %+v", len(cases), cases)
	}
	if cases[0].Suite != "unittests src/lib.rs" {
		t.Errorf("suite = %q", cases[0].Suite)
	}
	if !strings.Contains(cases[1].Output, "assertion failed") {
		t.Errorf("panic body not attached: %q", cases[1].Output)
	}
}

func TestParseDotnetAndXCTest(t *testing.T) {
	dotnet := Parse("xunit", `  Passed Cart.Tests.AppliesDiscount [12 ms]
  Failed Cart.Tests.RejectsExpired [3 ms]
  Skipped Cart.Tests.NeedsNetwork
`)
	if len(dotnet) != 3 {
		t.Fatalf("dotnet: got %d cases: %+v", len(dotnet), dotnet)
	}
	if dotnet[0].Suite != "Cart.Tests" || dotnet[0].Name != "AppliesDiscount" {
		t.Errorf("dotnet: qualified name not split: %+v", dotnet[0])
	}
	if dotnet[0].Duration != 12*time.Millisecond {
		t.Errorf("dotnet: duration = %s", dotnet[0].Duration)
	}

	swift := Parse("xctest", `Test Case '-[CartTests testAppliesDiscount]' passed (0.002 seconds).
Test Case 'CartTests.testRejectsExpired' failed (0.001 seconds).
`)
	if len(swift) != 2 {
		t.Fatalf("xctest: got %d cases: %+v", len(swift), swift)
	}
	if swift[0].Suite != "CartTests" || swift[0].Name != "testAppliesDiscount" {
		t.Errorf("xctest: name not split: %+v", swift[0])
	}
	if swift[1].Status != StatusFail {
		t.Errorf("xctest: status = %s", swift[1].Status)
	}
}

// The configured framework is only a hint: test.command can run anything.
func TestParseFallsBackWhenFrameworkDoesNotMatchOutput(t *testing.T) {
	cases := Parse("junit5", "--- PASS: TestOne (0.00s)\nok  \tgithub.com/x/y\t0.1s\n")
	if len(cases) != 1 || cases[0].Name != "TestOne" {
		t.Fatalf("go output not recovered under a mismatched framework: %+v", cases)
	}
}

func TestParseStripsANSIColour(t *testing.T) {
	cases := Parse("go-test", "\x1b[32m--- PASS: TestColoured (0.00s)\x1b[0m\n")
	if len(cases) != 1 || cases[0].Name != "TestColoured" {
		t.Fatalf("colour codes defeated the parser: %+v", cases)
	}
}

// An unreadable runner still gets a report: the exit code is the verdict.
func TestCollectFallsBackToTheExitCode(t *testing.T) {
	r := &Report{Command: "make check", Output: "everything is fine\n", ExitCode: 0}
	r.Collect()
	if r.Parsed {
		t.Error("Parsed should be false when no case was recovered")
	}
	if r.Total() != 1 || r.Passed() != 1 {
		t.Errorf("fallback case = %+v", r.Cases)
	}

	failed := &Report{Command: "make check", Output: "boom\n", ExitCode: 2}
	failed.Collect()
	if failed.Failed() != 1 || failed.OK() {
		t.Errorf("failed run reported as %+v", failed.Cases)
	}
}

func TestCountsAndSuites(t *testing.T) {
	r := &Report{Cases: []Case{
		{Suite: "a", Name: "one", Status: StatusPass, Duration: time.Second},
		{Suite: "b", Name: "two", Status: StatusFail},
		{Suite: "a", Name: "three", Status: StatusSkip},
		{Name: "loose", Status: StatusPass},
	}}
	if r.Total() != 4 || r.Passed() != 2 || r.Failed() != 1 || r.Skipped() != 1 {
		t.Errorf("counts = %d/%d/%d/%d", r.Total(), r.Passed(), r.Failed(), r.Skipped())
	}
	// Two passed of three executed cases; the skipped one does not count against
	// the rate.
	if got := r.PassRate(); got < 66.6 || got > 66.7 {
		t.Errorf("PassRate = %v, want ~66.7", got)
	}

	suites := r.Suites()
	if len(suites) != 3 {
		t.Fatalf("got %d suites, want 3: %+v", len(suites), suites)
	}
	if suites[0].Name != "a" || len(suites[0].Cases) != 2 || suites[0].Passed != 1 || suites[0].Skipped != 1 {
		t.Errorf("first suite = %+v", suites[0])
	}
	if suites[2].Name != defaultSuite {
		t.Errorf("unattributed case grouped under %q", suites[2].Name)
	}
}

func TestVerboseAddsPerCaseFlagOnlyWhereItHelps(t *testing.T) {
	tests := []struct {
		framework string
		command   string
		want      string
		added     bool
	}{
		{"go-test", "go test ./...", "go test ./... -v", true},
		{"go-test", "go test -v ./...", "go test -v ./...", false},
		{"go-test", "go test -json ./...", "go test -json ./...", false},
		{"pytest", "pytest", "pytest -v", true},
		{"pytest", "pytest -q", "pytest -q", false},
		{"jest", "npm test", "npm test", false},
		{"junit5", "mvn test", "mvn test", false},
		// Appending an argument to a pipeline would land it in the wrong command.
		{"go-test", "go test ./... | tee log.txt", "go test ./... | tee log.txt", false},
	}
	for _, tc := range tests {
		got, added := Verbose(tc.framework, tc.command)
		if got != tc.want || added != tc.added {
			t.Errorf("Verbose(%q, %q) = %q/%v, want %q/%v", tc.framework, tc.command, got, added, tc.want, tc.added)
		}
	}
}

func TestRecorderCopiesWithoutSwallowing(t *testing.T) {
	var rec Recorder
	var sink strings.Builder
	w := rec.Tee(&sink)
	if _, err := w.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Tee(&sink).Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "hello world" {
		t.Errorf("downstream got %q", sink.String())
	}
	if rec.String() != "hello world" {
		t.Errorf("recorded %q", rec.String())
	}
}

func TestWriteHTMLIsSelfContainedAndTimestamped(t *testing.T) {
	started := time.Date(2026, 8, 10, 14, 5, 6, 0, time.UTC)
	r := &Report{
		Project:   "katana",
		Root:      "/tmp/katana",
		Command:   "go test ./... -v",
		Framework: "go-test",
		Version:   "v1.2.3",
		StartedAt: started,
		Duration:  1500 * time.Millisecond,
		ExitCode:  1,
		Output:    "--- FAIL: TestOne (0.00s)\n",
		Behaviors: []Behavior{{Source: "behaviors/cart.md", Output: "tests/cart_test.go", Status: "behavior changed", Stack: "go/go-test via claude", Stale: true}},
	}
	r.Collect()

	dir := t.TempDir()
	path, err := r.WriteHTML(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(path); base != "report-20260810-140506.html" {
		t.Errorf("report file name = %q, want the run's timestamp", base)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{
		"<!doctype html>", "TestOne", "failed", "behaviors/cart.md",
		"go test ./... -v", "v1.2.3", "10 Aug 2026",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report does not mention %q", want)
		}
	}
	// A report must open from a file:// URL with no network, so nothing may be
	// fetched from elsewhere.
	for _, bad := range []string{"http://", "https://", "<script src", "<link rel=\"stylesheet\""} {
		if strings.Contains(html, bad) {
			t.Errorf("report is not self-contained: found %q", bad)
		}
	}
}

func TestWriteHTMLEscapesTestOutput(t *testing.T) {
	r := &Report{
		StartedAt: time.Now(),
		Cases:     []Case{{Suite: "pkg", Name: "TestXSS", Status: StatusFail, Output: `<script>alert("x")</script>`}},
		Parsed:    true,
	}
	path, err := r.WriteHTML(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "<script>alert") {
		t.Error("test output was not escaped into the report")
	}
}

func TestABuildFailureIsMarkedBlockedAndAPlainFailureIsNot(t *testing.T) {
	out := `--- FAIL: TestBroke (0.00s)
FAIL	github.com/x/ran	0.10s
FAIL	github.com/x/broken [build failed]
`
	r := &Report{Framework: "go-test", Output: out, ExitCode: 1}
	r.Collect()

	blocked := r.Blocked()
	if len(blocked) != 1 {
		t.Fatalf("Blocked() = %+v, want only the package that never ran", blocked)
	}
	if blocked[0].Suite != "github.com/x/broken" {
		t.Errorf("blocked suite = %q, want the package that failed to build", blocked[0].Suite)
	}
	// A test that ran and failed is a different thing, and must not be swept up.
	for _, c := range r.Cases {
		if c.Name == "TestBroke" && c.Blocked {
			t.Error("a test that ran and failed was marked blocked")
		}
	}
}

func TestAReportWithNothingBlockedReportsNone(t *testing.T) {
	r := &Report{Framework: "go-test", Output: "--- PASS: TestOne (0.00s)\nok  \tgithub.com/x/fine\t0.10s\n"}
	r.Collect()

	if got := r.Blocked(); len(got) != 0 {
		t.Errorf("Blocked() = %+v, want none", got)
	}
}
