// This file covers behaviors/internal/report.md: katana's test report — making a
// runner print per-case results, capturing the suite output, recovering cases
// from six runners' output, the totals a recorded run reports, and the
// self-contained HTML page it writes.
//
// Every assertion goes through the report package's exported API: Verbose,
// Recorder, Frameworks, Parse, the Report methods (Collect, OK, Result, Total,
// Passed, Failed, Skipped, PassRate, StaleBehaviors, Suites) and WriteHTML.
//
// Two things the specification describes are only observable through the written
// page, so those tests render a report and read the HTML back: how a timing is
// displayed, and what the page shows. The in-page filtering is JavaScript that
// only runs in a browser — those tests assert the page carries the data the
// filters match on, and, where there is nothing else to look at, that the
// embedded script states the rule.

package internal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/report"
)

// reportStart is a fixed run start time, so a file name and a rendered timestamp
// can be compared exactly.
var reportStart = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// maxRecordedBytes is the cap the specification puts on recorded output.
const maxRecordedBytes = 8 << 20

// truncationLine is the line that marks output which hit that cap.
const truncationLine = "… output truncated by katana at 8 MB …"

// mixedRunnerOutput holds exactly one case for each of the six parsers, so the
// parser that was used is named by the case that comes back. Every parser
// recovers the same number of cases from it, which is what makes it a tie.
const mixedRunnerOutput = "--- PASS: TestGoCase (0.01s)\n" +
	"tests/test_py.py::test_py_case PASSED\n" +
	"✓ js case\n" +
	"test cargo_case ... ok\n" +
	"Passed Ns.Cls.DotnetCase\n" +
	"Test Case 'XTests.testXCase' passed (0.001 seconds)\n"

// pytestHeavyOutput is the same mixture with a second pytest case, so pytest is
// the parser that recovers the most and any other parser being chosen can only
// be the configured framework's doing.
const pytestHeavyOutput = mixedRunnerOutput + "tests/test_py.py::test_py_other PASSED\n"

// --- helpers ---------------------------------------------------------------

// assertVerbose fails the test unless Verbose returns want and reports adjusted.
func assertVerbose(t *testing.T, framework, command, want string, adjusted bool) {
	t.Helper()
	got, gotAdjusted := report.Verbose(framework, command)
	if got != want {
		t.Errorf("Verbose(%q, %q) = %q, want %q", framework, command, got, want)
	}
	if gotAdjusted != adjusted {
		t.Errorf("Verbose(%q, %q) adjusted = %t, want %t", framework, command, gotAdjusted, adjusted)
	}
}

// assertUnchanged fails the test unless Verbose leaves command alone.
func assertUnchanged(t *testing.T, framework, command string) {
	t.Helper()
	assertVerbose(t, framework, command, command, false)
}

// parsedCase fails the test unless out yields exactly one case, and returns it.
func parsedCase(t *testing.T, framework, out string) report.Case {
	t.Helper()
	cases := report.Parse(framework, out)
	if len(cases) != 1 {
		t.Fatalf("Parse(%q, ...) = %d cases %+v, want 1", framework, len(cases), cases)
	}
	return cases[0]
}

// parsedCases fails the test unless out yields exactly n cases.
func parsedCases(t *testing.T, framework, out string, n int) []report.Case {
	t.Helper()
	cases := report.Parse(framework, out)
	if len(cases) != n {
		t.Fatalf("Parse(%q, ...) = %d cases %+v, want %d", framework, len(cases), cases, n)
	}
	return cases
}

// caseNamed returns the recovered case with the given name, failing the test if
// there is none.
func caseNamed(t *testing.T, cases []report.Case, name string) report.Case {
	t.Helper()
	for _, c := range cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no case named %q among %+v", name, cases)
	return report.Case{}
}

// recoveredNames lists the case names in the order they were recovered.
func recoveredNames(cases []report.Case) []string {
	out := make([]string, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.Name)
	}
	return out
}

// aCase builds a case with everything a report needs from one.
func aCase(suite, name string, status report.Status, d time.Duration) report.Case {
	return report.Case{Suite: suite, Name: name, Status: status, Duration: d}
}

// reportWith returns a report with the fields every page test shares, so a test
// only shows what it is about.
func reportWith(cases ...report.Case) *report.Report {
	return &report.Report{
		Project:   "checkout",
		Root:      "/src/checkout",
		Command:   "go test ./...",
		Framework: "go-test",
		Version:   "1.2.3",
		StartedAt: reportStart,
		Duration:  1500 * time.Millisecond,
		Parsed:    true,
		Cases:     cases,
	}
}

// writeReport writes r into a fresh directory and returns the path written.
func writeReport(t *testing.T, r *report.Report) string {
	t.Helper()
	path, err := r.WriteHTML(t.TempDir())
	if err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}
	return path
}

// renderPage writes r and returns the HTML page it produced.
func renderPage(t *testing.T, r *report.Report) string {
	t.Helper()
	body, err := os.ReadFile(writeReport(t, r))
	if err != nil {
		t.Fatalf("reading the report: %v", err)
	}
	return string(body)
}

// assertPageHas fails the test unless every want appears in the page.
func assertPageHas(t *testing.T, page string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(page, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
}

// assertPageLacks fails the test unless no unwanted string appears in the page.
func assertPageLacks(t *testing.T, page string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q, want it left out", bad)
		}
	}
}

// assertDetailHas fails the test unless a case's failure detail carries every
// want.
func assertDetailHas(t *testing.T, detail string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not contain %q", detail, want)
		}
	}
}

// renderedDuration renders one case carrying d and returns the duration cell.
func renderedDuration(t *testing.T, d time.Duration) string {
	t.Helper()
	page := renderPage(t, reportWith(aCase("pkg", "TestTimed", report.StatusPass, d)))
	const open = `<td class="du">`
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatalf("page has no duration cell:\n%s", page)
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, "</td>")
	if j < 0 {
		t.Fatalf("duration cell is never closed")
	}
	return rest[:j]
}

// syncWriter stands for the terminal: an io.Writer the two goroutines copying a
// process's stdout and stderr can share.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// recordedOutput returns what a recorder kept with any truncation notice
// removed, so the recorded suite output can be measured on its own.
func recordedOutput(got string) string {
	if i := strings.Index(got, truncationLine); i >= 0 {
		return got[:i]
	}
	return got
}

// failingWriter stands for a terminal that went away mid-suite: it reports a
// short write and an error.
type failingWriter struct {
	n   int
	err error
}

func (w failingWriter) Write(p []byte) (int, error) { return w.n, w.err }

// --- Making the runner print per-case results ------------------------------

func TestACommandWithShellSyntaxIsNeverAdjusted(t *testing.T) {
	// Parentheses are covered as a pair: a command carrying only one of them is
	// not something a project would configure.
	for _, c := range []struct{ name, command string }{
		{"pipe", "go test ./... | tee out.log"},
		{"ampersand", "go test ./... & wait"},
		{"semicolon", "go test ./... ; echo done"},
		{"redirect in", "go test ./... < cases.txt"},
		{"redirect out", "go test ./... > out.log"},
		{"backtick", "go test `ls -d ./...`"},
		{"dollar", "go test $PACKAGES"},
		{"parentheses", "(go test ./...)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUnchanged(t, "go", c.command)
		})
	}
}

func TestAGoTestCommandWithNoVerbosityFlagGetsMinusV(t *testing.T) {
	assertVerbose(t, "go-test", "go test ./...", "go test ./... -v", true)
}

func TestAGoTestCommandThatIsAlreadyVerboseIsLeftAlone(t *testing.T) {
	for _, c := range []struct{ name, command string }{
		{"-v", "go test -v ./..."},
		{"-json", "go test -json ./..."},
		{"-test.v", "go test -test.v ./..."},
		{"-v with a value", "go test -v=true ./..."},
		{"-v at the end", "go test ./... -v"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUnchanged(t, "go-test", c.command)
		})
	}
}

func TestAPytestCommandWithNoVerbosityFlagGetsMinusV(t *testing.T) {
	for _, c := range []struct{ name, command, want string }{
		{"pytest", "pytest tests", "pytest tests -v"},
		{"py.test", "py.test tests", "py.test tests -v"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertVerbose(t, "pytest", c.command, c.want, true)
		})
	}
}

func TestAPytestCommandThatAlreadySetsItsVerbosityIsLeftAlone(t *testing.T) {
	for _, c := range []struct{ name, command string }{
		{"-v", "pytest -v tests"},
		{"-vv", "pytest -vv tests"},
		{"-q", "pytest -q tests"},
		{"-qq", "pytest -qq tests"},
		{"--verbose", "pytest --verbose tests"},
		{"--quiet", "pytest --quiet tests"},
		{"--tb", "pytest --tb=short tests"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUnchanged(t, "pytest", c.command)
		})
	}
}

