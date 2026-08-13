// This file covers behaviors/internal/generator.md: turning one behavior
// specification into a test file by asking a harness to write it, deciding
// whether that produced anything, recovering the file from the harness's
// printed output when it did not, reporting a generation that produced nothing,
// and what the harness is asked for.
//
// A generation is only observable by running a harness, so every test about
// Generate points the generator at a stand-in agent CLI — a shell script that
// writes, prints and exits however the case under test needs — and then
// inspects the outcome, the file on disk and the error. The rules for telling
// code apart from an agent's reply are not exported on their own; they are
// exercised through the stdout fallback, which is the only way a project
// reaches them. Prompt assembly goes through generator.BuildPrompt, which is.

package internal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/generator"
	"github.com/adaptive-scale/katana/internal/harness"
)

// The fixture one behavior generates from. The output path is written with
// forward slashes throughout, the way katana.yaml and the tracker record it.
const (
	stubName     = "stub-agent" // the harness name katana reports in its errors
	behaviorPath = "behaviors/checkout.md"
	behaviorBody = "## Discounts\n\n- A valid code reduces the total.\n"
	outputPath   = "tests/checkout_test.go"
)

// goFile is a plausible generated test file, used wherever a test only needs
// "some file body" to pass around.
const goFile = "package tests\n\nimport \"testing\"\n\nfunc TestDiscountReducesTheTotal(t *testing.T) {}\n"

// stubFile is one file the stand-in agent writes before it exits.
type stubFile struct{ path, body string }

// stubAgent describes the stand-in agent CLI a generation runs instead of a
// real coding agent. Everything a generation can observe about a harness — the
// files it left behind, what it printed on each stream, and how it exited — is
// a field here.
type stubAgent struct {
	stdout  string     // printed on standard output
	stderr  string     // printed on standard error
	writes  []stubFile // project-relative paths written before exit
	empties []string   // project-relative paths left as empty files
	touches string     // project-relative marker file created first, for ordering checks
	exit    int        // exit status
}