func TestAFlagIsOnlyAlreadyPresentAtAWordBoundary(t *testing.T) {
	// "-vet=off" contains "-v" but is not the -v flag, and "--verbosity" is not
	// pytest's --verbose, so both commands still need adjusting.
	assertVerbose(t, "go", "go test -vet=off ./...", "go test -vet=off ./... -v", true)
	assertVerbose(t, "pytest", "pytest --verbosity 2 tests", "pytest --verbosity 2 tests -v", true)
}

func TestTrailingWhitespaceIsRemovedBeforeMinusVIsAppended(t *testing.T) {
	assertVerbose(t, "go", "go test ./...   ", "go test ./... -v", true)
}

func TestACommandThatInvokesTheRunnerIsAdjustedWithNoFrameworkConfigured(t *testing.T) {
	assertVerbose(t, "", "go test ./...", "go test ./... -v", true)
	assertVerbose(t, "", "pytest tests", "pytest tests -v", true)
}

func TestTheFrameworkAloneIsNotEnoughToAdjustACommand(t *testing.T) {
	assertUnchanged(t, "go", "make test")
}

func TestAGoFrameworkRunningPytestLeavesTheCommandUnchanged(t *testing.T) {
	// Only the framework's own branch is considered, so the pytest command is
	// never reached.
	assertUnchanged(t, "go", "pytest tests")
}

func TestACommandForAnyOtherRunnerIsLeftUnchanged(t *testing.T) {
	for _, c := range []struct{ name, framework, command string }{
		{"shell script", "", "./scripts/test.sh"},
		{"make target", "", "make check"},
		{"cargo", "rust", "cargo test"},
		{"npm script", "jest", "npm test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertUnchanged(t, c.framework, c.command)
		})
	}
}

// --- Capturing suite output ------------------------------------------------

func TestEverythingWrittenThroughTheRecorderIsForwardedUnchanged(t *testing.T) {
	var term syncWriter
	var rec report.Recorder

	w := rec.Tee(&term)
	for _, chunk := range []string{"first line\n", "second line\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if got, want := term.String(), "first line\nsecond line\n"; got != want {
		t.Errorf("terminal saw %q, want %q", got, want)
	}
}

func TestOutputUnderTheLimitIsRecordedExactlyAsWritten(t *testing.T) {
	var rec report.Recorder

	if _, err := rec.Tee(&syncWriter{}).Write([]byte("plain suite output\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got, want := rec.String(), "plain suite output\n"; got != want {
		t.Errorf("recorded %q, want %q", got, want)
	}
	if strings.Contains(rec.String(), truncationLine) {
		t.Error("recorded output carries a truncation notice it did not earn")
	}
}

func TestTheRecorderKeepsAtMostEightMebibytes(t *testing.T) {
	var rec report.Recorder

	if _, err := rec.Tee(&syncWriter{}).Write(bytes.Repeat([]byte("a"), maxRecordedBytes+4096)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := strings.Count(recordedOutput(rec.String()), "a"); got != maxRecordedBytes {
		t.Errorf("recorded %d bytes of output, want the %d byte limit", got, maxRecordedBytes)
	}
}

func TestTheWriteThatCrossesTheLimitIsRecordedUpToItAndTheRestDropped(t *testing.T) {
	var rec report.Recorder
	w := rec.Tee(&syncWriter{})

	if _, err := w.Write(bytes.Repeat([]byte("a"), maxRecordedBytes-5)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("b"), 10)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := recordedOutput(rec.String())
	if n := strings.Count(got, "a"); n != maxRecordedBytes-5 {
		t.Errorf("recorded %d bytes before the limit, want %d", n, maxRecordedBytes-5)
	}
	if n := strings.Count(got, "b"); n != 5 {
		t.Errorf("recorded %d bytes of the crossing write, want the 5 that fit", n)
	}
}

func TestTruncatedOutputEndsWithTheTruncationNoticeOnItsOwnLine(t *testing.T) {
	var rec report.Recorder

	if _, err := rec.Tee(&syncWriter{}).Write(bytes.Repeat([]byte("a"), maxRecordedBytes+1)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := rec.String()
	if !strings.HasSuffix(got, "\n") {
		t.Error("recorded output does not end with a line break after the notice")
	}
	ls := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if last := ls[len(ls)-1]; last != truncationLine {
		t.Errorf("last recorded line = %q, want %q", last, truncationLine)
	}
}

func TestTwoStreamsCanBeRecordedConcurrentlyWithoutCorruptingTheOutput(t *testing.T) {
	// Stands for a process's stdout and stderr, which exec.Cmd copies from
	// separate goroutines. Run under -race this is also a data race check.
	const writes = 500
	var term syncWriter
	var rec report.Recorder

	var wg sync.WaitGroup
	for _, chunk := range []string{"out\n", "err\n"} {
		wg.Add(1)
		w := rec.Tee(&term)
		go func(chunk string) {
			defer wg.Done()
			for i := 0; i < writes; i++ {
				if _, err := w.Write([]byte(chunk)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(chunk)
	}
	wg.Wait()

	got := rec.String()
	if n := len(got); n != writes*len("out\nerr\n") {
		t.Errorf("recorded %d bytes, want every byte of both streams", n)
	}
	for _, chunk := range []string{"out", "err"} {
		if n := strings.Count(got, chunk); n != writes {
			t.Errorf("recorded %d %q chunks, want %d", n, chunk, writes)
		}
	}
}

func TestARecordingFailureNeverChangesWhatTheCallerSees(t *testing.T) {
	boom := errors.New("terminal closed")
	var rec report.Recorder

	n, err := rec.Tee(failingWriter{n: 3, err: boom}).Write([]byte("suite output"))

	if n != 3 {
		t.Errorf("Write returned %d bytes, want the underlying writer's 3", n)
	}
	if !errors.Is(err, boom) {
		t.Errorf("Write returned %v, want the underlying writer's error", err)
	}
}

// --- Recognised frameworks and how a parser is chosen ----------------------

func TestFrameworksListsTheRunnersPerCaseResultsAreRecoveredFrom(t *testing.T) {
	want := []string{"go-test", "pytest", "jest", "vitest", "mocha", "cargo-test", "xunit", "xctest"}

	got := report.Frameworks()

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Frameworks() = %v, want %v", got, want)
	}
}

func TestTheFrameworkNameIsMatchedIgnoringCaseAndSurroundingWhitespace(t *testing.T) {
	// The fallback for this fixture is the Go parser, so the pytest case coming
	// back can only mean the padded, mixed-case name was recognised.
	for _, framework := range []string{"PyTest", "  pytest  ", "\tPYTEST\n"} {
		t.Run(framework, func(t *testing.T) {
			if got := parsedCase(t, framework, mixedRunnerOutput).Name; got != "test_py_case" {
				t.Errorf("case = %q, want the pytest case", got)
			}
		})
	}
}

func TestEachFrameworkAliasSelectsItsParser(t *testing.T) {
	// pytestHeavyOutput is won by the pytest parser on count, so any other
	// parser's case coming back is the configured framework's doing.
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"go", "golang", "go-test", "gotest", "gotestsum"}, "TestGoCase"},
		{[]string{"jest", "vitest", "mocha", "jasmine", "ava", "javascript", "typescript", "js", "ts"}, "js case"},
		{[]string{"cargo", "cargo-test", "rust"}, "cargo_case"},
		{[]string{"xunit", "nunit", "mstest", "dotnet", "csharp"}, "DotnetCase"},
		{[]string{"xctest", "swift-testing", "swift"}, "testXCase"},
	} {
		for _, name := range c.names {
			t.Run(name, func(t *testing.T) {
				if got := parsedCase(t, name, pytestHeavyOutput).Name; got != c.want {
					t.Errorf("case = %q, want %q", got, c.want)
				}
			})
		}
	}
}

func TestThePytestAliasesSelectThePytestParser(t *testing.T) {
	// mixedRunnerOutput falls back to the Go parser, so the pytest case naming
	// itself is what proves the alias.
	for _, name := range []string{"pytest", "py.test", "python", "py"} {
		t.Run(name, func(t *testing.T) {
			if got := parsedCase(t, name, mixedRunnerOutput).Name; got != "test_py_case" {
				t.Errorf("case = %q, want the pytest case", got)
			}
		})
	}
}

func TestColourCodesAreRemovedBeforeParsing(t *testing.T) {
	out := "\x1b[32m--- PASS: TestColoured (0.01s)\x1b[0m\n"

	if got := parsedCase(t, "go-test", out).Name; got != "TestColoured" {
		t.Errorf("case = %q, want the coloured line to parse like a plain one", got)
	}
}

func TestCarriageReturnsAreTreatedAsLineBreaks(t *testing.T) {
	for _, c := range []struct{ name, sep string }{
		{"crlf", "\r\n"},
		{"lone cr", "\r"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := "--- PASS: TestOne (0.01s)" + c.sep + "--- FAIL: TestTwo (0.02s)" + c.sep

			cases := parsedCases(t, "go-test", out, 2)

			if cases[0].Name != "TestOne" || cases[1].Name != "TestTwo" {
				t.Errorf("cases = %v, want both lines parsed", recoveredNames(cases))
			}
		})
	}
}

func TestTheConfiguredParserWinsWheneverItRecoversAnyCases(t *testing.T) {
	// The Go parser recovers one case here and pytest two, and the configured
	// framework still decides.
	if got := parsedCase(t, "go-test", pytestHeavyOutput).Name; got != "TestGoCase" {
		t.Errorf("case = %q, want the configured framework's parser to win", got)
	}
}

func TestAnUnknownFrameworkFallsBackToTheParserThatRecoversTheMost(t *testing.T) {
	cases := parsedCases(t, "haskell", pytestHeavyOutput, 2)

	if got := recoveredNames(cases); got[0] != "test_py_case" || got[1] != "test_py_other" {
		t.Errorf("cases = %v, want the two pytest cases", got)
	}
}

func TestAConfiguredParserThatRecoversNothingFallsBackToTheOthers(t *testing.T) {
	out := "--- PASS: TestGoCase (0.01s)\n"

	if got := parsedCase(t, "cargo", out).Name; got != "TestGoCase" {
		t.Errorf("case = %q, want the fallback to read the go output", got)
	}
}

func TestATieBetweenParsersIsBrokenByTheFixedOrder(t *testing.T) {
	// Every parser recovers one case from this fixture, and go is first in the
	// fixed order, so the same output always produces the same report.
	if got := parsedCase(t, "", mixedRunnerOutput).Name; got != "TestGoCase" {
		t.Errorf("case = %q, want the earliest parser in the fixed order to win", got)
	}
}

func TestOutputNoParserRecognisesYieldsNoCases(t *testing.T) {
	out := "building project\nnothing here looks like a test result\ndone in 4s\n"

	if cases := report.Parse("go-test", out); len(cases) != 0 {
		t.Errorf("Parse recovered %+v, want no cases", cases)
	}
}

// --- Reading go test output ------------------------------------------------

func TestAGoResultLineRecordsItsStatus(t *testing.T) {
	for _, c := range []struct {
		line   string
		want   report.Status
		name   string
		result string
	}{
		{"--- PASS: TestOne (0.12s)", report.StatusPass, "TestOne", "PASS"},
		{"--- FAIL: TestTwo (0.12s)", report.StatusFail, "TestTwo", "FAIL"},
		{"--- SKIP: TestThree (0.12s)", report.StatusSkip, "TestThree", "SKIP"},
	} {
		t.Run(c.result, func(t *testing.T) {
			got := parsedCase(t, "go-test", c.line+"\n")

			if got.Name != c.name || got.Status != c.want {
				t.Errorf("case = %q/%s, want %q/%s", got.Name, got.Status, c.name, c.want)
			}
			if got.Duration != 120*time.Millisecond {
				t.Errorf("duration = %s, want 120ms", got.Duration)
			}
		})
	}
}

func TestAnIndentedGoResultLineIsRecordedAsItsOwnCase(t *testing.T) {
	out := "--- FAIL: TestParent (0.02s)\n    --- FAIL: TestParent/child (0.01s)\n"

	cases := parsedCases(t, "go-test", out, 2)

	if cases[1].Name != "TestParent/child" {
		t.Errorf("subtest case = %q, want the indented result recorded too", cases[1].Name)
	}
}

func TestAGoPackageSummaryAssignsTheSuiteOfTheCasesSinceTheLastOne(t *testing.T) {
	out := "--- PASS: TestCart (0.01s)\n" +
		"ok  \tgithub.com/x/cart\t0.104s\n" +
		"--- FAIL: TestCheckout (0.02s)\n" +
		"FAIL\tgithub.com/x/checkout\t0.312s\n" +
		"?   \tgithub.com/x/empty\t[no test files]\n"

	cases := parsedCases(t, "go-test", out, 2)

	if cases[0].Suite != "github.com/x/cart" {
		t.Errorf("first case suite = %q, want the package that reported it", cases[0].Suite)
	}
	if cases[1].Suite != "github.com/x/checkout" {
		t.Errorf("second case suite = %q, want only the cases since the previous summary", cases[1].Suite)
	}
}

func TestAGoPackageThatFailedToBuildGetsAFailedCaseOfItsOwn(t *testing.T) {
	out := "# github.com/x/broken\n" +
		"./broken_test.go:9:2: undefined: Missing\n" +
		"FAIL\tgithub.com/x/broken [build failed]\n"

	got := parsedCase(t, "go-test", out)

	if got.Name != "build failed" {
		t.Errorf("case name = %q, want the bracketed note without its brackets", got.Name)
	}
	if got.Status != report.StatusFail {
		t.Errorf("status = %s, want a failure so the package does not read as empty and passing", got.Status)
	}
	if got.Suite != "github.com/x/broken" {
		t.Errorf("suite = %q, want the package that failed", got.Suite)
	}
}

func TestGoDetailBetweenRunAndTheResultIsAttachedToAFailedCase(t *testing.T) {
	out := "=== RUN   TestCheckout\n" +
		"    checkout_test.go:42: total = 100, want 90\n" +
		"--- FAIL: TestCheckout (0.02s)\n"

	if got := parsedCase(t, "go-test", out).Output; !strings.Contains(got, "total = 100, want 90") {
		t.Errorf("detail = %q, want the failure output the runner printed", got)
	}
}

func TestGoDetailIsAttachedToASkippedCase(t *testing.T) {
	out := "=== RUN   TestOffline\n" +
		"    offline_test.go:7: needs network\n" +
		"--- SKIP: TestOffline (0.00s)\n"

	if got := parsedCase(t, "go-test", out).Output; !strings.Contains(got, "needs network") {
		t.Errorf("detail = %q, want the skip reason", got)
	}
}

func TestAPassingGoCaseCarriesNoDetail(t *testing.T) {
	out := "=== RUN   TestNoisy\n" +
		"    noisy_test.go:9: some chatter\n" +
		"--- PASS: TestNoisy (0.01s)\n"

	if got := parsedCase(t, "go-test", out).Output; got != "" {
		t.Errorf("detail = %q, want a passing case to carry none", got)
	}
}

func TestAGoRunLineDiscardsDetailBufferedSoFar(t *testing.T) {
	for _, marker := range []string{"=== RUN   TestB", "=== PAUSE TestB", "=== CONT  TestB", "=== NAME  TestB"} {
		t.Run(strings.Fields(marker)[1], func(t *testing.T) {
			out := "    stale chatter from another case\n" + marker + "\n--- FAIL: TestB (0.01s)\n"

			if got := parsedCase(t, "go-test", out).Output; got != "" {
				t.Errorf("detail = %q, want the buffered lines discarded", got)
			}
		})
	}
}

func TestABareGoPassOrFailLineDiscardsDetailBufferedSoFar(t *testing.T) {
	for _, marker := range []string{"PASS", "FAIL"} {
		t.Run(marker, func(t *testing.T) {
			out := "    stale chatter\n" + marker + "\n--- FAIL: TestB (0.01s)\n"

			if got := parsedCase(t, "go-test", out).Output; got != "" {
				t.Errorf("detail = %q, want the buffered lines discarded", got)
			}
		})
	}
}

// --- Reading pytest output -------------------------------------------------

func TestAPytestVerboseLineRecordsTheFileAsSuiteAndTheTestAsName(t *testing.T) {
	got := parsedCase(t, "pytest", "tests/unit/test_cart.py::test_adds_item PASSED\n")

	if got.Suite != "tests/unit/test_cart.py" {
		t.Errorf("suite = %q, want the file", got.Suite)
	}
	if got.Name != "test_adds_item" {
		t.Errorf("name = %q, want the test", got.Name)
	}
}

func TestAPytestShortSummaryLineRecordsACaseWithItsMessageAsDetail(t *testing.T) {
	got := parsedCase(t, "pytest", "FAILED tests/test_cart.py::test_total - AssertionError: 100 != 90\n")

	if got.Suite != "tests/test_cart.py" || got.Name != "test_total" {
		t.Errorf("case = %q::%q, want the file and test from the summary line", got.Suite, got.Name)
	}
	if got.Status != report.StatusFail {
		t.Errorf("status = %s, want a failure", got.Status)
	}
	if got.Output != "AssertionError: 100 != 90" {
		t.Errorf("detail = %q, want the text after \" - \"", got.Output)
	}
}

func TestPytestOutcomesMapOntoPassFailAndSkip(t *testing.T) {
	for _, c := range []struct {
		outcome string
		want    report.Status
	}{
		{"PASSED", report.StatusPass},
		{"XPASS", report.StatusPass},
		{"FAILED", report.StatusFail},
		{"ERROR", report.StatusFail},
		{"SKIPPED", report.StatusSkip},
		{"XFAIL", report.StatusSkip},
	} {
		t.Run(c.outcome, func(t *testing.T) {
			out := "tests/test_a.py::test_a " + c.outcome + "\n"

			if got := parsedCase(t, "pytest", out).Status; got != c.want {
				t.Errorf("%s recorded as %s, want %s", c.outcome, got, c.want)
			}
		})
	}
}

func TestTheFirstAppearanceOfAPytestCaseSetsItsStatus(t *testing.T) {
	out := "tests/test_a.py::test_a PASSED\n" +
		"FAILED tests/test_a.py::test_a - flaky rerun\n"

	got := parsedCase(t, "pytest", out)

	if got.Status != report.StatusPass {
		t.Errorf("status = %s, want the first occurrence's status", got.Status)
	}
	if got.Output != "flaky rerun" {
		t.Errorf("detail = %q, want the later occurrence to fill in the missing detail", got.Output)
	}
}

func TestALaterPytestOccurrenceDoesNotReplaceDetailAlreadyRecorded(t *testing.T) {
	out := "FAILED tests/test_a.py::test_a - first message\n" +
		"FAILED tests/test_a.py::test_a - second message\n"

	if got := parsedCase(t, "pytest", out).Output; got != "first message" {
		t.Errorf("detail = %q, want the detail recorded first", got)
	}
}

func TestPytestReportsNothingWithoutPerCaseLines(t *testing.T) {
	out := "=================================== FAILURES ===================================\n" +
		"_______________________________ test_alpha _______________________________\n" +
		"    assert 1 == 2\n" +
		"=========================== short test summary info ============================\n"

	if cases := report.Parse("pytest", out); len(cases) != 0 {
		t.Errorf("Parse recovered %+v, want nothing without per-case lines", cases)
	}
}

func TestAPytestFailuresSectionTracebackIsAttachedToItsCase(t *testing.T) {
	out := "tests/test_a.py::test_alpha FAILED\n" +
		"=================================== FAILURES ===================================\n" +
		"_______________________________ test_alpha _______________________________\n" +
		"    assert cart.total == 90\n" +
		"E   AssertionError\n"

	got := parsedCase(t, "pytest", out).Output

	assertDetailHas(t, got, "assert cart.total == 90", "E   AssertionError")
}

func TestPytestTracebackCollectionEndsAtTheNextHeader(t *testing.T) {
	out := "tests/test_a.py::test_alpha FAILED\n" +
		"tests/test_a.py::test_beta FAILED\n" +
		"____ test_alpha ____\n" +
		"alpha body\n" +
		"____ test_beta ____\n" +
		"beta body\n"

	cases := parsedCases(t, "pytest", out, 2)

	if got := caseNamed(t, cases, "test_alpha").Output; got != "alpha body" {
		t.Errorf("test_alpha detail = %q, want only its own traceback", got)
	}
	if got := caseNamed(t, cases, "test_beta").Output; got != "beta body" {
		t.Errorf("test_beta detail = %q, want its own traceback", got)
	}
}

func TestPytestTracebackCollectionEndsAtALineBeginningWithEquals(t *testing.T) {
	out := "tests/test_a.py::test_alpha FAILED\n" +
		"____ test_alpha ____\n" +
		"alpha body\n" +
		"=========================== short test summary info ============================\n" +
		"not part of the traceback\n"

	got := parsedCase(t, "pytest", out).Output

	if got != "alpha body" {
		t.Errorf("detail = %q, want collection to stop at the === line", got)
	}
}

func TestACollectedPytestTracebackNeverReplacesDetailAlreadyRecorded(t *testing.T) {
	out := "FAILED tests/test_a.py::test_alpha - AssertionError: short\n" +
		"____ test_alpha ____\n" +
		"the long traceback\n"

	if got := parsedCase(t, "pytest", out).Output; got != "AssertionError: short" {
		t.Errorf("detail = %q, want the detail the case already had", got)
	}
}

func TestAPytestHeaderNamingAClassMatchesTheCaseRecordedWithColons(t *testing.T) {
	out := "tests/test_a.py::TestCart::test_total FAILED\n" +
		"____ TestCart.test_total ____\n" +
		"class traceback\n"

	if got := parsedCase(t, "pytest", out).Output; got != "class traceback" {
		t.Errorf("detail = %q, want the dotted header matched to the ::-separated case", got)
	}
}

func TestAPytestHeaderWithNoExactMatchIsMatchedByItsLastSegment(t *testing.T) {
	out := "tests/test_a.py::test_alpha FAILED\n" +
		"____ tests.unit.deep.test_alpha ____\n" +
		"last segment traceback\n"

	if got := parsedCase(t, "pytest", out).Output; got != "last segment traceback" {
		t.Errorf("detail = %q, want the header matched by its last segment", got)
	}
}

func TestAPytestHeaderThatMatchesNoCaseIsDiscarded(t *testing.T) {
	out := "tests/test_a.py::test_alpha FAILED\n" +
		"____ test_nobody ____\n" +
		"orphan traceback\n"

	if got := parsedCase(t, "pytest", out).Output; got != "" {
		t.Errorf("detail = %q, want the unmatched traceback discarded", got)
	}
}

// --- Reading jest, vitest and mocha output ---------------------------------

func TestAJSFileLineSetsTheSuiteOfTheCasesThatFollow(t *testing.T) {
	for _, marker := range []string{"PASS", "FAIL", "RUNS"} {
		t.Run(marker, func(t *testing.T) {
			out := marker + " src/cart.test.ts\n  ✓ adds an item\n"

			if got := parsedCase(t, "jest", out).Suite; got != "src/cart.test.ts" {
				t.Errorf("suite = %q, want the file the line named", got)
			}
		})
	}
}

func TestJSCaseMarkersRecordTheirStatus(t *testing.T) {
	for _, c := range []struct {
		markers []string
		want    report.Status
	}{
		{[]string{"✓", "✔"}, report.StatusPass},
		{[]string{"✕", "✗", "×", "✘"}, report.StatusFail},
		{[]string{"○", "◯", "↓", "⊘", "✎"}, report.StatusSkip},
	} {
		for _, marker := range c.markers {
			t.Run(marker, func(t *testing.T) {
				out := marker + " adds an item\n"

				got := parsedCase(t, "jest", out)

				if got.Status != c.want {
					t.Errorf("%q recorded as %s, want %s", marker, got.Status, c.want)
				}
				if got.Name != "adds an item" {
					t.Errorf("name = %q, want the case name after the marker", got.Name)
				}
			})
		}
	}
}

func TestATrailingMillisecondTimingOnAJSCaseIsItsDuration(t *testing.T) {
	if got := parsedCase(t, "jest", "✓ adds an item (123 ms)\n").Duration; got != 123*time.Millisecond {
		t.Errorf("duration = %s, want 123ms", got)
	}
}

func TestAJSCaseWithNoTimingHasNoDuration(t *testing.T) {
	if got := parsedCase(t, "jest", "✓ adds an item\n").Duration; got != 0 {
		t.Errorf("duration = %s, want none", got)
	}
}

func TestAJSPerFileRollUpIsNotRecordedAsACase(t *testing.T) {
	for _, c := range []struct{ name, line string }{
		{"plural", "✓ src/cart.test.ts (3 tests) 5ms"},
		{"singular", "✓ src/cart.test.ts (1 test) 2ms"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := c.line + "\n✓ adds an item\n"

			got := parsedCase(t, "jest", out)

			if got.Name != "adds an item" {
				t.Errorf("recovered %q, want only the real case", got.Name)
			}
		})
	}
}