// install writes the stand-in agent as a shell script in dir and returns its
// path. The script drains the prompt katana pipes to it before doing anything
// else, as a real harness reading its instructions would.
func (a stubAgent) install(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in agent is a shell script")
	}

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("cat > /dev/null\n")
	if a.touches != "" {
		fmt.Fprintf(&b, "mkdir -p \"$(dirname '%s')\"\n: > '%s'\n", a.touches, a.touches)
	}
	for _, f := range a.writes {
		fmt.Fprintf(&b, "mkdir -p \"$(dirname '%s')\"\n", f.path)
		fmt.Fprintf(&b, "cat > '%s' <<'KATANA_EOF'\n%s\nKATANA_EOF\n", f.path, strings.TrimRight(f.body, "\n"))
	}
	for _, p := range a.empties {
		fmt.Fprintf(&b, "mkdir -p \"$(dirname '%s')\"\n: > '%s'\n", p, p)
	}
	if a.stdout != "" {
		fmt.Fprintf(&b, "cat <<'KATANA_EOF'\n%s\nKATANA_EOF\n", strings.TrimRight(a.stdout, "\n"))
	}
	if a.stderr != "" {
		fmt.Fprintf(&b, "cat >&2 <<'KATANA_EOF'\n%s\nKATANA_EOF\n", strings.TrimRight(a.stderr, "\n"))
	}
	fmt.Fprintf(&b, "exit %d\n", a.exit)

	path := filepath.Join(dir, "stub-agent.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// newGeneratorProject lays out a project root holding files, keyed by
// project-relative forward-slash path.
func newGeneratorProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// newGenerator wires a generator to root and to the stand-in agent, the way
// `katana generate` does: the agent runs with the project root as its working
// directory, so the paths in the prompt mean the same thing to both.
func newGenerator(t *testing.T, root string, a stubAgent) *generator.Generator {
	t.Helper()
	runner, err := harness.New(stubName, harness.Spec{Command: a.install(t, t.TempDir())}, harness.Options{
		Dir:     root,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return generator.New(runner, root)
}

// request is the generation the fixture behavior asks for.
func request(out string) generator.Request {
	return generator.Request{
		BehaviorPath:    behaviorPath,
		BehaviorContent: behaviorBody,
		OutputPath:      out,
		Language:        "go",
		Framework:       "go-test",
	}
}

func generate(t *testing.T, root string, a stubAgent, req generator.Request) (*generator.Outcome, error) {
	t.Helper()
	return newGenerator(t, root, a).Generate(context.Background(), req)
}

// mustGenerate runs a generation that is expected to succeed.
func mustGenerate(t *testing.T, root string, a stubAgent, req generator.Request) *generator.Outcome {
	t.Helper()
	out, err := generate(t, root, a, req)
	if err != nil {
		t.Fatalf("generation failed: %v", err)
	}
	if out == nil {
		t.Fatal("generation returned no outcome and no error")
	}
	return out
}

// generateErr runs a generation that is expected to fail and returns its error.
func generateErr(t *testing.T, root string, a stubAgent, req generator.Request) error {
	t.Helper()
	out, err := generate(t, root, a, req)
	if err == nil {
		t.Fatalf("generation succeeded, want an error (outcome %+v)", out)
	}
	if out != nil {
		t.Errorf("outcome = %+v, want none reported alongside the error", out)
	}
	return err
}

func readOutput(t *testing.T, root, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading the generated file: %v", err)
	}
	return string(body)
}

func assertNoFile(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if body, err := os.ReadFile(p); err == nil {
		t.Errorf("%s was written after all, with %q", rel, body)
	}
}

// stdoutOnly runs a generation whose stand-in agent writes nothing and prints
// stdout, and reports what katana made of it: the recovered file body, or the
// error it gave when it would not treat the output as a file.
func stdoutOnly(t *testing.T, stdout string) (string, error) {
	t.Helper()
	root := newGeneratorProject(t, nil)
	out, err := generate(t, root, stubAgent{stdout: stdout}, request(outputPath))
	if err != nil {
		// Nothing usable means nothing written: the point of refusing is that
		// English never lands in a test file.
		assertNoFile(t, root, outputPath)
		return "", err
	}
	if out.WroteFile || !out.FromStdout {
		t.Errorf("outcome = %+v, want it reported as recovered from stdout", out)
	}
	return readOutput(t, root, outputPath), nil
}

// acceptedBody fails unless stdout was accepted as a file body, and returns it.
func acceptedBody(t *testing.T, stdout string) string {
	t.Helper()
	body, err := stdoutOnly(t, stdout)
	if err != nil {
		t.Fatalf("output was not accepted as a file body: %v\noutput:\n%s", err, stdout)
	}
	return body
}

// assertNotAFileBody fails unless katana refused to treat stdout as a file.
func assertNotAFileBody(t *testing.T, stdout string) {
	t.Helper()
	if body, err := stdoutOnly(t, stdout); err == nil {
		t.Fatalf("output was written as a file body:\n%s", body)
	}
}

// --- Producing a test file for one behavior --------------------------------

func TestGenerationProducesTheTestFileAtTheRequestedPath(t *testing.T) {
	root := newGeneratorProject(t, nil)

	out := mustGenerate(t, root, stubAgent{writes: []stubFile{{outputPath, goFile}}}, request(outputPath))

	if got := readOutput(t, root, outputPath); !strings.Contains(got, "func TestDiscountReducesTheTotal") {
		t.Errorf("%s = %q, want the harness's test file", outputPath, got)
	}
	if !out.WroteFile {
		t.Errorf("outcome = %+v, want the file reported as produced", out)
	}
}

func TestForwardSlashesInTheOutputPathBecomeDirectoriesUnderTheProjectRoot(t *testing.T) {
	root := newGeneratorProject(t, nil)

	// The agent prints rather than writes, so katana itself resolves the path;
	// the temp root is nowhere near the process's working directory, so finding
	// the file here means the path was taken relative to the project root.
	mustGenerate(t, root, stubAgent{stdout: fenced(goFile)}, request("tests/api/checkout_test.go"))

	native := filepath.Join(root, "tests", "api", "checkout_test.go")
	if _, err := os.Stat(native); err != nil {
		t.Errorf("no file at %s: %v", native, err)
	}
}

func TestExistingTestsAtTheOutputPathAreGivenToTheHarnessToUpdate(t *testing.T) {
	const existing = "package tests\n\nfunc TestOldName(t *testing.T) {}\n"
	root := newGeneratorProject(t, map[string]string{outputPath: existing})

	g := newGenerator(t, root, stubAgent{writes: []stubFile{{outputPath, goFile}}})
	var prompt string
	g.OnPrompt = func(p string) { prompt = p }
	if _, err := g.Generate(context.Background(), request(outputPath)); err != nil {
		t.Fatalf("generation failed: %v", err)
	}

	if !strings.Contains(prompt, existing) {
		t.Errorf("the file already at the output path was not shown to the harness:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Existing tests") {
		t.Errorf("the harness was not told these are the tests to update:\n%s", prompt)
	}
}

func TestWithNoFileAtTheOutputPathTheHarnessIsAskedToCreateItFresh(t *testing.T) {
	root := newGeneratorProject(t, nil)

	g := newGenerator(t, root, stubAgent{writes: []stubFile{{outputPath, goFile}}})
	var prompt string
	g.OnPrompt = func(p string) { prompt = p }
	if _, err := g.Generate(context.Background(), request(outputPath)); err != nil {
		t.Fatalf("generation failed: %v", err)
	}

	if strings.Contains(prompt, "Existing tests") {
		t.Errorf("a new behavior should not claim to have existing tests:\n%s", prompt)
	}
}

func TestThePromptObserverSeesThePromptBeforeTheHarnessRuns(t *testing.T) {
	root := newGeneratorProject(t, nil)

	g := newGenerator(t, root, stubAgent{
		touches: "agent-started",
		writes:  []stubFile{{outputPath, goFile}},
	})
	var (
		seen          string
		observed      bool
		agentHadRun   bool
		startedMarker = filepath.Join(root, "agent-started")
	)
	g.OnPrompt = func(p string) {
		observed, seen = true, p
		_, err := os.Stat(startedMarker)
		agentHadRun = err == nil
	}

	if _, err := g.Generate(context.Background(), request(outputPath)); err != nil {
		t.Fatalf("generation failed: %v", err)
	}

	if !observed {
		t.Fatal("the registered observer never saw a prompt")
	}
	if !strings.Contains(seen, behaviorBody) || !strings.Contains(seen, outputPath) {
		t.Errorf("the observer saw something other than the assembled prompt:\n%s", seen)
	}
	if _, err := os.Stat(startedMarker); err != nil {
		t.Fatalf("the stand-in agent never ran, so the ordering proves nothing: %v", err)
	}
	if agentHadRun {
		t.Error("the prompt was reported only after the harness had started")
	}
}

func TestAHarnessThatFailsToRunIsReportedAndNoFileIsWritten(t *testing.T) {
	root := newGeneratorProject(t, nil)

	// The agent prints usable code and then dies; the run failure is the
	// answer, not the code it managed to print on the way out.
	err := generateErr(t, root, stubAgent{stdout: fenced(goFile), exit: 3}, request(outputPath))

	if !strings.Contains(err.Error(), "exited with status 3") {
		t.Errorf("error = %v, want the harness's own failure", err)
	}
	assertNoFile(t, root, outputPath)
}

func TestAnUnreadableFileAtTheOutputPathStopsGenerationWithThatReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with no permission bits set")
	}
	root := newGeneratorProject(t, map[string]string{outputPath: goFile})
	p := filepath.Join(root, filepath.FromSlash(outputPath))
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	err := generateErr(t, root, stubAgent{touches: "agent-started", stdout: fenced(goFile)}, request(outputPath))

	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error = %v, want the underlying read failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "agent-started")); statErr == nil {
		t.Error("the harness was run even though the existing file could not be read")
	}
}

// --- Deciding whether generation succeeded ---------------------------------

func TestAFileLeftBehindByTheHarnessIsReportedAsWrittenWithItsSize(t *testing.T) {
	root := newGeneratorProject(t, nil)

	out := mustGenerate(t, root, stubAgent{writes: []stubFile{{outputPath, goFile}}}, request(outputPath))

	if !out.WroteFile {
		t.Error("WroteFile = false, want the harness credited with writing the file")
	}
	if out.FromStdout {
		t.Error("FromStdout = true, want the file taken from disk, not stdout")
	}
	if want := len(readOutput(t, root, outputPath)); out.Bytes != want {
		t.Errorf("Bytes = %d, want %d, the size of the file on disk", out.Bytes, want)
	}
}

func TestAByteIdenticalFileIsReportedAsUnchangedAndCountsAsSuccess(t *testing.T) {
	root := newGeneratorProject(t, map[string]string{outputPath: goFile})

	// The agent read the specification, judged the existing tests to already
	// satisfy it, and touched nothing.
	out := mustGenerate(t, root, stubAgent{stdout: "The existing tests already cover this."}, request(outputPath))

	if !out.Unchanged {
		t.Error("Unchanged = false, want the identical file reported as unchanged")
	}
	if !out.WroteFile {
		t.Error("WroteFile = false, want the file that is in place reported as produced")
	}
	if got := readOutput(t, root, outputPath); got != goFile {
		t.Errorf("the existing tests were disturbed: %q", got)
	}
}