func TestSurroundingWhitespaceOnAJSLineIsIgnoredAndTheNameTrimmed(t *testing.T) {
	out := "   ✓   adds an item   \n"

	if got := parsedCase(t, "jest", out).Name; got != "adds an item" {
		t.Errorf("name = %q, want it trimmed", got)
	}
}

func TestAJSCaseSeenBeforeAnyFileLineHasAnEmptySuite(t *testing.T) {
	if got := parsedCase(t, "jest", "✓ adds an item\n").Suite; got != "" {
		t.Errorf("suite = %q, want it empty until a file line names one", got)
	}
}

// --- Reading cargo test output ---------------------------------------------

func TestACargoResultLineRecordsItsStatus(t *testing.T) {
	for _, c := range []struct {
		outcome string
		want    report.Status
	}{
		{"ok", report.StatusPass},
		{"FAILED", report.StatusFail},
		{"ignored", report.StatusSkip},
	} {
		t.Run(c.outcome, func(t *testing.T) {
			out := "test cart::tests::adds_item ... " + c.outcome + "\n"

			got := parsedCase(t, "cargo", out)

			if got.Name != "cart::tests::adds_item" {
				t.Errorf("name = %q, want the test path", got.Name)
			}
			if got.Status != c.want {
				t.Errorf("status = %s, want %s", got.Status, c.want)
			}
		})
	}
}

func TestACargoRunningLineSetsTheTargetOfTheCasesThatFollow(t *testing.T) {
	out := "     Running unittests src/lib.rs (target/debug/deps/cart-9f2)\n" +
		"test adds_item ... ok\n"

	if got := parsedCase(t, "cargo", out).Suite; got != "unittests src/lib.rs" {
		t.Errorf("suite = %q, want the trimmed target", got)
	}
}

func TestCargoReportsNothingWithoutTestResultLines(t *testing.T) {
	out := "     Running unittests src/lib.rs (target/debug/deps/cart-9f2)\n" +
		"running 0 tests\n"

	if cases := report.Parse("cargo", out); len(cases) != 0 {
		t.Errorf("Parse recovered %+v, want nothing without result lines", cases)
	}
}

func TestACargoStdoutHeaderCollectsTheFailureBodyForThatCase(t *testing.T) {
	out := "test adds_item ... FAILED\n" +
		"failures:\n" +
		"\n" +
		"---- adds_item stdout ----\n" +
		"thread 'adds_item' panicked at src/lib.rs:12:\n" +
		"assertion failed: total == 90\n"

	got := parsedCase(t, "cargo", out).Output

	assertDetailHas(t, got, "panicked at src/lib.rs:12", "assertion failed: total == 90")
}

func TestCargoBodyCollectionEndsAtAFailuresLine(t *testing.T) {
	out := "test adds_item ... FAILED\n" +
		"---- adds_item stdout ----\n" +
		"the panic\n" +
		"failures:\n" +
		"    adds_item\n"

	if got := parsedCase(t, "cargo", out).Output; got != "the panic" {
		t.Errorf("detail = %q, want collection to stop at the failures: line", got)
	}
}

func TestCargoBodyCollectionEndsAtTheNextTestResultLine(t *testing.T) {
	out := "test adds_item ... FAILED\n" +
		"---- adds_item stdout ----\n" +
		"the panic\n" +
		"test removes_item ... ok\n" +
		"trailing chatter\n"

	cases := parsedCases(t, "cargo", out, 2)

	if got := caseNamed(t, cases, "adds_item").Output; got != "the panic" {
		t.Errorf("detail = %q, want collection to stop at the next result line", got)
	}
}

func TestACargoBodyGoesToTheFirstCaseOfThatNameWithNoDetailYet(t *testing.T) {
	out := "     Running unittests src/lib.rs (target/debug/deps/a)\n" +
		"test dup ... FAILED\n" +
		"     Running tests/integration.rs (target/debug/deps/b)\n" +
		"test dup ... FAILED\n" +
		"---- dup stdout ----\n" +
		"first body\n" +
		"---- dup stdout ----\n" +
		"second body\n" +
		"failures:\n"

	cases := parsedCases(t, "cargo", out, 2)

	if cases[0].Output != "first body" {
		t.Errorf("first case detail = %q, want %q", cases[0].Output, "first body")
	}
	if cases[1].Output != "second body" {
		t.Errorf("second case detail = %q, want the next body to fall to the case without one", cases[1].Output)
	}
}

func TestCargoCasesCarryNoDuration(t *testing.T) {
	if got := parsedCase(t, "cargo", "test adds_item ... ok\n").Duration; got != 0 {
		t.Errorf("duration = %s, want none: cargo does not report one", got)
	}
}

// --- Reading dotnet test output --------------------------------------------

func TestADotnetResultLineRecordsItsStatus(t *testing.T) {
	for _, c := range []struct {
		outcome string
		want    report.Status
	}{
		{"Passed", report.StatusPass},
		{"Failed", report.StatusFail},
		{"Skipped", report.StatusSkip},
	} {
		t.Run(c.outcome, func(t *testing.T) {
			out := c.outcome + " Shop.Tests.CartTests.AddsItem\n"

			if got := parsedCase(t, "xunit", out).Status; got != c.want {
				t.Errorf("status = %s, want %s", got, c.want)
			}
		})
	}
}

func TestADotnetNameIsSplitAtItsLastDot(t *testing.T) {
	got := parsedCase(t, "xunit", "Passed Shop.Tests.CartTests.AddsItem\n")

	if got.Suite != "Shop.Tests.CartTests" {
		t.Errorf("suite = %q, want everything before the last dot", got.Suite)
	}
	if got.Name != "AddsItem" {
		t.Errorf("name = %q, want the final segment", got.Name)
	}
}

func TestADotnetNameWithNoUsableDotIsRecordedWhole(t *testing.T) {
	for _, c := range []struct{ name, full string }{
		{"no dot", "AddsItem"},
		{"dot first", ".AddsItem"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := parsedCase(t, "xunit", "Passed "+c.full+"\n")

			if got.Suite != "" {
				t.Errorf("suite = %q, want it empty", got.Suite)
			}
			if got.Name != c.full {
				t.Errorf("name = %q, want the whole string %q", got.Name, c.full)
			}
		})
	}
}

func TestADotnetBracketedTimingIsRecordedAsTheDuration(t *testing.T) {
	for _, c := range []struct {
		timing string
		want   time.Duration
	}{
		{"[12 ms]", 12 * time.Millisecond},
		{"[1 s]", time.Second},
		{"[2 m]", 2 * time.Minute},
	} {
		t.Run(c.timing, func(t *testing.T) {
			out := "Passed Shop.CartTests.AddsItem " + c.timing + "\n"

			if got := parsedCase(t, "xunit", out).Duration; got != c.want {
				t.Errorf("duration = %s, want %s", got, c.want)
			}
		})
	}
}

func TestADotnetLessThanOneMillisecondTimingIsReadAsOneMillisecond(t *testing.T) {
	out := "Passed Shop.CartTests.AddsItem [< 1 ms]\n"

	if got := parsedCase(t, "xunit", out).Duration; got != time.Millisecond {
		t.Errorf("duration = %s, want 1ms with the < ignored", got)
	}
}

func TestAnUnreadableDotnetTimingIsNoDuration(t *testing.T) {
	for _, c := range []struct{ name, timing string }{
		{"not a number and a unit", "[quick]"},
		{"number does not parse", "[x ms]"},
		{"unknown unit", "[12 hours]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := "Passed Shop.CartTests.AddsItem " + c.timing + "\n"

			if got := parsedCase(t, "xunit", out).Duration; got != 0 {
				t.Errorf("duration = %s, want none", got)
			}
		})
	}
}

func TestSurroundingWhitespaceOnADotnetLineIsIgnored(t *testing.T) {
	out := "  \tPassed Shop.CartTests.AddsItem [12 ms]  \n"

	if got := parsedCase(t, "xunit", out).Name; got != "AddsItem" {
		t.Errorf("name = %q, want the padded line to parse", got)
	}
}

// --- Reading XCTest output -------------------------------------------------

func TestAnXCTestResultLineRecordsItsStatus(t *testing.T) {
	for _, c := range []struct {
		outcome string
		want    report.Status
	}{
		{"passed", report.StatusPass},
		{"failed", report.StatusFail},
		{"skipped", report.StatusSkip},
	} {
		t.Run(c.outcome, func(t *testing.T) {
			out := "Test Case 'FooTests.testBar' " + c.outcome + " (0.004 seconds)\n"

			got := parsedCase(t, "xctest", out)

			if got.Status != c.want {
				t.Errorf("status = %s, want %s", got.Status, c.want)
			}
			if got.Duration != 4*time.Millisecond {
				t.Errorf("duration = %s, want 4ms", got.Duration)
			}
		})
	}
}

func TestAnXCTestTimingIsAcceptedInSingularAndPlural(t *testing.T) {
	for _, c := range []struct {
		timing string
		want   time.Duration
	}{
		{"1 second", time.Second},
		{"2.000 seconds", 2 * time.Second},
	} {
		t.Run(c.timing, func(t *testing.T) {
			out := "Test Case 'FooTests.testBar' passed (" + c.timing + ")\n"

			if got := parsedCase(t, "xctest", out).Duration; got != c.want {
				t.Errorf("duration = %s, want %s", got, c.want)
			}
		})
	}
}