func TestAFileTheHarnessChangedIsNotReportedAsUnchanged(t *testing.T) {
	root := newGeneratorProject(t, map[string]string{outputPath: "package tests\n\nfunc TestOldName(t *testing.T) {}\n"})

	out := mustGenerate(t, root, stubAgent{writes: []stubFile{{outputPath, goFile}}}, request(outputPath))

	if out.Unchanged {
		t.Errorf("Unchanged = true for a rewritten file, now %q", readOutput(t, root, outputPath))
	}
}

func TestAnEmptyFileAfterTheRunIsTreatedAsNoFileAtAll(t *testing.T) {
	root := newGeneratorProject(t, nil)

	out := mustGenerate(t, root, stubAgent{empties: []string{outputPath}, stdout: fenced(goFile)}, request(outputPath))

	if out.WroteFile || !out.FromStdout {
		t.Errorf("outcome = %+v, want the empty file ignored and stdout used", out)
	}
	if got := readOutput(t, root, outputPath); !strings.Contains(got, "func TestDiscountReducesTheTotal") {
		t.Errorf("%s = %q, want the body recovered from stdout", outputPath, got)
	}
}

func TestTheHarnessReplyIsKeptWithTheOutcomeWhenTheHarnessWroteTheFile(t *testing.T) {
	root := newGeneratorProject(t, nil)

	out := mustGenerate(t, root, stubAgent{
		stdout: "\n\n  Wrote tests/checkout_test.go\n\n",
		writes: []stubFile{{outputPath, goFile}},
	}, request(outputPath))

	if out.HarnessOutput != "Wrote tests/checkout_test.go" {
		t.Errorf("HarnessOutput = %q, want the reply with its surrounding whitespace trimmed", out.HarnessOutput)
	}
}

func TestTheHarnessReplyIsKeptWithTheOutcomeWhenTheBodyCameFromStdout(t *testing.T) {
	reply := "\nI could not write the file, so here it is:\n\n" + fenced(goFile) + "\n"
	root := newGeneratorProject(t, nil)

	out := mustGenerate(t, root, stubAgent{stdout: reply}, request(outputPath))

	if want := strings.TrimSpace(reply); out.HarnessOutput != want {
		t.Errorf("HarnessOutput = %q, want %q", out.HarnessOutput, want)
	}
}

// --- Falling back to the harness's printed output --------------------------

func TestTheFileBodyIsRecoveredFromStdoutWhenTheHarnessWritesNothing(t *testing.T) {
	root := newGeneratorProject(t, nil)

	out := mustGenerate(t, root, stubAgent{stdout: fenced(goFile)}, request(outputPath))

	if got := readOutput(t, root, outputPath); !strings.Contains(got, "func TestDiscountReducesTheTotal") {
		t.Errorf("%s = %q, want the printed file body", outputPath, got)
	}
	if !out.FromStdout {
		t.Error("FromStdout = false, want the body credited to stdout")
	}
	if out.WroteFile {
		t.Error("WroteFile = true, want it clear the harness did not write the file")
	}
}