func TestAnObjectiveCStyleXCTestNameIsStrippedBeforeItIsSplit(t *testing.T) {
	out := "Test Case '-[FooTests testBar]' passed (0.004 seconds)\n"

	got := parsedCase(t, "xctest", out)

	if got.Suite != "FooTests" || got.Name != "testBar" {
		t.Errorf("case = %q/%q, want FooTests/testBar", got.Suite, got.Name)
	}
}

func TestAnXCTestNameIsSplitAtItsLastSpaceOrDot(t *testing.T) {
	for _, c := range []struct{ full, suite, name string }{
		{"FooTests.testBar", "FooTests", "testBar"},
		{"-[FooTests testBar]", "FooTests", "testBar"},
	} {
		t.Run(c.full, func(t *testing.T) {
			out := "Test Case '" + c.full + "' passed (0.004 seconds)\n"

			got := parsedCase(t, "xctest", out)

			if got.Suite != c.suite || got.Name != c.name {
				t.Errorf("case = %q/%q, want %q/%q", got.Suite, got.Name, c.suite, c.name)
			}
		})
	}
}

func TestAnXCTestNameWithNoSpaceOrDotHasAnEmptySuite(t *testing.T) {
	out := "Test Case 'testBar' passed (0.004 seconds)\n"

	got := parsedCase(t, "xctest", out)

	if got.Suite != "" {
		t.Errorf("suite = %q, want it empty", got.Suite)
	}
	if got.Name != "testBar" {
		t.Errorf("name = %q, want the whole name", got.Name)
	}
}

// --- Failure detail attached to cases --------------------------------------

func TestAtMostTwoHundredDetailLinesAreKeptPerCase(t *testing.T) {
	var b strings.Builder
	b.WriteString("=== RUN   TestNoisy\n")
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&b, "    detail line %d\n", i)
	}
	b.WriteString("--- FAIL: TestNoisy (0.01s)\n")

	got := parsedCase(t, "go-test", b.String()).Output

	if n := len(strings.Split(got, "\n")); n != 200 {
		t.Errorf("detail spans %d lines, want the first 200", n)
	}
	if !strings.Contains(got, "detail line 200") || strings.Contains(got, "detail line 201") {
		t.Error("detail does not stop at the two hundredth line")
	}
}

func TestBlankLinesBeforeTheFirstDetailLineAreIgnored(t *testing.T) {
	out := "=== RUN   TestNoisy\n" +
		"\n" +
		"   \n" +
		"    first real line\n" +
		"--- FAIL: TestNoisy (0.01s)\n"

	if got := parsedCase(t, "go-test", out).Output; got != "first real line" {
		t.Errorf("detail = %q, want the leading blank lines ignored", got)
	}
}

func TestTrailingSpacesAndTabsAreRemovedFromEachDetailLine(t *testing.T) {
	out := "=== RUN   TestNoisy\n" +
		"    first line   \t\n" +
		"    second line\n" +
		"--- FAIL: TestNoisy (0.01s)\n"

	if got := parsedCase(t, "go-test", out).Output; got != "first line\nsecond line" {
		t.Errorf("detail = %q, want each line's trailing whitespace removed", got)
	}
}

func TestTheCommonLeadingIndentationIsRemovedFromTheDetail(t *testing.T) {
	out := "=== RUN   TestNoisy\n" +
		"        outer\n" +
		"            inner\n" +
		"--- FAIL: TestNoisy (0.01s)\n"

	if got := parsedCase(t, "go-test", out).Output; got != "outer\n    inner" {
		t.Errorf("detail = %q, want the shared indentation removed and the rest kept", got)
	}
}

func TestBlankLinesAtEitherEndOfTheDetailAreRemoved(t *testing.T) {
	out := "=== RUN   TestNoisy\n" +
		"    body\n" +
		"\n" +
		"    \n" +
		"--- FAIL: TestNoisy (0.01s)\n"

	if got := parsedCase(t, "go-test", out).Output; got != "body" {
		t.Errorf("detail = %q, want the trailing blank lines removed", got)
	}
}

// --- Timings ---------------------------------------------------------------

func TestATimingReportedAsZeroStaysDistinguishableFromNoTiming(t *testing.T) {
	// go test prints 0.00s for anything under 5ms, and a suite that fast should
	// not read as untimed.
	got := parsedCase(t, "go-test", "--- PASS: TestFast (0.00s)\n")

	if got.Duration <= 0 {
		t.Fatalf("duration = %s, want a reported zero kept as a timing", got.Duration)
	}
	if shown := renderedDuration(t, got.Duration); shown != "&lt;1ms" && shown != "<1ms" {
		t.Errorf("displayed as %q, want <1ms", shown)
	}
}

func TestATimingThatFailsToParseIsRecordedAsNoTiming(t *testing.T) {
	if got := parsedCase(t, "go-test", "--- PASS: TestOdd (1.2.3s)\n").Duration; got != 0 {
		t.Errorf("duration = %s, want none for an unreadable timing", got)
	}
}

func TestACaseWithNoTimingIsDisplayedAsAnEmDash(t *testing.T) {
	if got := renderedDuration(t, 0); got != "—" {
		t.Errorf("displayed as %q, want an em dash rather than 0s", got)
	}
}

func TestADurationUnderOneMillisecondIsDisplayedAsLessThanOneMillisecond(t *testing.T) {
	if got := renderedDuration(t, 400*time.Microsecond); got != "&lt;1ms" && got != "<1ms" {
		t.Errorf("displayed as %q, want <1ms", got)
	}
}

func TestADurationUnderOneSecondIsDisplayedInWholeMilliseconds(t *testing.T) {
	if got := renderedDuration(t, 250*time.Millisecond); got != "250ms" {
		t.Errorf("displayed as %q, want 250ms", got)
	}
}

func TestADurationUnderOneMinuteIsDisplayedInSecondsWithTwoDecimals(t *testing.T) {
	if got := renderedDuration(t, 12340*time.Millisecond); got != "12.34s" {
		t.Errorf("displayed as %q, want 12.34s", got)
	}
}

func TestADurationOfAMinuteOrMoreIsRoundedToTheNearestSecond(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Second, "1m30s"},
		{89600 * time.Millisecond, "1m30s"},
	} {
		t.Run(c.d.String(), func(t *testing.T) {
			if got := renderedDuration(t, c.d); got != c.want {
				t.Errorf("displayed as %q, want %q", got, c.want)
			}
		})
	}
}

// --- The recorded run and its totals ---------------------------------------

func TestCollectMarksTheReportParsedAndListsTheRecoveredCases(t *testing.T) {
	r := reportWith()
	r.Parsed = false
	r.Output = "--- PASS: TestOne (0.01s)\n--- FAIL: TestTwo (0.02s)\n"

	r.Collect()

	if !r.Parsed {
		t.Error("Parsed = false, want the report marked as parsed")
	}
	if got := recoveredNames(r.Cases); len(got) != 2 || got[0] != "TestOne" || got[1] != "TestTwo" {
		t.Errorf("cases = %v, want the two recovered cases", got)
	}
}

func TestCollectRecordsAStandInCaseWhenNoParserRecognisesTheOutput(t *testing.T) {
	r := reportWith()
	r.Command = "./scripts/test.sh"
	r.Duration = 4 * time.Second
	r.Output = "nothing here looks like a test result\n"

	r.Collect()

	if r.Parsed {
		t.Error("Parsed = true, want the report to say per-case results were not recovered")
	}
	if len(r.Cases) != 1 {
		t.Fatalf("cases = %+v, want one stand-in case", r.Cases)
	}
	got := r.Cases[0]
	if got.Suite != "test suite" {
		t.Errorf("suite = %q, want %q", got.Suite, "test suite")
	}
	if got.Name != "./scripts/test.sh" {
		t.Errorf("name = %q, want the test command", got.Name)
	}
	if got.Duration != 4*time.Second {
		t.Errorf("duration = %s, want the suite's duration", got.Duration)
	}
}

func TestTheStandInCasePassesOnlyWhenTheCommandExitedZero(t *testing.T) {
	for _, c := range []struct {
		exit int
		want report.Status
	}{
		{0, report.StatusPass},
		{1, report.StatusFail},
		{130, report.StatusFail},
	} {
		t.Run(fmt.Sprintf("exit %d", c.exit), func(t *testing.T) {
			r := reportWith()
			r.ExitCode = c.exit
			r.Output = "unrecognisable output\n"

			r.Collect()

			if got := r.Cases[0].Status; got != c.want {
				t.Errorf("stand-in case = %s, want %s", got, c.want)
			}
		})
	}
}

func TestTheVerdictComesFromTheExitCodeAndNotTheRecoveredCases(t *testing.T) {
	failing := reportWith(aCase("pkg", "TestOne", report.StatusFail, time.Second))
	failing.ExitCode = 0
	passing := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	passing.ExitCode = 1

	if !failing.OK() || failing.Result() != "passed" {
		t.Errorf("exit code 0 reads %q, want passed even with a failed case", failing.Result())
	}
	if passing.OK() || passing.Result() != "failed" {
		t.Errorf("exit code 1 reads %q, want failed even with only passing cases", passing.Result())
	}
}