func TestKatanaCreatesTheParentDirectoriesOfTheOutputPathItself(t *testing.T) {
	root := newGeneratorProject(t, nil)
	const deep = "tests/api/v2/checkout_test.go"

	mustGenerate(t, root, stubAgent{stdout: fenced(goFile)}, request(deep))

	if got := readOutput(t, root, deep); !strings.Contains(got, "func TestDiscountReducesTheTotal") {
		t.Errorf("%s = %q, want the file written under directories katana created", deep, got)
	}
}

func TestTheLargestFencedBlockIsPreferredAsTheFileBody(t *testing.T) {
	stdout := "Here is a helper I considered:\n\n" +
		"```go\n// nope\n```\n\n" +
		"and the file itself:\n\n" + fenced(goFile) + "\n"

	body := acceptedBody(t, stdout)

	if !strings.Contains(body, "func TestDiscountReducesTheTotal") {
		t.Errorf("the smaller block won: %q", body)
	}
	if strings.Contains(body, "```") {
		t.Errorf("fences leaked into the file: %q", body)
	}
	if strings.Contains(body, "Here is a helper") {
		t.Errorf("prose leaked into the file: %q", body)
	}
}

func TestARecoveredFencedBodyIsTrimmedAndEndsWithASingleNewline(t *testing.T) {
	stdout := "here you go:\n\n```go\n\n\n" + goFile + "\n\n\n```\n\nlet me know.\n"

	body := acceptedBody(t, stdout)

	if want := strings.TrimSpace(goFile) + "\n"; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestOutputWithNoFenceIsUsedWholeWhenItLooksLikeSourceCode(t *testing.T) {
	body := acceptedBody(t, "\n\n"+goFile+"\n\n")

	if want := strings.TrimSpace(goFile) + "\n"; body != want {
		t.Errorf("body = %q, want the whole output trimmed with one trailing newline (%q)", body, want)
	}
}

func TestOutputThatIsEntirelyWhitespaceIsNeverUsedAsAFileBody(t *testing.T) {
	for _, stdout := range []string{"   ", "\n\n\n", " \n\t\n "} {
		t.Run(fmt.Sprintf("%q", stdout), func(t *testing.T) {
			assertNotAFileBody(t, stdout)
		})
	}
}

// --- Telling code apart from an agent's reply ------------------------------

func TestOutputOfFewerThanThreeLinesIsNeverAcceptedAsAFileBody(t *testing.T) {
	// Both lines are as code-like as a line gets; the length alone rejects it,
	// because a one-line confirmation is not a file.
	assertNotAFileBody(t, "package tests\nfunc TestDiscountReducesTheTotal(t *testing.T) {}")
}

func TestStructureAndIndentationMakeALineCodeLike(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"ending in an opening brace", "if a {\nif b {\nif c {"},
		{"ending in a closing brace", "a }\nb }\nc }"},
		{"ending in a semicolon", "a = 1;\nb = 2;\nc = 3;"},
		{"ending in a colon", "alpha:\nbeta:\ngamma:"},
		{"exactly a closing brace", "}\n}\n}"},
		{"exactly a closing paren", ")\n)\n)"},
		{"indented with a tab", "\talpha\n\tbeta\n\tgamma"},
		{"indented with four spaces", "    alpha\n    beta\n    gamma"},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := acceptedBody(t, c.body)
			if strings.TrimSpace(body) != strings.TrimSpace(c.body) {
				t.Errorf("body = %q, want the output itself (%q)", body, c.body)
			}
		})
	}
}