func TestTheCaseTotalsCountPassesFailuresAndSkips(t *testing.T) {
	r := reportWith(
		aCase("pkg", "TestOne", report.StatusPass, time.Second),
		aCase("pkg", "TestTwo", report.StatusPass, time.Second),
		aCase("pkg", "TestThree", report.StatusFail, time.Second),
		aCase("pkg", "TestFour", report.StatusSkip, time.Second),
	)

	if got := r.Total(); got != 4 {
		t.Errorf("Total() = %d, want 4", got)
	}
	if got := r.Passed(); got != 2 {
		t.Errorf("Passed() = %d, want 2", got)
	}
	if got := r.Failed(); got != 1 {
		t.Errorf("Failed() = %d, want 1", got)
	}
	if got := r.Skipped(); got != 1 {
		t.Errorf("Skipped() = %d, want 1", got)
	}
}

func TestTheTotalsIncludeTheStandInCaseWhenNothingWasParsed(t *testing.T) {
	r := reportWith()
	r.ExitCode = 1
	r.Output = "unrecognisable output\n"

	r.Collect()

	if r.Total() != 1 || r.Failed() != 1 {
		t.Errorf("totals = %d cases / %d failed, want the stand-in case counted", r.Total(), r.Failed())
	}
}

func TestThePassRateIsThePercentageOfExecutedCasesThatPassed(t *testing.T) {
	r := reportWith(
		aCase("pkg", "TestOne", report.StatusPass, time.Second),
		aCase("pkg", "TestTwo", report.StatusPass, time.Second),
		aCase("pkg", "TestThree", report.StatusFail, time.Second),
		aCase("pkg", "TestFour", report.StatusSkip, time.Second),
	)

	// Two of the three executed cases passed; the skip is not counted.
	if got := r.PassRate(); got < 66.6 || got > 66.7 {
		t.Errorf("PassRate() = %v, want two thirds of the executed cases", got)
	}
}

func TestThePassRateIsZeroWithNoExecutedCases(t *testing.T) {
	for _, c := range []struct {
		name  string
		cases []report.Case
	}{
		{"no cases", nil},
		{"only skips", []report.Case{aCase("pkg", "TestSkipped", report.StatusSkip, 0)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := reportWith(c.cases...).PassRate(); got != 0 {
				t.Errorf("PassRate() = %v, want 0", got)
			}
		})
	}
}

func TestSuitesKeepTheOrderTheRunnerReportedThemIn(t *testing.T) {
	r := reportWith(
		aCase("zebra", "TestOne", report.StatusPass, time.Second),
		aCase("alpha", "TestTwo", report.StatusPass, time.Second),
		aCase("zebra", "TestThree", report.StatusPass, time.Second),
	)

	got := r.Suites()

	if len(got) != 2 || got[0].Name != "zebra" || got[1].Name != "alpha" {
		t.Fatalf("suites = %+v, want zebra then alpha", got)
	}
	if names := recoveredNames(got[0].Cases); len(names) != 2 || names[0] != "TestOne" || names[1] != "TestThree" {
		t.Errorf("zebra's cases = %v, want them in the reported order", names)
	}
}

func TestACaseWithNoSuiteIsGroupedUnderTestSuite(t *testing.T) {
	for _, c := range []struct{ name, suite string }{
		{"empty", ""},
		{"whitespace", "  \t "},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := reportWith(aCase(c.suite, "TestOne", report.StatusPass, time.Second))

			got := r.Suites()

			if len(got) != 1 || got[0].Name != "test suite" {
				t.Errorf("suites = %+v, want one named %q", got, "test suite")
			}
		})
	}
}

func TestEachSuiteReportsItsOwnTalliesAndTotalDuration(t *testing.T) {
	r := reportWith(
		aCase("pkg", "TestOne", report.StatusPass, 100*time.Millisecond),
		aCase("pkg", "TestTwo", report.StatusFail, 200*time.Millisecond),
		aCase("pkg", "TestThree", report.StatusSkip, 300*time.Millisecond),
	)

	got := r.Suites()

	if len(got) != 1 {
		t.Fatalf("suites = %+v, want one", got)
	}
	s := got[0]
	if s.Passed != 1 || s.Failed != 1 || s.Skipped != 1 {
		t.Errorf("tallies = %d/%d/%d, want one of each", s.Passed, s.Failed, s.Skipped)
	}
	if s.Duration != 600*time.Millisecond {
		t.Errorf("duration = %s, want the sum of its cases", s.Duration)
	}
}

func TestTheStaleBehaviorCountIsTheNumberOutOfDateWithTheirTests(t *testing.T) {
	r := reportWith()
	r.Behaviors = []report.Behavior{
		{Source: "behaviors/one.md", Stale: true},
		{Source: "behaviors/two.md"},
		{Source: "behaviors/three.md", Stale: true},
	}

	if got := r.StaleBehaviors(); got != 2 {
		t.Errorf("StaleBehaviors() = %d, want 2", got)
	}
}

// --- Writing the report file -----------------------------------------------

func TestTheReportDirectoryIsCreatedAlongWithMissingParents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "reports")

	path, err := reportWith().WriteHTML(dir)
	if err != nil {
		t.Fatalf("WriteHTML: %v", err)
	}

	if filepath.Dir(path) != dir {
		t.Errorf("wrote to %q, want a file inside %q", path, dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("stat of the written report: %v", err)
	}
}

func TestTheReportFileIsNamedAfterTheRunStartTime(t *testing.T) {
	path := writeReport(t, reportWith())

	if got, want := filepath.Base(path), "report-20260304-050607.html"; got != want {
		t.Errorf("file name = %q, want %q", got, want)
	}
}

func TestEachRunLeavesItsOwnReportFile(t *testing.T) {
	dir := t.TempDir()
	first := reportWith()
	second := reportWith()
	second.StartedAt = reportStart.Add(time.Minute)

	for _, r := range []*report.Report{first, second} {
		if _, err := r.WriteHTML(dir); err != nil {
			t.Fatalf("WriteHTML: %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("directory holds %d files, want one per run", len(entries))
	}
}

func TestADirectoryThatCannotBeCreatedIsReportedWithNoPath(t *testing.T) {
	// A regular file where a parent directory would go.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := reportWith().WriteHTML(filepath.Join(blocked, "reports"))

	if err == nil {
		t.Fatal("WriteHTML succeeded, want the directory error reported")
	}
	if path != "" {
		t.Errorf("path = %q, want none", path)
	}
}

func TestAFileThatCannotBeWrittenIsReportedWithNoPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	path, err := reportWith().WriteHTML(dir)

	if err == nil {
		t.Fatal("WriteHTML succeeded, want the write error reported")
	}
	if path != "" {
		t.Errorf("path = %q, want none", path)
	}
}

func TestThePageIsSelfContained(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	assertPageHas(t, page, "<style>", "<script>")
	assertPageLacks(t, page, "<link", "src=")
}

// --- What the page shows ---------------------------------------------------

func TestTheHeaderNamesTheProjectTheStartTimeTheDurationAndTheExitCode(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.ExitCode = 3

	page := renderPage(t, r)

	assertPageHas(t, page, "checkout", "4 Mar 2026, 05:06:07 UTC", "1.50s", "exit code 3")
}

func TestTheVerdictBadgeIsStyledByTheVerdict(t *testing.T) {
	for _, c := range []struct {
		exit        int
		class, word string
	}{
		{0, `class="verdict pass"`, "passed"},
		{1, `class="verdict fail"`, "failed"},
	} {
		t.Run(c.word, func(t *testing.T) {
			r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
			r.ExitCode = c.exit

			page := renderPage(t, r)

			assertPageHas(t, page, c.class+">"+c.word)
		})
	}
}

func TestTheTilesShowTheCountsAndTheWholeNumberPassRate(t *testing.T) {
	r := reportWith(
		aCase("pkg", "TestOne", report.StatusPass, time.Second),
		aCase("pkg", "TestTwo", report.StatusPass, time.Second),
		aCase("pkg", "TestThree", report.StatusFail, time.Second),
		aCase("pkg", "TestFour", report.StatusSkip, time.Second),
	)

	page := renderPage(t, r)

	// Two of three executed cases passed: 66.67% is shown as 67%.
	assertPageHas(t, page,
		`<div class="n">4</div><div class="k">cases</div>`,
		`<div class="n">2</div><div class="k">passed</div>`,
		`<div class="n">1</div><div class="k">failed</div>`,
		`<div class="n">1</div><div class="k">skipped</div>`,
		`<div class="n">67%</div><div class="k">pass rate</div>`,
	)
}

func TestTheProportionalBarIsShownOnlyWhenThereIsACase(t *testing.T) {
	withCase := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))
	if !strings.Contains(withCase, `class="bar"`) {
		t.Error("page has no pass/fail/skip bar, want one when there is a case")
	}

	empty := renderPage(t, reportWith())
	assertPageLacks(t, empty, `class="bar"`)
}

func TestTheDetailsBlockListsTheCommandRootFrameworkAndVersion(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	assertPageHas(t, page, "go test ./...", "/src/checkout", "go-test", "1.2.3")
}

func TestAnUnsetFrameworkIsShownAsUnset(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.Framework = ""

	page := renderPage(t, r)

	assertPageHas(t, page, "(unset)")
}

func TestAnUnparsedRunExplainsWhatTheResultMeans(t *testing.T) {
	r := reportWith(aCase("test suite", "./scripts/test.sh", report.StatusPass, time.Second))
	r.Parsed = false

	page := renderPage(t, r)

	assertPageHas(t, page,
		"could not recognise per-case results",
		"exit code",
		"end of this page",
		"go-test, pytest, jest, vitest, mocha, cargo-test, xunit and xctest",
	)
}

func TestASuiteSectionShowsItsNameNonZeroTalliesAndTotalDuration(t *testing.T) {
	r := reportWith(
		aCase("github.com/x/cart", "TestOne", report.StatusPass, 100*time.Millisecond),
		aCase("github.com/x/cart", "TestTwo", report.StatusFail, 150*time.Millisecond),
	)

	page := renderPage(t, r)

	assertPageHas(t, page, "github.com/x/cart", "<b>1</b> passed", "<b>1</b> failed", "250ms")
	// No case was skipped, so no skip tally belongs in the summary.
	assertPageLacks(t, page, "skipped</span>")
}

func TestASuiteWithAFailureIsExpandedWhenThePageOpens(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusFail, time.Second))

	page := renderPage(t, r)

	if !strings.Contains(page, `<details class="suite" open>`) {
		t.Error("a failing suite is not expanded, want it open so the failure is visible")
	}
}

func TestASuiteWithNoFailureStartsCollapsed(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))

	page := renderPage(t, r)

	if strings.Contains(page, `<details class="suite" open>`) {
		t.Error("a passing suite is expanded, want it collapsed")
	}
	assertPageHas(t, page, `<details class="suite">`)
}

func TestEachCaseRowShowsItsStatusPillNameDetailAndDuration(t *testing.T) {
	c := aCase("pkg", "TestBroken", report.StatusFail, 250*time.Millisecond)
	c.Output = "cart_test.go:42: total = 100, want 90"

	page := renderPage(t, reportWith(c))

	assertPageHas(t, page,
		`<span class="pill fail">fail</span>`,
		"TestBroken",
		"<pre>cart_test.go:42: total = 100, want 90</pre>",
		`<td class="du">250ms</td>`,
	)
}

func TestACaseWithNoDetailShowsNone(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	assertPageLacks(t, page, "<pre>")
}

func TestTheBehaviorsTableIsShownOnlyWhenBehaviorsWereRecorded(t *testing.T) {
	with := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	with.Behaviors = []report.Behavior{{
		Source: "behaviors/checkout.md",
		Output: "tests/checkout_test.go",
		Status: "up to date",
		Stack:  "go / go-test",
	}}

	page := renderPage(t, with)
	assertPageHas(t, page, "behaviors/checkout.md", "tests/checkout_test.go", "up to date", "go / go-test")

	without := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))
	assertPageLacks(t, without, "<h2>Behaviors</h2>")
}

func TestAStaleBehaviorIsNotedWithItsCountAndWhatToRun(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.Behaviors = []report.Behavior{
		{Source: "behaviors/one.md", Status: "stale", Stale: true},
		{Source: "behaviors/two.md", Status: "up to date"},
	}

	page := renderPage(t, r)

	assertPageHas(t, page, "1 behavior", "do not fully cover the current specification", "katana generate")
}

func TestAStaleBehaviorHasItsStatusHighlighted(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.Behaviors = []report.Behavior{{Source: "behaviors/one.md", Status: "stale", Stale: true}}

	page := renderPage(t, r)

	assertPageHas(t, page, `class="stale">stale`)
}

func TestTheFullSuiteOutputIsShownInACollapsibleSectionAtTheEnd(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.Output = "the whole captured log\n"

	page := renderPage(t, r)

	assertPageHas(t, page, "<h2>Suite output</h2>", "<details>", "the whole captured log")
	if strings.Index(page, "<h2>Suite output</h2>") < strings.Index(page, "<h2>Test cases</h2>") {
		t.Error("the suite output comes before the cases, want it at the end of the page")
	}
}

func TestTheSuiteOutputSectionIsOmittedWhenThereIsNoOutput(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.Output = ""

	page := renderPage(t, r)

	assertPageLacks(t, page, "<h2>Suite output</h2>")
}

func TestTheFooterNamesTheKatanaVersionThatWroteTheReport(t *testing.T) {
	r := reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second))
	r.Version = "9.9.9-test"

	page := renderPage(t, r)

	if i := strings.Index(page, "<footer>"); i < 0 || !strings.Contains(page[i:], "9.9.9-test") {
		t.Error("the footer does not name the katana version")
	}
}

func TestValuesTakenFromTheRunAreHTMLEscaped(t *testing.T) {
	c := aCase("pkg", "TestRenders<b>markup</b>", report.StatusFail, time.Second)
	c.Output = "want <nil>, got <script>alert(1)</script>"
	r := reportWith(c)
	r.Command = "go test ./... <all>"
	r.Output = "<img src=x onerror=alert(1)>"

	page := renderPage(t, r)

	assertPageLacks(t, page,
		"TestRenders<b>markup</b>",
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"./... <all>",
	)
	assertPageHas(t, page, "&lt;b&gt;markup&lt;/b&gt;", "&lt;script&gt;alert(1)", "&lt;img", "&lt;all&gt;")
}

// --- Filtering within the page ---------------------------------------------
//
// The filtering itself is the page's own JavaScript, which only runs in a
// browser. These tests assert the page ships the controls and the per-row data
// the filters match on; where a rule has no other trace in the file, they assert
// the embedded script states it.

func TestThePageOffersAllFailedPassedAndSkippedFilters(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	// "All" is the one pressed button when the page opens; the other three are
	// explicitly unpressed, so only ever one is pressed.
	assertPageHas(t, page, `data-filter="all" aria-pressed="true"`)
	for _, filter := range []string{"fail", "pass", "skip"} {
		assertPageHas(t, page, `data-filter="`+filter+`" aria-pressed="false"`)
	}
}

func TestOnlyOneFilterButtonIsPressedAtATime(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	if !strings.Contains(page, `String(o === b)`) {
		t.Error("the page's script does not set aria-pressed on every button when one is clicked")
	}
}

func TestCaseRowsCarryTheStatusAndLowercasedNameTheFiltersMatchOn(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestMixedCase", report.StatusSkip, time.Second)))

	assertPageHas(t, page, `data-status="skip"`, `data-name="testmixedcase"`)
}

func TestTheStatusFilterAndTheNameSearchApplyTogether(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	if !strings.Contains(page, "row.dataset.status === status") ||
		!strings.Contains(page, "row.dataset.name.indexOf(term)") ||
		!strings.Contains(page, "&&") {
		t.Error("the page's script does not require a row to satisfy both the status filter and the search")
	}
}

func TestTheSearchTermIsTrimmedAndMatchedCaseInsensitively(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	if !strings.Contains(page, "q.value.trim().toLowerCase()") {
		t.Error("the page's script does not trim and lowercase the search term")
	}
}

func TestASuiteWhoseRowsAreAllHiddenIsHiddenItself(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	if !strings.Contains(page, "suite.hidden = shown === 0") {
		t.Error("the page's script does not hide a suite with no visible row")
	}
}

func TestFilteringExpandsEverySuiteThatStillHasAVisibleRow(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	if !strings.Contains(page, "status !== 'all' || term !== ''") || !strings.Contains(page, "suite.open = true") {
		t.Error("the page's script does not expand matching suites while a filter or search is active")
	}
}

func TestThePageShowsANoMatchMessageOnlyWhenNothingIsVisible(t *testing.T) {
	page := renderPage(t, reportWith(aCase("pkg", "TestOne", report.StatusPass, time.Second)))

	assertPageHas(t, page, "No test case matches this filter.")
	if !strings.Contains(page, `id="no-match" hidden`) {
		t.Error("the no-match message does not start hidden")
	}
	if !strings.Contains(page, "none.hidden = shownTotal > 0") {
		t.Error("the page's script does not hide the no-match message once a row is visible")
	}
}