func TestARecognisedSourceMarkerMakesALineCodeLike(t *testing.T) {
	markers := []string{
		"package ", "import ", "from ", "def ", "func ", "class ", "fn ",
		"#include", "using ", "namespace ", "module ", "require ", "@Test",
		"describe(", "it(", "test(", "const ", "let ", "var ", "public ",
		"private ", "internal ", "assert", "expect(", "self.", "#[",
	}
	for _, m := range markers {
		t.Run(fmt.Sprintf("%q", m), func(t *testing.T) {
			// Three lines carrying no signal but the marker itself, so nothing
			// else in the rule can be what accepted them.
			body := fmt.Sprintf("%salpha\n%sbeta\n%sgamma", m, m, m)
			if got := acceptedBody(t, body); strings.TrimSpace(got) != body {
				t.Errorf("body = %q, want %q", got, body)
			}
		})
	}
}

func TestBlankLinesAreIgnoredWhenJudgingWhetherOutputIsCode(t *testing.T) {
	// Four code-like lines spread across twelve blank ones: if the blanks
	// counted towards the total, four out of sixteen would fall under the
	// one-third bar and this would be rejected.
	code := []string{"func TestAlpha(t *testing.T) {", "}", "func TestBeta(t *testing.T) {", "}"}
	body := strings.Join(code, "\n\n\n\n\n")

	if got := acceptedBody(t, body); !strings.Contains(got, "func TestBeta") {
		t.Errorf("body = %q, want the whole output kept", got)
	}
}

func TestUnfencedOutputNeedsAtLeastThreeCodeLikeLines(t *testing.T) {
	// Two code-like lines out of five non-blank ones: comfortably past the
	// one-third bar, so only the count of three can reject it.
	assertNotAFileBody(t, strings.Join([]string{
		"Here is what I changed for you.",
		"func TestDiscountReducesTheTotal(t *testing.T) {",
		"It covers the happy path only.",
		"}",
		"Tell me if you want the edge cases too.",
	}, "\n"))
}

func TestUnfencedOutputNeedsCodeLikeLinesToBeAThirdOfTheOutput(t *testing.T) {
	// Four code-like lines, well past the count of three, drowned in prose.
	var printed []string
	for i := 0; i < 20; i++ {
		printed = append(printed, fmt.Sprintf("Sentence number %d about what I decided to do", i))
	}
	printed = append(printed, "func TestAlpha(t *testing.T) {", "}", "func TestBeta(t *testing.T) {", "}")

	assertNotAFileBody(t, strings.Join(printed, "\n"))
}

func TestAParagraphOfProseWithACodeLikeLineFailsInsteadOfBeingWritten(t *testing.T) {
	assertNotAFileBody(t, strings.Join([]string{
		"I read the specification and wrote the tests you asked for.",
		"They live in tests/checkout_test.go:",
		"The suite covers discounts, expiry and the error path.",
		"Nothing else in the repository was touched.",
	}, "\n"))
}

// --- Reporting a failed generation -----------------------------------------

func TestAGenerationThatProducedNothingNamesTheHarnessAndTheOutputPath(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{stdout: "Done, nothing to do."}, request(outputPath))

	want := fmt.Sprintf("harness %q did not write %s and printed no test code; harness said: %s",
		stubName, outputPath, "Done, nothing to do.")
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestTheFailureSummaryIsTheHarnessStandardOutput(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{
		stdout: "I decided the tests were unnecessary.",
		stderr: "some diagnostic noise",
	}, request(outputPath))

	if !strings.Contains(err.Error(), "harness said: I decided the tests were unnecessary.") {
		t.Errorf("error = %v, want it to quote standard output", err)
	}
	if strings.Contains(err.Error(), "some diagnostic noise") {
		t.Errorf("error = %v, want standard error left out while stdout has content", err)
	}
}

func TestTheFailureSummaryFallsBackToStandardErrorWhenStandardOutputIsEmpty(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{stderr: "the agent could not reach its model"}, request(outputPath))

	if !strings.Contains(err.Error(), "harness said: the agent could not reach its model") {
		t.Errorf("error = %v, want it to quote standard error", err)
	}
}

func TestTheFailureSummaryIsNoOutputWhenTheHarnessSaidNothing(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{}, request(outputPath))

	if !strings.Contains(err.Error(), "harness said: (no output)") {
		t.Errorf("error = %v, want it to report that the harness said nothing", err)
	}
}

func TestTheGenerationFailureSummaryIsFlattenedToASingleLine(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{stdout: "alpha\nbeta\ngamma"}, request(outputPath))

	if !strings.Contains(err.Error(), "harness said: alpha beta gamma") {
		t.Errorf("error = %v, want the reply's newlines replaced with spaces", err)
	}
}

func TestTheFailureSummaryIsTruncatedAtFourHundredCharacters(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{stdout: strings.Repeat("x", 600)}, request(outputPath))

	if !strings.Contains(err.Error(), strings.Repeat("x", 400)+"...") {
		t.Errorf("error = %v, want the reply cut at 400 characters followed by an ellipsis", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 401)) {
		t.Errorf("error = %v, want no more than 400 characters of the reply", err)
	}
}

func TestAGenerationPermissionHintIsAppendedOnItsOwnIndentedLine(t *testing.T) {
	root := newGeneratorProject(t, nil)

	err := generateErr(t, root, stubAgent{stdout: "Write to tests/checkout_test.go: permission denied."}, request(outputPath))

	if !strings.Contains(err.Error(), "\n  hint: ") {
		t.Errorf("error = %q, want the hint on its own indented line", err.Error())
	}
	if !strings.Contains(err.Error(), "denied file-write permission") {
		t.Errorf("error = %v, want the hint to name the likely cause", err)
	}
}

// --- What the harness is asked to do ---------------------------------------

func TestThePromptStatesTheLanguageFrameworkAndOutputPath(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))
	if !strings.Contains(p, "write go tests for it using go-test") {
		t.Errorf("prompt does not state the language and framework:\n%s", p)
	}
	if !strings.Contains(p, outputPath) {
		t.Errorf("prompt does not give the output path:\n%s", p)
	}

	other := request(outputPath)
	other.Language, other.Framework = "python", "pytest"
	if p := generator.BuildPrompt(other); !strings.Contains(p, "write python tests for it using pytest") {
		t.Errorf("prompt does not follow the requested language and framework:\n%s", p)
	}
}

func TestThePromptTellsTheHarnessToWriteTheFileItself(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	for _, want := range []string{
		"Create any parent directories the path needs",
		"Write the file yourself using your file tools",
		"do not print the test code as your reply",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestThePromptPermitsPrintingTheFileOnlyWhenTheWriteIsRefused(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	for _, want := range []string{
		"If, and only if, writing that file fails",
		"no file tool is available",
		"the write is denied by a permission check",
		"do not stop and do not ask for access",
		"single fenced code block",
		"with no prose before or after it",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestThePromptAsksForAnInPlaceUpdateWhenTestsExist(t *testing.T) {
	req := request(outputPath)
	req.ExistingTests = "package tests\n\nfunc TestOldName(t *testing.T) {}\n"

	p := generator.BuildPrompt(req)

	if !strings.Contains(p, "TestOldName") {
		t.Errorf("the existing tests were not included:\n%s", p)
	}
	for _, want := range []string{
		"Existing tests at that path",
		"change the cases the specification changed",
		"add cases it added",
		"remove cases it no longer describes",
		"Preserve unrelated hand-written helpers, fixtures and imports",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestThePromptHasNoExistingTestsSectionWhenThereAreNone(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	if strings.Contains(p, "Existing tests") {
		t.Errorf("a new behavior should not be given an existing-tests section:\n%s", p)
	}
}

func TestTheGenerationPromptListsTakenNamesAndForbidsRedeclaringThem(t *testing.T) {
	req := request(outputPath)
	req.Reserved = []string{"TestTheCartStartsEmpty", "TestTheCartTotalIncludesTax"}

	p := generator.BuildPrompt(req)

	for _, name := range req.Reserved {
		if !strings.Contains(p, "- "+name) {
			t.Errorf("the taken name %q was not listed:\n%s", name, p)
		}
	}
	for _, want := range []string{
		"Do not declare any of them in this file",
		"In languages where a directory is one namespace",
		"a redeclared name stops the whole package compiling",
		"every test in every file beside it silently stops running",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestTheGenerationPromptPreservesOverlappingBehaviorsWithPartSpecificNames(t *testing.T) {
	req := request(outputPath)
	req.Reserved = []string{"TestCheckoutRejectsAnExpiredCode"}

	p := generator.BuildPrompt(req)

	for _, want := range []string{
		"Two specifications often describe the same rule about different parts of the product",
		"Where that happens, still write the test",
		"name it for the part of the product this specification is about, not for the rule alone",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestTheGenerationPromptOmitsTakenNamesWhenNoneAreGiven(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	if strings.Contains(p, "Test names already taken") {
		t.Errorf("prompt has a taken-names section despite receiving no names:\n%s", p)
	}
}

func TestThePromptIncludesTheSpecificationAndThePathItCameFrom(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	if !strings.Contains(p, behaviorBody) {
		t.Errorf("prompt does not include the specification:\n%s", p)
	}
	if !strings.Contains(p, behaviorPath) {
		t.Errorf("prompt does not name the behavior file it came from:\n%s", p)
	}
}

func TestThePromptRequiresOneTestPerBehaviorNamedAfterIt(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	for _, want := range []string{
		"Cover every behavior the specification states, including the error and edge cases it calls out",
		"One test per distinct asserted behavior",
		"Name each test after the behavior it verifies",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestTheGenerationPromptRequiresPackageWideTestNameUniqueness(t *testing.T) {
	// No reserved names are needed for this rule: every generation must still
	// protect against collisions with files already sharing the target package.
	p := generator.BuildPrompt(request(outputPath))

	if want := "Every test name must be unique across the files that share this one's package or directory, not merely within this file"; !strings.Contains(p, want) {
		t.Errorf("prompt is missing %q:\n%s", want, p)
	}
}

func TestThePromptRequiresAssertionsOnBehaviorAndAReasonableReadingOfGaps(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	for _, want := range []string{
		"Assert on the behavior the specification describes, not on incidental implementation detail",
		"pick the reasonable interpretation and note it in a brief comment",
		"rather than inventing a requirement",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestThePromptRequiresReadingTheSurroundingCodeAndTheCodeUnderTest(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	for _, want := range []string{
		"read a neighbouring test file first",
		"If the code under test exists in this repository, read it so the tests call the real API",
		"Do not invent function signatures you have not verified",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestThePromptRequiresACompilingFileAndForbidsOtherEditsAndTestRuns(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	for _, want := range []string{
		"The file must compile and be runnable by the project's normal test command",
		"Do not modify any file other than the target test file",
		"Do not run the test suite",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestExtraProjectInstructionsAreAppendedAsTheirOwnSectionTrimmed(t *testing.T) {
	req := request(outputPath)
	req.ExtraInstructions = "\n\n  Never call the payment gateway.  \n\n"

	p := generator.BuildPrompt(req)

	if want := "## Additional project instructions\n\nNever call the payment gateway.\n"; !strings.Contains(p, want) {
		t.Errorf("prompt does not carry the extra instructions trimmed:\n%s", p)
	}
}

func TestBlankExtraProjectInstructionsGetNoSectionAtAll(t *testing.T) {
	for _, extra := range []string{"", "   ", "\n\t\n"} {
		t.Run(fmt.Sprintf("%q", extra), func(t *testing.T) {
			req := request(outputPath)
			req.ExtraInstructions = extra

			if p := generator.BuildPrompt(req); strings.Contains(p, "Additional project instructions") {
				t.Errorf("prompt has an empty extra-instructions section:\n%s", p)
			}
		})
	}
}

func TestThePromptClosesByAskingForAOneLineConfirmationOfThePath(t *testing.T) {
	p := generator.BuildPrompt(request(outputPath))

	if !strings.Contains(p, "reply with one short line confirming the path") {
		t.Errorf("prompt does not ask for a confirmation:\n%s", p)
	}
	if !strings.Contains(p, "If you could not write it, reply with the fenced file contents") {
		t.Errorf("prompt does not offer the printed-file alternative:\n%s", p)
	}
}

// fenced wraps a file body the way an agent prints one, so tests can hand the
// stand-in agent a reply that carries code.
func fenced(body string) string {
	return "```go\n" + strings.TrimRight(body, "\n") + "\n```"
}
