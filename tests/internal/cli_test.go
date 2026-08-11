// This file covers behaviors/internal/cli.md: katana's command line — the
// entry point that dispatches a subcommand, validates its flags, drives a
// coding-agent harness where one is needed, and reports what happened.
//
// The command line is only observable from outside the package as a process:
// `katana run` propagates the suite's exit code with os.Exit, the release
// notice is written to the real os.Stderr, and several behaviors are about
// which stream a line lands on. So every assertion here drives cli.Run in a
// child process — this test binary re-runs itself (see
// TestKatanaCommandLineHelper) with the arguments, environment and working
// directory the test wants, and the test reads back its stdout, stderr and
// exit code.
//
// The coding agent is a child process too: a harness is an executable, so the
// stand-in agent is this same binary re-run once more (see
// TestKatanaAgentHelper), pointed at by harness.command in the project's
// katana.yaml. It reads the prompt on stdin, finds the output path katana asked
// for in it, and writes, prints, skips or fails as the test told it to.
//
// Helpers here are named for the command line rather than reusing the ones in
// the other files of this package, so this file stands on its own.

package internal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/cli"
	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/tracker"
)

// --- Driving the command line ----------------------------------------------

const (
	cliHelperEnv  = "KATANA_CLI_HELPER"
	cliArgsEnv    = "KATANA_CLI_ARGS"
	cliVersionEnv = "KATANA_CLI_VERSION"

	agentEnv       = "KATANA_CLI_AGENT"
	agentModeEnv   = "KATANA_CLI_AGENT_MODE"
	agentBodyEnv   = "KATANA_CLI_AGENT_BODY"
	agentLogEnv    = "KATANA_CLI_AGENT_LOG"
	agentDelayEnv  = "KATANA_CLI_AGENT_DELAY"
	agentSlowEnv   = "KATANA_CLI_AGENT_SLOW_MATCH"
	agentFailEnv   = "KATANA_CLI_AGENT_FAIL_MATCH"
	agentStderrEnv = "KATANA_CLI_AGENT_STDERR"
)

// cliEnvKeys are the environment variables this file decides for the child, so
// they are stripped from the inherited environment first. Without that, running
// the suite in CI — where CI is set — would silently change what the release
// check does.
var cliEnvKeys = []string{
	cliHelperEnv, cliArgsEnv, cliVersionEnv,
	agentEnv, agentModeEnv, agentBodyEnv, agentLogEnv, agentDelayEnv,
	agentSlowEnv, agentFailEnv, agentStderrEnv,
	"CI", "KATANA_NO_UPDATE_CHECK", "KATANA_CACHE_DIR", "KATANA_GITHUB_API",
	"KATANA_GITHUB_TOKEN", "GITHUB_TOKEN", "GH_TOKEN",
	"NO_COLOR", "CLICOLOR_FORCE",
}

// cliOptions are the parts of an invocation a test varies.
type cliOptions struct {
	dir     string            // working directory; the process's own when empty
	version string            // cli.Version in the child; "dev" when empty
	env     map[string]string // added to the child environment, overriding the defaults
	exe     string            // binary to run; this test binary when empty
	onStart func(*exec.Cmd)   // called once the child is running, on its own goroutine
	// merged gives the child one file descriptor for both streams, the way a
	// terminal does. It is the only way to observe the order writes to the two
	// streams really happened in: with a pipe each, the order they arrive in is
	// the order this process happens to read them. It leaves stdout and stderr
	// empty, so a test that needs them apart runs twice.
	merged bool
}

// cliOutcome is everything a finished invocation said.
type cliOutcome struct {
	stdout string
	stderr string
	all    string // both streams, in the order they were written
	code   int
}

// out is stdout and stderr together, for an assertion that does not care which
// stream carried the line.
func (o cliOutcome) out() string { return o.stdout + o.stderr }

// runKatana invokes katana's command line in a child process and returns what
// it printed and the exit code a shell would have seen.
func runKatana(t *testing.T, opts cliOptions, args ...string) cliOutcome {
	t.Helper()

	cmd, finish := startKatana(t, opts, args...)
	if opts.onStart != nil {
		go opts.onStart(cmd)
	}
	return finish()
}

// startKatana starts the command line without waiting for it, for the tests
// that have to signal it while it is working.
func startKatana(t *testing.T, opts cliOptions, args ...string) (*exec.Cmd, func() cliOutcome) {
	t.Helper()

	exe := opts.exe
	if exe == "" {
		exe = cliBinary(t)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(exe, "-test.run=^TestKatanaCommandLineHelper$")
	cmd.Dir = opts.dir
	cmd.Env = childEnv(map[string]string{
		cliHelperEnv:  "1",
		cliArgsEnv:    string(encoded),
		cliVersionEnv: opts.version,
		// The daily release check is background noise unless a test is about
		// it, and a cache directory of its own keeps it off the real one.
		"KATANA_NO_UPDATE_CHECK": "1",
		"KATANA_CACHE_DIR":       t.TempDir(),
		"NO_COLOR":               "1",
	}, opts.env)

	var mu sync.Mutex
	var stdout, stderr, all bytes.Buffer

	// One pipe shared by both streams keeps the kernel's ordering; a pipe each
	// records the streams apart but not their order relative to one another.
	var drained chan struct{}
	if opts.merged {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("creating the output pipe: %v", err)
		}
		cmd.Stdout, cmd.Stderr = w, w
		drained = make(chan struct{})
		go func() {
			defer close(drained)
			defer r.Close()
			io.Copy(&all, r)
		}()
		// The child holds the only other reference once it has started, so the
		// reader above sees EOF when it exits.
		defer w.Close()
	} else {
		cmd.Stdout = cliTee{mu: &mu, self: &stdout, all: &all}
		cmd.Stderr = cliTee{mu: &mu, self: &stderr, all: &all}
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting katana: %v", err)
	}
	return cmd, func() cliOutcome {
		t.Helper()
		code := 0
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("running katana: %v", err)
			}
			code = exitErr.ExitCode()
		}
		if drained != nil {
			<-drained
		}
		mu.Lock()
		defer mu.Unlock()
		return cliOutcome{stdout: stdout.String(), stderr: stderr.String(), all: all.String(), code: code}
	}
}

// cliTee records one stream on its own and into the shared transcript, under a
// lock the two streams share so the merged order is the real one.
type cliTee struct {
	mu   *sync.Mutex
	self *bytes.Buffer
	all  *bytes.Buffer
}

func (c cliTee) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.all.Write(p)
	return c.self.Write(p)
}

// childEnv builds the child environment: the inherited one with everything this
// file controls removed, then the defaults, then the test's own overrides.
func childEnv(defaults, overrides map[string]string) []string {
	drop := make(map[string]bool, len(cliEnvKeys))
	for _, k := range cliEnvKeys {
		drop[k] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range defaults {
		if _, overridden := overrides[k]; !overridden {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

func cliBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot find the test binary to run as katana: %v", err)
	}
	return exe
}

// TestKatanaCommandLineHelper is not a test of its own: it is katana itself.
// The command line is a process, so the only way to observe its exit code and
// its streams is to be one — this test binary re-runs itself with the arguments
// the test chose and reproduces main.go's own error handling.
func TestKatanaCommandLineHelper(t *testing.T) {
	if os.Getenv(cliHelperEnv) != "1" {
		t.Skip("katana itself, run only as a child process of a command-line test")
	}
	if v := os.Getenv(cliVersionEnv); v != "" {
		cli.Version = v
	}
	var args []string
	if raw := os.Getenv(cliArgsEnv); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			fmt.Fprintf(os.Stderr, "bad %s: %v\n", cliArgsEnv, err)
			os.Exit(99)
		}
	}
	if err := cli.Run(args); err != nil {
		// flag.ErrHelp means the user asked for usage; the flag package has
		// already printed it. This is main.go's behaviour, verbatim.
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		}
		os.Exit(1)
	}
	os.Exit(0) // exit before the testing package prints its own report
}

// --- The stand-in coding agent ---------------------------------------------

// agentSpec is what the fake harness does for every unit of work in a run.
type agentSpec struct {
	// mode is one of "write" (the default), "stdout", "skip", "unchanged",
	// "quiet" and "fail".
	mode string
	// body is what it writes or prints; a default for the file type when empty.
	body string
	// log is a file it appends "start" and "end" lines to, which is how a test
	// sees whether the harness ran at all and whether two ran at once.
	log string
	// delay is how long every invocation takes.
	delay time.Duration
	// slowMatch delays only the invocations whose output path contains it, so a
	// test can decide which unit finishes last.
	slowMatch string
	// failMatch fails only the invocations whose output path contains it.
	failMatch string
	// stderr is written to the agent's own standard error.
	stderr string
}

func (a agentSpec) env(t *testing.T) map[string]string {
	t.Helper()
	env := map[string]string{
		agentEnv:     "1",
		agentModeEnv: a.mode,
		agentBodyEnv: a.body,
		agentLogEnv:  a.log,
	}
	if a.delay > 0 {
		env[agentDelayEnv] = strconv.FormatInt(a.delay.Milliseconds(), 10)
	}
	if a.slowMatch != "" {
		env[agentSlowEnv] = a.slowMatch
	}
	if a.failMatch != "" {
		env[agentFailEnv] = a.failMatch
	}
	if a.stderr != "" {
		env[agentStderrEnv] = a.stderr
	}
	return env
}

// agentTests is the test file the stand-in agent writes, with two cases in it
// so "how many test cases came out of this" has a number worth checking.
const agentTests = "package tests\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\n\nfunc TestTwo(t *testing.T) {}\n"

// agentSpecFile is the behavior file it writes: two lines beginning with "#"
// and two bullets, which is two sections and two statements.
const agentSpecFile = "# What billing does\n\n## Charging a card\n\n- A charge below the minimum is rejected.\n- A declined card leaves the order unpaid.\n"

// TestKatanaAgentHelper is not a test of its own: it is the coding agent every
// generation and discovery in this file runs. It reads the prompt on stdin,
// takes the output path out of it exactly as a real agent would, and then does
// whatever the test asked of it.
func TestKatanaAgentHelper(t *testing.T) {
	if os.Getenv(agentEnv) != "1" {
		t.Skip("the fake coding agent, run only as a child process of a katana run")
	}
	prompt, _ := io.ReadAll(os.Stdin)
	target := agentTarget(string(prompt))

	logLine := func(s string) {
		path := os.Getenv(agentLogEnv)
		if path == "" {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintln(f, s)
	}

	logLine("start")
	if ms, err := strconv.Atoi(os.Getenv(agentDelayEnv)); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	if m := os.Getenv(agentSlowEnv); m != "" && strings.Contains(target, m) {
		time.Sleep(600 * time.Millisecond)
	}

	mode := os.Getenv(agentModeEnv)
	if m := os.Getenv(agentFailEnv); m != "" && strings.Contains(target, m) {
		mode = "fail"
	}
	body := os.Getenv(agentBodyEnv)
	if body == "" {
		body = agentTests
		if strings.HasSuffix(target, ".md") {
			body = agentSpecFile
		}
	}
	if s := os.Getenv(agentStderrEnv); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}

	switch mode {
	case "", "write":
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(90)
		}
		if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		fmt.Println("wrote " + filepath.ToSlash(target))
		fmt.Println("let me know if you want anything changed")
	case "stdout":
		fmt.Println("I could not write the file, so here it is:")
		fmt.Println("```")
		fmt.Print(body)
		fmt.Println("```")
	case "skip":
		fmt.Println("SKIP: no product behavior worth specifying")
	case "unchanged":
		// Whatever is already at the path is left exactly as it stands.
		fmt.Println("the file already says what the code does")
	case "quiet":
		fmt.Println("all done")
	case "fail":
		fmt.Fprintln(os.Stderr, "the agent could not reach its model")
		logLine("end")
		os.Exit(3)
	}
	logLine("end")
	os.Exit(0)
}

// agentTarget reads the output path out of a katana prompt. Both the generation
// and the discovery prompt name it the same way: on an indented line of its own
// after the sentence that introduces it.
func agentTarget(prompt string) string {
	const marker = "relative to the current working directory:"
	i := strings.Index(prompt, marker)
	if i < 0 {
		return ""
	}
	for _, line := range strings.Split(prompt[i+len(marker):], "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return filepath.FromSlash(s)
		}
	}
	return ""
}

// --- Project fixtures ------------------------------------------------------

// projectSpec describes a katana project to lay out in a temp directory.
type projectSpec struct {
	agent agentSpec
	// behaviors are behavior markdown base names; "cart" alone when empty.
	behaviors []string
	// specs overrides the markdown of a named behavior.
	specs map[string]string
	// defaults replaces the body of the defaults: block.
	defaults string
	// paths replaces the behaviors: path entries.
	paths []string
	// jobs sets harness.jobs; left unset when zero.
	jobs int
	// testCmd and testDir configure `katana run`.
	testCmd string
	testDir string
	// files are extra project files, by project-relative slash path.
	files map[string]string
	// noHarnessCommand points the harness at an executable that is not there,
	// for the tests about a harness katana cannot run.
	noHarnessCommand bool
}

// cliProject lays a project out in a temp directory and returns its root.
func cliProject(t *testing.T, spec projectSpec) string {
	t.Helper()

	root := t.TempDir()
	// Resolved, so the paths katana prints and the paths the agent writes to
	// are the same string on macOS (/var against /private/var).
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}

	names := spec.behaviors
	if names == nil {
		names = []string{"cart"}
	}
	for _, n := range names {
		body := spec.specs[n]
		if body == "" {
			body = "# " + n + "\n\n## Its rules\n\n- It does the thing.\n"
		}
		cliWrite(t, root, "behaviors/"+n+".md", body)
	}
	for name, body := range spec.files {
		cliWrite(t, root, name, body)
	}
	cliWrite(t, root, config.FileName, katanaYAML(t, spec))
	return root
}

// katanaYAML renders the project configuration: a harness that is this test
// binary standing in for a coding agent, plus whatever the test varied.
func katanaYAML(t *testing.T, spec projectSpec) string {
	t.Helper()

	command := cliBinary(t)
	if spec.noHarnessCommand {
		command = "katana-no-such-agent"
	}

	var b strings.Builder
	b.WriteString("version: 1\n")
	b.WriteString("harness:\n  name: fake\n")
	fmt.Fprintf(&b, "  command: %s\n", strconv.Quote(command))
	b.WriteString("  args: [\"-test.run=^TestKatanaAgentHelper$\"]\n")
	b.WriteString("  prompt: stdin\n")
	if spec.jobs > 0 {
		fmt.Fprintf(&b, "  jobs: %d\n", spec.jobs)
	}
	b.WriteString("  env:\n")
	env := spec.agent.env(t)
	for _, k := range sortedKeys(env) {
		fmt.Fprintf(&b, "    %s: %s\n", k, strconv.Quote(env[k]))
	}

	b.WriteString("defaults:\n")
	if spec.defaults != "" {
		b.WriteString(spec.defaults)
	} else {
		b.WriteString("  language: go\n  output_dir: tests\n")
	}

	if spec.testCmd != "" {
		b.WriteString("test:\n")
		fmt.Fprintf(&b, "  command: %s\n", strconv.Quote(spec.testCmd))
		if spec.testDir != "" {
			fmt.Fprintf(&b, "  dir: %s\n", strconv.Quote(spec.testDir))
		}
	}

	paths := spec.paths
	if paths == nil {
		paths = []string{"behaviors"}
	}
	b.WriteString("behaviors:\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "  - path: %s\n", strconv.Quote(p))
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func cliWrite(t *testing.T, root, name, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func cliRead(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

func cliExists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	return err == nil
}

// --- Assertions ------------------------------------------------------------

func wantContains(t *testing.T, got, want, what string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%s is missing %q:\n%s", what, want, got)
	}
}

func wantAbsent(t *testing.T, got, unwanted, what string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Errorf("%s should not contain %q:\n%s", what, unwanted, got)
	}
}

func wantExit(t *testing.T, res cliOutcome, code int) {
	t.Helper()
	if res.code != code {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", res.code, code, res.stdout, res.stderr)
	}
}

func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this behavior is observed through a POSIX shell command")
	}
}

// --- Dispatching a command -------------------------------------------------

func TestInvokingKatanaWithNoArgumentsPrintsUsageAndSucceeds(t *testing.T) {
	res := runKatana(t, cliOptions{})

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "katana generates test code from written product behavior.", "usage")
	wantAbsent(t, res.stderr, "Usage:", "standard error")
}

func TestTheRecognisedCommandsAreDispatchedRatherThanRejected(t *testing.T) {
	for _, name := range []string{"init", "discover", "generate", "run", "status", "harnesses", "update", "version"} {
		t.Run(name, func(t *testing.T) {
			// --help settles the dispatch without doing any of the work: every
			// command either prints its own usage or, for version, its answer.
			res := runKatana(t, cliOptions{}, name, "--help")
			wantAbsent(t, res.out(), "unknown command", name)
		})
	}
}

func TestGenIsAnAliasForGenerate(t *testing.T) {
	res := runKatana(t, cliOptions{}, "gen", "--help")
	wantContains(t, res.stderr, "Usage: katana generate", "gen usage")
}

func TestTestIsAnAliasForRun(t *testing.T) {
	res := runKatana(t, cliOptions{}, "test", "--help")
	wantContains(t, res.stderr, "Usage: katana run", "test usage")
}

func TestUpgradeAndSelfUpdateAreAliasesForUpdate(t *testing.T) {
	for _, name := range []string{"upgrade", "self-update"} {
		t.Run(name, func(t *testing.T) {
			res := runKatana(t, cliOptions{}, name, "--help")
			wantContains(t, res.stderr, "Usage: katana update", name+" usage")
		})
	}
}

func TestVersionFlagsAreAliasesForTheVersionCommand(t *testing.T) {
	for _, name := range []string{"--version", "-v"} {
		t.Run(name, func(t *testing.T) {
			res := runKatana(t, cliOptions{version: "v1.4.2"}, name)
			wantExit(t, res, 0)
			if got := strings.TrimSpace(res.stdout); got != "katana v1.4.2" {
				t.Errorf("%s printed %q, want %q", name, got, "katana v1.4.2")
			}
		})
	}
}

func TestHelpPrintsUsageToStandardOutputAndSucceeds(t *testing.T) {
	for _, name := range []string{"help", "--help", "-h"} {
		t.Run(name, func(t *testing.T) {
			res := runKatana(t, cliOptions{}, name)
			wantExit(t, res, 0)
			wantContains(t, res.stdout, "katana generates test code from written product behavior.", "usage")
			wantAbsent(t, res.stderr, "Usage:", "standard error")
		})
	}
}

func TestAnUnrecognisedCommandPrintsUsageToStandardErrorAndFails(t *testing.T) {
	res := runKatana(t, cliOptions{}, "frobnicate")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "katana generates test code from written product behavior.", "usage on standard error")
	wantContains(t, res.stderr, `unknown command "frobnicate"`, "error")
	wantAbsent(t, res.stdout, "Usage:", "standard output")
}

func TestVersionPrintsTheVersionString(t *testing.T) {
	res := runKatana(t, cliOptions{version: "v2.7.0"}, "version")

	wantExit(t, res, 0)
	if got := strings.TrimSpace(res.stdout); got != "katana v2.7.0" {
		t.Errorf("version printed %q, want %q", got, "katana v2.7.0")
	}
}

func TestAnUnstampedBinaryReportsItsVersionAsDev(t *testing.T) {
	// No -X at build time leaves cli.Version at its declared default.
	res := runKatana(t, cliOptions{}, "version")

	wantExit(t, res, 0)
	if got := strings.TrimSpace(res.stdout); got != "katana dev" {
		t.Errorf("version printed %q, want %q", got, "katana dev")
	}
}

func TestTheUsageListsTheBuiltInHarnessesAndTheTypicalFlows(t *testing.T) {
	res := runKatana(t, cliOptions{}, "help")

	for _, name := range harness.Names() {
		wantContains(t, res.stdout, name, "harness list")
	}
	for _, line := range []string{
		"katana init --language go --harness claude",
		"katana generate",
		"katana run",
		"katana discover --dry-run",
		"katana discover",
	} {
		wantContains(t, res.stdout, line, "typical use")
	}
}

// primeReleaseCache writes the cached result of a release check, which is what
// lets the notice be observed without reaching GitHub.
func primeReleaseCache(t *testing.T, tag string) string {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(struct {
		CheckedAt time.Time `json:"checked_at"`
		LatestTag string    `json:"latest_tag"`
	}{CheckedAt: time.Now(), LatestTag: tag})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTheReleaseNoticeIsPrintedToStandardErrorAfterTheCommandHasFinished(t *testing.T) {
	const notice = "katana v9.9.9 is available (you have v1.0.0)"
	opts := func() cliOptions {
		return cliOptions{
			version: "v1.0.0",
			env: map[string]string{
				"KATANA_NO_UPDATE_CHECK": "",
				"KATANA_CACHE_DIR":       primeReleaseCache(t, "v9.9.9"),
			},
		}
	}

	res := runKatana(t, opts(), "version")

	wantExit(t, res, 0)
	wantContains(t, res.stderr, notice, "release notice")
	wantAbsent(t, res.stdout, notice, "standard output")

	// Which stream carried which line is settled above. Ordering between the
	// two is only observable over one descriptor, as a terminal gives katana.
	merged := opts()
	merged.merged = true
	res = runKatana(t, merged, "version")

	wantExit(t, res, 0)
	work := strings.Index(res.all, "katana v1.0.0")
	news := strings.Index(res.all, notice)
	if work < 0 || news < 0 || news < work {
		t.Errorf("the notice must follow the command's own output, got:\n%s", res.all)
	}
}

func TestUpdateAndHelpStartNoReleaseCheck(t *testing.T) {
	cache := primeReleaseCache(t, "v9.9.9")
	for _, name := range []string{"update", "upgrade", "self-update", "help", "--help", "-h"} {
		t.Run(name, func(t *testing.T) {
			args := []string{name}
			if name == "update" || name == "upgrade" || name == "self-update" {
				// Stop at flag parsing, so no release is fetched either.
				args = append(args, "--help")
			}
			res := runKatana(t, cliOptions{
				version: "v1.0.0",
				env: map[string]string{
					"KATANA_NO_UPDATE_CHECK": "",
					"KATANA_CACHE_DIR":       cache,
				},
			}, args...)
			wantAbsent(t, res.out(), "is available (you have", name)
		})
	}
}

// --- Setting up a project --------------------------------------------------

func TestInitDefaultsToTheCurrentDirectory(t *testing.T) {
	root := t.TempDir()

	res := runKatana(t, cliOptions{dir: root}, "init")

	wantExit(t, res, 0)
	if !cliExists(root, config.FileName) {
		t.Errorf("init wrote no %s into the working directory", config.FileName)
	}
}

func TestInitConfiguresGoWithTheClaudeHarnessByDefault(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	cfg := cliRead(t, root, config.FileName)
	wantContains(t, cfg, "name: claude", "configuration")
	wantContains(t, cfg, "language: go", "configuration")
}

func TestInitDefaultsToABehaviorsAndATestsDirectory(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	if !cliExists(root, "behaviors") {
		t.Error("init created no behaviors directory")
	}
	wantContains(t, cliRead(t, root, config.FileName), "output_dir: tests", "configuration")
	wantContains(t, cliRead(t, root, config.FileName), "- path: behaviors", "configuration")
}

func TestEachInitDefaultIsOverridableByFlag(t *testing.T) {
	root := t.TempDir()

	res := runKatana(t, cliOptions{}, "init",
		"--dir", root, "--language", "python", "--harness", "codex",
		"--behaviors", "specs", "--output", "spec_tests")

	wantExit(t, res, 0)
	cfg := cliRead(t, root, config.FileName)
	wantContains(t, cfg, "name: codex", "configuration")
	wantContains(t, cfg, "language: python", "configuration")
	wantContains(t, cfg, "output_dir: spec_tests", "configuration")
	wantContains(t, cfg, "- path: specs", "configuration")
	if !cliExists(root, "specs") {
		t.Error("init created no specs directory")
	}
}

func TestInitRejectsAHarnessWithNoBuiltInEntry(t *testing.T) {
	root := t.TempDir()

	res := runKatana(t, cliOptions{}, "init", "--dir", root, "--harness", "borges")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, `unknown harness "borges"; choose one of `+strings.Join(harness.Names(), ", "), "error")
	wantContains(t, res.stderr, "or set harness.command in "+config.FileName+" to use another agent CLI", "error")
	if cliExists(root, config.FileName) {
		t.Error("nothing should have been written for an unknown harness")
	}
	if cliExists(root, config.Dir) {
		t.Error("nothing should have been written for an unknown harness")
	}
}

func TestInitRefusesToOverwriteAnExistingConfiguration(t *testing.T) {
	root := t.TempDir()
	cliWrite(t, root, config.FileName, "version: 1\n# mine\n")

	res := runKatana(t, cliOptions{}, "init", "--dir", root)

	wantExit(t, res, 1)
	wantContains(t, res.stderr, filepath.Join(root, config.FileName)+" already exists (pass --force to overwrite)", "error")
	if got := cliRead(t, root, config.FileName); got != "version: 1\n# mine\n" {
		t.Errorf("the existing configuration was not left in place, got:\n%s", got)
	}
}

func TestForceOverwritesAnExistingConfiguration(t *testing.T) {
	root := t.TempDir()
	cliWrite(t, root, config.FileName, "version: 1\n# mine\n")

	res := runKatana(t, cliOptions{}, "init", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, cliRead(t, root, config.FileName), "name: claude", "configuration")
}

func TestTheWrittenConfigurationRecordsTheDefaultsOfTheChosenLanguage(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root, "--language", "python", "--harness", "codex"), 0)

	cfg := cliRead(t, root, config.FileName)
	for _, want := range []string{
		"name: codex",
		"language: python",
		"framework: " + config.DefaultFramework("python"),
		"output_dir: tests",
		`output_template: "` + config.DefaultOutputTemplate("python") + `"`,
		"command: " + config.DefaultTestCommand("python"),
		"- path: behaviors",
	} {
		wantContains(t, cfg, want, "configuration")
	}
}

func TestTheWrittenConfigurationCommentsOutEveryOptionalHarnessField(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	cfg := cliRead(t, root, config.FileName)
	for _, field := range []string{"command", "args", "prompt", "model", "model_flag", "timeout", "jobs", "env"} {
		wantContains(t, cfg, "# "+field+":", "commented harness fields")
	}
}

func TestTheWrittenConfigurationCommentsOutThePerBehaviorOverrides(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	cfg := cliRead(t, root, config.FileName)
	for _, field := range []string{"output", "language", "framework", "harness", "instructions"} {
		wantContains(t, cfg, "#   "+field+":", "commented per-behavior overrides")
	}
}

func TestInitCreatesTheTrackerDirectoryAndAnEmptyTrackerFile(t *testing.T) {
	root := t.TempDir()

	res := runKatana(t, cliOptions{}, "init", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "created "+config.Dir+"/tracker.json", "report")
	if _, err := os.Stat(tracker.Path(root)); err != nil {
		t.Fatalf("no tracker file was created: %v", err)
	}
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatalf("the tracker init wrote does not load: %v", err)
	}
	if len(tr.Entries) != 0 {
		t.Errorf("a fresh tracker holds %d entries, want none", len(tr.Entries))
	}
}

func TestInitKeepsAnExistingTrackerFile(t *testing.T) {
	root := t.TempDir()
	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)
	before := cliRead(t, root, config.Dir+"/tracker.json")

	res := runKatana(t, cliOptions{}, "init", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "kept existing "+config.Dir+"/tracker.json", "report")
	if got := cliRead(t, root, config.Dir+"/tracker.json"); got != before {
		t.Errorf("the existing tracker was rewritten:\n%s", got)
	}
}

func TestTheTrackerItselfStaysUnderVersionControl(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	ignore := cliRead(t, root, config.Dir+"/.gitignore")
	wantContains(t, ignore, ".tracker-*.json", "tracker .gitignore")
	// The point of the rule: the scratch files are ignored and the tracker is
	// not, so a teammate's checkout knows what is already generated. katana
	// also ignores each machine's recorded run results, which the specification
	// does not mention; what it asserts is that the tracker stays tracked.
	for _, line := range strings.Split(ignore, "\n") {
		if strings.TrimSpace(line) == "tracker.json" {
			t.Errorf("the tracker itself is ignored:\n%s", ignore)
		}
	}
}

func TestAnExistingTrackerGitignoreKeepsItsOwnEntries(t *testing.T) {
	root := t.TempDir()
	cliWrite(t, root, config.Dir+"/.gitignore", "# ours\nscratch/\n")

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	// Read as: an existing file is not replaced. katana appends the patterns it
	// is missing rather than leaving the file byte-identical, so what is checked
	// is that nothing the project wrote there was lost.
	ignore := cliRead(t, root, config.Dir+"/.gitignore")
	wantContains(t, ignore, "# ours", "existing .gitignore")
	wantContains(t, ignore, "scratch/", "existing .gitignore")
}

func TestInitWritesASampleBehaviorFile(t *testing.T) {
	root := t.TempDir()

	res := runKatana(t, cliOptions{}, "init", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, cliRead(t, root, "behaviors/example.md"), "# Shopping cart checkout", "sample behavior")
	wantContains(t, res.stdout, "created behaviors/example.md", "report")
}

func TestNoSampleSkipsTheSampleBehaviorFile(t *testing.T) {
	root := t.TempDir()

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root, "--no-sample"), 0)

	if cliExists(root, "behaviors/example.md") {
		t.Error("--no-sample still wrote the sample behavior file")
	}
	if !cliExists(root, "behaviors") {
		t.Error("--no-sample should still create the behaviors directory")
	}
}

func TestAnExistingSampleBehaviorFileIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	cliWrite(t, root, "behaviors/example.md", "# mine\n")

	wantExit(t, runKatana(t, cliOptions{}, "init", "--dir", root), 0)

	if got := cliRead(t, root, "behaviors/example.md"); got != "# mine\n" {
		t.Errorf("the existing sample was overwritten, got:\n%s", got)
	}
}

func TestInitReportsTheHarnessAndLanguageItConfigured(t *testing.T) {
	root := t.TempDir()

	res := runKatana(t, cliOptions{}, "init",
		"--dir", root, "--harness", "codex", "--language", "python", "--behaviors", "specs")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "katana is set up with the codex harness generating python tests.", "report")
	wantContains(t, res.stdout, "Next: describe a behavior in specs/, then run `katana generate`.", "report")
}

// --- Discovering behavior from existing code -------------------------------

// discoverProject lays out a project with source code and no behavior files.
func discoverProject(t *testing.T, spec projectSpec) string {
	t.Helper()
	spec.behaviors = []string{}
	return cliProject(t, spec)
}

const billingSource = "package billing\n\nfunc Charge(cents int) error { return nil }\n"

func TestDiscoverWritesBehaviorFilesMirroringTheSourceTree(t *testing.T) {
	root := discoverProject(t, projectSpec{
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, cliRead(t, root, "behaviors/internal/billing.md"), "# What billing does", "discovered behavior")
}

func TestDiscoverReadsTheProjectsConfiguredDefaultLanguage(t *testing.T) {
	root := discoverProject(t, projectSpec{
		defaults: "  language: python\n  output_dir: tests\n",
		files:    map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "no python source found in this project", "report")
}

func TestLanguageOverridesTheLanguageRead(t *testing.T) {
	root := discoverProject(t, projectSpec{
		defaults: "  language: python\n  output_dir: tests\n",
		files:    map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--language", "go")

	wantExit(t, res, 0)
	if !cliExists(root, "behaviors/internal/billing.md") {
		t.Errorf("--language go read nothing:\n%s", res.out())
	}
}

func TestOutOverridesWhereBehaviorFilesAreWritten(t *testing.T) {
	root := discoverProject(t, projectSpec{
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--out", "specs")

	wantExit(t, res, 0)
	if !cliExists(root, "specs/internal/billing.md") {
		t.Errorf("--out did not move the output:\n%s", res.out())
	}
}

func TestPathLimitsDiscoveryAndMayBeRepeated(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go": billingSource,
		"internal/cart/cart.go":      "package cart\n",
		"internal/audit/audit.go":    "package audit\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root,
		"--path", "internal/billing", "--path", "internal/cart")

	wantExit(t, res, 0)
	if !cliExists(root, "behaviors/internal/billing.md") || !cliExists(root, "behaviors/internal/cart.md") {
		t.Errorf("both named paths should have been discovered:\n%s", res.out())
	}
	if cliExists(root, "behaviors/internal/audit.md") {
		t.Error("a path that was not named should not have been discovered")
	}
}

func TestExcludeSkipsADirectoryAndMayBeRepeated(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go": billingSource,
		"internal/cart/cart.go":      "package cart\n",
		"internal/audit/audit.go":    "package audit\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root,
		"--exclude", "cart", "--exclude", "internal/audit")

	wantExit(t, res, 0)
	if !cliExists(root, "behaviors/internal/billing.md") {
		t.Errorf("the directory that was not excluded should have been discovered:\n%s", res.out())
	}
	if cliExists(root, "behaviors/internal/cart.md") {
		t.Error("--exclude by name did not skip the directory")
	}
	if cliExists(root, "behaviors/internal/audit.md") {
		t.Error("--exclude by path did not skip the directory")
	}
}

func TestGroupDefaultsToOneBehaviorFilePerDirectory(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go": billingSource,
		"internal/billing/refund.go": "package billing\n\nfunc Refund() {}\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "wrote 1 behavior file(s)", "report")
	if !cliExists(root, "behaviors/internal/billing.md") {
		t.Error("the directory did not become one behavior file")
	}
}

func TestGroupFileProducesOneBehaviorFilePerSourceFile(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go": billingSource,
		"internal/billing/refund.go": "package billing\n\nfunc Refund() {}\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--group", "file")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "wrote 2 behavior file(s)", "report")
	if !cliExists(root, "behaviors/internal/billing/charge.md") {
		t.Errorf("--group file did not write a behavior file per source file:\n%s", res.out())
	}
}

func TestTestCodeIsLeftOutUnlessIncludeTestsIsGiven(t *testing.T) {
	files := map[string]string{
		"internal/billing/charge.go":      billingSource,
		"internal/billing/charge_test.go": "package billing\n\nfunc TestCharge(t *testing.T) {}\n",
	}

	root := discoverProject(t, projectSpec{files: files})
	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--verbose")
	wantExit(t, res, 0)
	wantAbsent(t, res.stdout, "charge_test.go", "files read")

	withTests := discoverProject(t, projectSpec{files: files})
	res = runKatana(t, cliOptions{}, "discover", "--dir", withTests, "--verbose", "--include-tests")
	wantExit(t, res, 0)
	wantContains(t, res.stdout, "charge_test.go", "files read with --include-tests")
}

func TestNoSourceFoundReportsTheProjectAndSucceeds(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{"docs/readme.txt": "hello\n"}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "no go source found in this project", "report")
	wantContains(t, res.stdout, "check defaults.language in "+config.FileName+", or pass --language and --path", "advice")
}

func TestNoSourceFoundNamesThePathsThatWereGiven(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"docs/readme.txt":  "hello\n",
		"notes/todo.txt":   "later\n",
		"internal/main.go": "package internal\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--path", "docs", "--path", "notes")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "no go source found in docs, notes", "report")
}

func TestAUnitThatAlreadyHasABehaviorFileIsSkipped(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go":    billingSource,
		"behaviors/internal/billing.md": "# hand written\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"skip  internal/billing → behaviors/internal/billing.md (already written; pass --force to update it against the code)",
		"report")
	if got := cliRead(t, root, "behaviors/internal/billing.md"); got != "# hand written\n" {
		t.Errorf("the existing behavior file was rewritten:\n%s", got)
	}
}

func TestForceRewritesExistingBehaviorFilesAndLabelsThemUpdate(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go":    billingSource,
		"behaviors/internal/billing.md": "# hand written\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "internal/billing → behaviors/internal/billing.md (update)", "report")
	wantContains(t, cliRead(t, root, "behaviors/internal/billing.md"), "# What billing does", "rewritten behavior")
}

func TestWhenEveryUnitHasABehaviorFileDiscoverRunsNoHarness(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent.log")
	root := discoverProject(t, projectSpec{
		agent: agentSpec{log: log},
		files: map[string]string{
			"internal/billing/charge.go":    billingSource,
			"behaviors/internal/billing.md": "# hand written\n",
		},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "all 1 unit(s) already have behavior files", "report")
	if _, err := os.Stat(log); err == nil {
		t.Error("the harness was run for a unit that already had a behavior file")
	}
}

func TestDryRunListsEachUnitAndRunsNoHarness(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent.log")
	root := discoverProject(t, projectSpec{
		agent: agentSpec{log: log},
		files: map[string]string{
			"internal/billing/charge.go": billingSource,
			"internal/billing/refund.go": "package billing\n\nfunc Refund() {}\n",
		},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--dry-run")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "1 behavior file(s) would be discovered:", "report")
	wantContains(t, res.stdout, "internal/billing", "unit")
	wantContains(t, res.stdout, "behaviors/internal/billing.md", "output")
	wantContains(t, res.stdout, "reading 2 go file(s) with fake", "summary")
	if _, err := os.Stat(log); err == nil {
		t.Error("--dry-run ran the harness")
	}
	if cliExists(root, "behaviors/internal/billing.md") {
		t.Error("--dry-run wrote a behavior file")
	}
}

func TestTheHarnessIsCheckedOnceBeforeAnyUnitIsStarted(t *testing.T) {
	root := discoverProject(t, projectSpec{
		noHarnessCommand: true,
		files: map[string]string{
			"internal/billing/charge.go": billingSource,
			"internal/cart/cart.go":      "package cart\n",
			"internal/audit/audit.go":    "package audit\n",
		},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "katana-no-such-agent", "error")
	wantAbsent(t, res.out(), "failed:", "per-unit failures")
	wantAbsent(t, res.out(), "unit(s) failed", "per-unit failures")
}

func TestEachFinishedUnitReportsItsSizeArrivalAndElapsedTime(t *testing.T) {
	root := discoverProject(t, projectSpec{
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, fmt.Sprintf("ok: %d bytes, written by harness, ", len(agentSpecFile)), "unit report")
}

func TestASpecificationRecoveredFromHarnessStdoutIsReportedAsSuch(t *testing.T) {
	root := discoverProject(t, projectSpec{
		agent: agentSpec{mode: "stdout"},
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "recovered from harness stdout", "unit report")
	wantContains(t, cliRead(t, root, "behaviors/internal/billing.md"), "# What billing does", "recovered behavior")
}

func TestABehaviorFileTheHarnessLeftAloneIsReportedAsNothingToCorrect(t *testing.T) {
	root := discoverProject(t, projectSpec{
		agent: agentSpec{mode: "unchanged"},
		files: map[string]string{
			"internal/billing/charge.go":    billingSource,
			"behaviors/internal/billing.md": "# already right\n",
		},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "unchanged; harness found nothing to correct", "unit report")
}

func TestAUnitTheHarnessDeclinedIsReportedAsSkipped(t *testing.T) {
	root := discoverProject(t, projectSpec{
		agent: agentSpec{mode: "skip"},
		files: map[string]string{
			"internal/billing/charge.go": billingSource,
			"internal/cart/cart.go":      "package cart\n",
		},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "skipped: no product behavior worth specifying", "unit report")
	wantContains(t, res.stdout, "wrote 0 behavior file(s), 2 unit(s) had no behavior to specify", "summary")
}

func TestAUnitThatErroredIsReportedOnStandardError(t *testing.T) {
	root := discoverProject(t, projectSpec{
		agent: agentSpec{mode: "fail"},
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "failed:", "unit failure")
	wantAbsent(t, res.stdout, "failed:", "standard output")
}

func TestDiscoverExitsNonZeroWhenAUnitFailed(t *testing.T) {
	root := discoverProject(t, projectSpec{
		agent: agentSpec{failMatch: "cart"},
		files: map[string]string{
			"internal/billing/charge.go": billingSource,
			"internal/cart/cart.go":      "package cart\n",
		},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--jobs", "1")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "1 of 2 unit(s) failed", "error")
}

func TestDiscoverEndsByReportingHowManyBehaviorFilesItWrote(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go": billingSource,
		"internal/cart/cart.go":      "package cart\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "wrote 2 behavior file(s)", "summary")
}

func TestBehaviorFilesWrittenOutsideTheConfiguredPathsAreNoted(t *testing.T) {
	root := cliProject(t, projectSpec{
		behaviors: []string{"cart"},
		paths:     []string{"behaviors/cart.md"},
		files:     map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--out", "specs")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"note: 1 of them are outside the behaviors configured in "+config.FileName+" and will not be generated from.",
		"note")
	wantContains(t, res.stdout, "add `- path: specs/internal` under behaviors:", "advice")
}

func TestAConfigurationThatCannotBeResolvedProducesNoSuchNote(t *testing.T) {
	// The configured behaviors path matches nothing, so katana cannot say what
	// is or is not covered — and says nothing either way.
	root := discoverProject(t, projectSpec{
		paths: []string{"behaviors"},
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--out", "specs")

	wantExit(t, res, 0)
	wantAbsent(t, res.stdout, "will not be generated from", "note")
	wantAbsent(t, res.stdout, "under behaviors:", "advice")
}

func TestDiscoverTellsTheUserToReviewWhatItWrote(t *testing.T) {
	root := discoverProject(t, projectSpec{
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"review what was written — it describes what the code does today, bugs included — then run `katana generate`",
		"advice")
}

// --- Generating tests from behaviors ---------------------------------------

func TestGenerateOnlyProducesTestsForBehaviorsThatNeedIt(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent.log")
	root := cliProject(t, projectSpec{agent: agentSpec{log: log}, behaviors: []string{"cart", "checkout"}})

	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	first := cliRead(t, filepath.Dir(log), filepath.Base(log))

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "all 2 behavior(s) up to date", "report")
	if got := cliRead(t, filepath.Dir(log), filepath.Base(log)); got != first {
		t.Errorf("the second run invoked the harness again:\n%s", got)
	}
}

func TestFileLimitsTheRunToNamedBehaviorFiles(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout", "refund"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root,
		"--file", "behaviors/cart.md", "--file", "behaviors/refund.md")

	wantExit(t, res, 0)
	if !cliExists(root, "tests/cart_test.go") || !cliExists(root, "tests/refund_test.go") {
		t.Errorf("both named behaviors should have been generated:\n%s", res.out())
	}
	if cliExists(root, "tests/checkout_test.go") {
		t.Error("a behavior that was not named should not have been generated")
	}
}

func TestAnAbsoluteFilePathIsInterpretedRelativeToTheProjectRoot(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root,
		"--file", filepath.Join(root, "behaviors", "cart.md"))

	wantExit(t, res, 0)
	if !cliExists(root, "tests/cart_test.go") {
		t.Errorf("an absolute --file matched nothing:\n%s", res.out())
	}
	if cliExists(root, "tests/checkout_test.go") {
		t.Error("an absolute --file should still limit the run")
	}
}

func TestNoBehaviorMatchedIsReportedAndSucceeds(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--file", "behaviors/nope.md")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "no behaviors matched", "report")
}

func TestAnUnchangedBehaviorIsOnlyRegeneratedWithForce(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent.log")
	root := cliProject(t, projectSpec{agent: agentSpec{log: log}})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	first := cliRead(t, filepath.Dir(log), filepath.Base(log))

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generated 1 behavior(s)", "report")
	if got := cliRead(t, filepath.Dir(log), filepath.Base(log)); got == first {
		t.Error("--force did not re-run the harness for an up-to-date behavior")
	}
}

func TestAHandEditedTestFileIsSkippedRatherThanOverwritten(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	cliWrite(t, root, "tests/cart_test.go", agentTests+"\nfunc TestByHand(t *testing.T) {}\n")

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"skip  behaviors/cart.md → tests/cart_test.go ("+tracker.StatusOutputModified.String()+"; pass --force to regenerate over it)",
		"report")
	wantContains(t, cliRead(t, root, "tests/cart_test.go"), "func TestByHand", "hand edits")
}

func TestATestFileKatanaHasNoRecordOfWritingIsSkipped(t *testing.T) {
	root := cliProject(t, projectSpec{})
	cliWrite(t, root, "tests/cart_test.go", "package tests\n\nfunc TestMine(t *testing.T) {}\n")

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"skip  behaviors/cart.md → tests/cart_test.go ("+tracker.StatusOutputUntracked.String()+"; pass --force to regenerate over it)",
		"report")
	wantContains(t, cliRead(t, root, "tests/cart_test.go"), "func TestMine", "the untouched file")
}

func TestForceRegeneratesEveryMatchedBehaviorIncludingOnesKatanaDidNotWrite(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	cliWrite(t, root, "tests/cart_test.go", "package tests\n\nfunc TestMine(t *testing.T) {}\n")

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generated 2 behavior(s)", "report")
	wantAbsent(t, cliRead(t, root, "tests/cart_test.go"), "func TestMine", "regenerated file")
}

func TestNothingToDoAndNothingHeldBackReportsAllUpToDate(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "all 2 behavior(s) up to date", "report")
}

func TestNothingToDoWithBehaviorsHeldBackIsReportedSeparately(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	cliWrite(t, root, "tests/checkout_test.go", agentTests+"\nfunc TestByHand(t *testing.T) {}\n")

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"nothing to generate: 1 behavior(s) up to date, 1 left alone (pass --force to regenerate over them)",
		"report")
	wantAbsent(t, res.stdout, "all 2 behavior(s) up to date", "the all-up-to-date wording")
}

func TestDryRunListsWhatWouldBeGeneratedAndRunsNoHarness(t *testing.T) {
	log := filepath.Join(t.TempDir(), "agent.log")
	root := cliProject(t, projectSpec{agent: agentSpec{log: log}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--dry-run")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "1 behavior(s) would be generated:", "report")
	wantContains(t, res.stdout, tracker.StatusNew.String(), "status column")
	wantContains(t, res.stdout, "behaviors/cart.md", "source column")
	wantContains(t, res.stdout, "tests/cart_test.go", "output column")
	wantContains(t, res.stdout, "go/go-test via fake", "stack column")
	if _, err := os.Stat(log); err == nil {
		t.Error("--dry-run ran the harness")
	}
	if cliExists(root, "tests/cart_test.go") {
		t.Error("--dry-run generated a file")
	}
}

func TestEveryHarnessIsBuiltAndCheckedBeforeAnyBehaviorIsGenerated(t *testing.T) {
	root := cliProject(t, projectSpec{
		noHarnessCommand: true,
		behaviors:        []string{"cart", "checkout", "refund"},
	})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "katana-no-such-agent", "error")
	wantAbsent(t, res.stderr, "failed to generate", "per-behavior failures")
	if cliExists(root, "tests") {
		t.Error("no behavior should have been started")
	}
}

func TestEachFinishedBehaviorReportsItsSizeCasesArrivalAndTime(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		fmt.Sprintf("ok: %d bytes, 2 test case(s), written by harness, ", len(agentTests)),
		"behavior report")
}

func TestTestsRecoveredFromHarnessStdoutAreReportedAsSuch(t *testing.T) {
	root := cliProject(t, projectSpec{agent: agentSpec{mode: "stdout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "recovered from harness stdout", "behavior report")
	wantContains(t, cliRead(t, root, "tests/cart_test.go"), "func TestOne", "recovered tests")
}

func TestAnUnchangedTestFileIsReportedAsJudgedSufficient(t *testing.T) {
	root := cliProject(t, projectSpec{agent: agentSpec{mode: "unchanged"}})
	cliWrite(t, root, "tests/cart_test.go", agentTests)

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "unchanged; harness judged existing tests sufficient", "behavior report")
}

func TestTheTrackerEntryRecordsEverythingAboutTheGeneration(t *testing.T) {
	root := cliProject(t, projectSpec{})

	wantExit(t, runKatana(t, cliOptions{version: "v3.1.4"}, "generate", "--dir", root), 0)

	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := tr.Get("behaviors/cart.md")
	if !ok {
		t.Fatal("the behavior was not recorded")
	}
	srcHash, err := tracker.HashFile(filepath.Join(root, "behaviors", "cart.md"))
	if err != nil {
		t.Fatal(err)
	}
	outHash, err := tracker.HashFile(filepath.Join(root, "tests", "cart_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Source != "behaviors/cart.md" || e.Output != "tests/cart_test.go" {
		t.Errorf("entry names %q → %q", e.Source, e.Output)
	}
	if e.SourceHash != srcHash || e.OutputHash != outHash {
		t.Errorf("entry hashes = %q/%q, want %q/%q", e.SourceHash, e.OutputHash, srcHash, outHash)
	}
	if strings.Join(e.Tests, ",") != "TestOne,TestTwo" {
		t.Errorf("entry tests = %q, want [TestOne TestTwo]", e.Tests)
	}
	if e.Language != "go" || e.Framework != "go-test" || e.Harness != "fake" {
		t.Errorf("entry stack = %s/%s via %s", e.Language, e.Framework, e.Harness)
	}
	if e.GeneratedAt.IsZero() {
		t.Error("entry records no generation time")
	}
	if e.KatanaVersion != "v3.1.4" {
		t.Errorf("entry katana version = %q, want v3.1.4", e.KatanaVersion)
	}
}

func TestTheTrackerIsWrittenToDiskAsEachBehaviorFinishes(t *testing.T) {
	// The second behavior fails, so the only way its predecessor's entry is on
	// disk afterwards is that it was written before the run was over.
	root := cliProject(t, projectSpec{
		agent:     agentSpec{failMatch: "checkout"},
		behaviors: []string{"cart", "checkout"},
	})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "1")

	wantExit(t, res, 1)
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Get("behaviors/cart.md"); !ok {
		t.Error("the behavior that succeeded was not recorded")
	}
	if _, ok := tr.Get("behaviors/checkout.md"); ok {
		t.Error("the behavior that failed was recorded")
	}
}

func TestAFailureToWriteTheTrackerIsWarnedAboutAndDoesNotStopGeneration(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("this behavior is observed by making the tracker directory unwritable")
	}
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	katanaDir := filepath.Join(root, config.Dir)
	if err := os.MkdirAll(katanaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(katanaDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(katanaDir, 0o755) })

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "1")

	wantContains(t, res.stderr, "warning: could not update tracker:", "warning")
	for _, name := range []string{"tests/cart_test.go", "tests/checkout_test.go"} {
		if !cliExists(root, name) {
			t.Errorf("generation stopped: %s was never written\n%s", name, res.out())
		}
	}
}

func TestTrackerEntriesForBehaviorsNoLongerConfiguredArePruned(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	if err := os.Remove(filepath.Join(root, "behaviors", "checkout.md")); err != nil {
		t.Fatal(err)
	}

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "pruned tracker entry for removed behavior behaviors/checkout.md", "report")
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Get("behaviors/checkout.md"); ok {
		t.Error("the entry for the removed behavior is still recorded")
	}
}

func TestARunLimitedWithFilePrunesNothing(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	if err := os.Remove(filepath.Join(root, "behaviors", "checkout.md")); err != nil {
		t.Fatal(err)
	}

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--file", "behaviors/cart.md", "--force")

	wantExit(t, res, 0)
	wantAbsent(t, res.stdout, "pruned tracker entry", "report")
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tr.Get("behaviors/checkout.md"); !ok {
		t.Error("a limited run pruned an entry it was never asked about")
	}
}

func TestGenerateExitsNonZeroWhenABehaviorFailed(t *testing.T) {
	root := cliProject(t, projectSpec{
		agent:     agentSpec{failMatch: "checkout"},
		behaviors: []string{"cart", "checkout"},
	})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "1")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "1 of 2 behavior(s) failed to generate", "error")
}

func TestWhenAllPlannedBehaviorsSucceededGenerateReportsTheCount(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generated 2 behavior(s)", "report")
	wantAbsent(t, res.stdout, "generated 2 of", "report")
}

func TestAGeneratedFileThatIsGoneAfterwardsSurfacesAsOutputMissing(t *testing.T) {
	// katana does not fail the generation over a file that is not there — the
	// state shows up on the next status instead.
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	if err := os.Remove(filepath.Join(root, "tests", "cart_test.go")); err != nil {
		t.Fatal(err)
	}

	res := runKatana(t, cliOptions{}, "status", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, tracker.StatusOutputMissing.String(), "status")
}

// --- Choosing how many run at once -----------------------------------------

func TestAJobsValueBelowOneFailsBeforeAnyWorkStarts(t *testing.T) {
	for _, cmd := range []string{"generate", "discover"} {
		t.Run(cmd, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "agent.log")
			root := cliProject(t, projectSpec{
				agent: agentSpec{log: log},
				files: map[string]string{"internal/billing/charge.go": billingSource},
			})

			res := runKatana(t, cliOptions{}, cmd, "--dir", root, "--jobs", "0")

			wantExit(t, res, 1)
			wantContains(t, res.stderr, "--jobs must be at least 1, got 0", "error")
			if _, err := os.Stat(log); err == nil {
				t.Error("work started before the flag was rejected")
			}
		})
	}
}

func TestJIsTheShorthandForJobs(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout", "refund", "returns"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "-j", "2")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generating 4 behavior(s), 2 at a time", "announcement")
}

func TestTheShorthandRejectsAValueBelowOneToo(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "-j", "-3")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "--jobs must be at least 1, got -3", "error")
}

func TestWithoutJobsTheCountComesFromTheConfiguredHarnessJobs(t *testing.T) {
	root := cliProject(t, projectSpec{jobs: 2, behaviors: []string{"cart", "checkout", "refund", "returns"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generating 4 behavior(s), 2 at a time", "announcement")
}

func TestAJobCountLargerThanTheWorkIsReducedToIt(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "8")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generating 2 behavior(s), 2 at a time", "announcement")
}

func TestVerboseWithoutJobsNarratesOneBehaviorAtATime(t *testing.T) {
	root := cliProject(t, projectSpec{jobs: 4, behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"note: --verbose narrates one behavior at a time; pass --jobs N to generate in parallel",
		"note")
	wantContains(t, res.stdout, "generating 2 behavior(s)\n", "announcement")
}

func TestVerboseWithAnExplicitJobsKeepsThatCount(t *testing.T) {
	root := cliProject(t, projectSpec{jobs: 4, behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose", "--jobs", "2")

	wantExit(t, res, 0)
	wantAbsent(t, res.stdout, "--verbose narrates one behavior at a time", "note")
	wantContains(t, res.stdout, "generating 2 behavior(s), 2 at a time", "announcement")
}

func TestWithOneWorkerTheCountIsAnnouncedWithoutARate(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "1")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "generating 2 behavior(s)\n", "announcement")
	wantAbsent(t, res.stdout, "at a time", "announcement")
}

func TestWithOneWorkerEachItemIsAnnouncedAsItStarts(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "1")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"[1/2] behaviors/cart.md → tests/cart_test.go ("+tracker.StatusNew.String()+")", "counter")
	wantContains(t, res.stdout,
		"[2/2] behaviors/checkout.md → tests/checkout_test.go ("+tracker.StatusNew.String()+")", "counter")
	wantAbsent(t, res.stdout, "  start ", "start lines")
}

func TestWithMoreThanOneWorkerEachItemAnnouncesItsStartImmediately(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "2")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"start behaviors/cart.md → tests/cart_test.go ("+tracker.StatusNew.String()+")", "start line")
	wantContains(t, res.stdout,
		"start behaviors/checkout.md → tests/checkout_test.go ("+tracker.StatusNew.String()+")", "start line")
}

func TestWithMoreThanOneWorkerEachNarrationIsPrintedAsOneBlock(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "2")

	wantExit(t, res, 0)
	// Every block is its header followed immediately by that behavior's own
	// lines: nothing from the other agent may appear between them.
	lines := strings.Split(res.stdout, "\n")
	blocks := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, "[") {
			continue
		}
		blocks++
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "  ok:") {
			t.Fatalf("block %q is not followed by its own report:\n%s", line, res.stdout)
		}
	}
	if blocks != 2 {
		t.Errorf("printed %d blocks, want 2:\n%s", blocks, res.stdout)
	}
}

func TestWithMoreThanOneWorkerTheCounterFollowsCompletionOrder(t *testing.T) {
	root := cliProject(t, projectSpec{
		agent:     agentSpec{slowMatch: "cart_test.go"},
		behaviors: []string{"cart", "checkout"},
	})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--jobs", "2")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "[1/2] behaviors/checkout.md", "completion order")
	wantContains(t, res.stdout, "[2/2] behaviors/cart.md", "completion order")
}

func TestAnInterruptStopsHandingOutNewWorkAndLetsWhatIsRunningFinish(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("interrupting a child process is not portable to Windows")
	}
	log := filepath.Join(t.TempDir(), "agent.log")
	root := cliProject(t, projectSpec{
		agent:     agentSpec{log: log, delay: 700 * time.Millisecond},
		behaviors: []string{"a1", "a2", "a3", "a4"},
	})

	res := runKatana(t, cliOptions{onStart: func(cmd *exec.Cmd) {
		// Interrupt once the first generation is under way, so exactly the work
		// already in flight is what gets to finish.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(log); err == nil && strings.Contains(string(b), "start") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cmd.Process.Signal(os.Interrupt)
	}}, "generate", "--dir", root, "--jobs", "1")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "interrupted; stopping", "report")
	wantContains(t, res.stdout, " of 4 behavior(s)", "report")
	wantAbsent(t, res.stdout, "generated 4 behavior(s)", "report")
}

// --- Deciding whether a behavior is current --------------------------------

// statusOf runs status and returns the row for a behavior source.
func statusRow(t *testing.T, root, source string) string {
	t.Helper()
	res := runKatana(t, cliOptions{}, "status", "--dir", root)
	for _, line := range strings.Split(res.stdout, "\n") {
		if strings.Contains(line, source) && !strings.Contains(line, "→") {
			return line
		}
	}
	t.Fatalf("status printed no row for %s:\n%s", source, res.stdout)
	return ""
}

func TestABehaviorWithNoEntryAndNoTestFileIsNew(t *testing.T) {
	root := cliProject(t, projectSpec{})

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusNew.String(), "status row")
}

func TestABehaviorWithNoEntryButAnExistingTestFileIsOutputNotTracked(t *testing.T) {
	root := cliProject(t, projectSpec{})
	cliWrite(t, root, "tests/cart_test.go", agentTests)

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusOutputUntracked.String(), "status row")
}

func TestAChangedSpecificationOutranksAHandEditedTestFile(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	cliWrite(t, root, "behaviors/cart.md", "# cart\n\n## Its rules\n\n- It does something else now.\n")
	cliWrite(t, root, "tests/cart_test.go", agentTests+"\nfunc TestByHand(t *testing.T) {}\n")

	row := statusRow(t, root, "behaviors/cart.md")
	wantContains(t, row, tracker.StatusBehaviorChanged.String(), "status row")
	wantAbsent(t, row, tracker.StatusOutputModified.String(), "status row")
}

func TestARecordedStackThatNoLongerMatchesTheConfigurationIsConfigChanged(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)

	// The framework the tests were generated for is no longer the one
	// configured, so the recorded entry no longer describes what katana would
	// produce today.
	cfg := cliRead(t, root, config.FileName)
	cfg = strings.Replace(cfg, "defaults:\n", "defaults:\n  framework: ginkgo\n", 1)
	cliWrite(t, root, config.FileName, cfg)

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusConfigChanged.String(), "status row")
}

func TestARecordedOutputPathThatNoLongerMatchesTheConfigurationIsConfigChanged(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)

	cfg := cliRead(t, root, config.FileName)
	cfg = strings.Replace(cfg, "output_dir: tests", "output_dir: suite", 1)
	cliWrite(t, root, config.FileName, cfg)

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusConfigChanged.String(), "status row")
}

func TestABehaviorWhoseGeneratedFileIsAbsentIsOutputMissing(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	if err := os.Remove(filepath.Join(root, "tests", "cart_test.go")); err != nil {
		t.Fatal(err)
	}

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusOutputMissing.String(), "status row")
}

func TestABehaviorWhoseGeneratedFileDiffersFromTheRecordedHashIsOutputModified(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	cliWrite(t, root, "tests/cart_test.go", agentTests+"\nfunc TestByHand(t *testing.T) {}\n")

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusOutputModified.String(), "status row")
}

func TestABehaviorMatchingItsTrackerEntryInEveryRespectIsUpToDate(t *testing.T) {
	root := cliProject(t, projectSpec{})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusUpToDate.String(), "status row")
}

func TestARecordedEntryWithNoOutputHashNeverCountsAsModified(t *testing.T) {
	root := cliProject(t, projectSpec{})
	cliWrite(t, root, "tests/cart_test.go", agentTests)

	srcHash, err := tracker.HashFile(filepath.Join(root, "behaviors", "cart.md"))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	tr.Record(tracker.Entry{
		Source:      "behaviors/cart.md",
		SourceHash:  srcHash,
		Output:      "tests/cart_test.go",
		Language:    "go",
		Framework:   "go-test",
		Harness:     "fake",
		GeneratedAt: time.Now().UTC(),
	})
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}

	wantContains(t, statusRow(t, root, "behaviors/cart.md"), tracker.StatusUpToDate.String(), "status row")
}

// --- Reporting status ------------------------------------------------------

func TestStatusPrintsARowPerMatchedBehavior(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "status", "--dir", root)

	wantExit(t, res, 0)
	for _, heading := range []string{"STATUS", "BEHAVIOR", "TESTS", "STACK"} {
		wantContains(t, res.stdout, heading, "table heading")
	}
	for _, cell := range []string{
		"behaviors/cart.md", "tests/cart_test.go",
		"behaviors/checkout.md", "tests/checkout_test.go",
		"go/go-test via fake",
	} {
		wantContains(t, res.stdout, cell, "table row")
	}
}

func TestStatusWithNoMatchingBehaviorSucceeds(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "status", "--dir", root, "--file", "behaviors/nope.md")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "no behaviors matched", "report")
}

func TestFileLimitsTheStatusReportAndMayBeRepeated(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout", "refund"}})

	res := runKatana(t, cliOptions{}, "status", "--dir", root,
		"--file", "behaviors/cart.md", "--file", "behaviors/refund.md")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "behaviors/cart.md", "report")
	wantContains(t, res.stdout, "behaviors/refund.md", "report")
	wantAbsent(t, res.stdout, "behaviors/checkout.md", "report")
	wantContains(t, res.stdout, "2 behavior(s)", "summary")
}

func TestStatusEndsWithTheBehaviorAndOutOfDateCounts(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root, "--file", "behaviors/cart.md"), 0)

	res := runKatana(t, cliOptions{}, "status", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "2 behavior(s), 1 out of date", "summary")
}

func TestStatusAdvisesGeneratingWhenAnythingIsOutOfDate(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "status", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "run `katana generate` to bring them up to date", "advice")
}

func TestStrictMakesAnOutOfDateBehaviorAFailure(t *testing.T) {
	root := cliProject(t, projectSpec{behaviors: []string{"cart", "checkout"}})

	res := runKatana(t, cliOptions{}, "status", "--dir", root, "--strict")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "2 behavior(s) out of date", "error")
	// The report itself is still printed before the failure.
	wantContains(t, res.stdout, "2 behavior(s), 2 out of date", "summary")
}

func TestWithoutStrictAnOutOfDateBehaviorIsNotAFailure(t *testing.T) {
	root := cliProject(t, projectSpec{})

	wantExit(t, runKatana(t, cliOptions{}, "status", "--dir", root), 0)
}

// --- Running the test suite ------------------------------------------------

// generatedProject is a project whose one behavior is already generated and up
// to date, so a run has nothing to warn about.
func generatedProject(t *testing.T, spec projectSpec) string {
	t.Helper()
	root := cliProject(t, spec)
	wantExit(t, runKatana(t, cliOptions{}, "generate", "--dir", root), 0)
	return root
}

func TestRunExecutesTheConfiguredTestCommand(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "touch suite-ran"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root)

	wantExit(t, res, 0)
	if !cliExists(root, "suite-ran") {
		t.Errorf("the test command never ran:\n%s", res.out())
	}
}

func TestRunUsesTheConfiguredTestDirectory(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{
		testCmd: "touch suite-ran",
		testDir: "sub",
		files:   map[string]string{"sub/keep.txt": "\n"},
	})

	res := runKatana(t, cliOptions{}, "run", "--dir", root)

	wantExit(t, res, 0)
	if !cliExists(root, "sub/suite-ran") {
		t.Errorf("the suite did not run in the configured directory:\n%s", res.out())
	}
}

func TestRunWithoutATestCommandFails(t *testing.T) {
	// katana fills an unset test command in from the language's convention, so
	// the only project without one is a project in a language it has no
	// conventions for.
	root := cliProject(t, projectSpec{defaults: "  language: fortran\n  output_dir: tests\n"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root)

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "no test.command set in katana.yaml", "error")
}

func TestArgumentsAfterADoubleDashAreShellQuotedAndAppended(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "printf '[%s]'"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--", "a b", "c'd")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "[a b][c'd]", "suite output")
}

func TestOutOfDateBehaviorsAreListedBeforeTheSuiteRuns(t *testing.T) {
	skipWithoutShell(t)
	root := cliProject(t, projectSpec{testCmd: "touch suite-ran"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stderr, "1 behavior(s) out of date with their tests:", "warning")
	wantContains(t, res.stderr, tracker.StatusNew.String(), "warning")
	wantContains(t, res.stderr, "behaviors/cart.md", "warning")
	wantContains(t, res.stderr, "run `katana generate` first", "advice")
	if !cliExists(root, "suite-ran") {
		t.Error("the warning should not have stopped the suite")
	}
}

func TestCheckTurnsTheStalenessWarningIntoAFailureWithoutRunningTheSuite(t *testing.T) {
	skipWithoutShell(t)
	root := cliProject(t, projectSpec{testCmd: "touch suite-ran"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--check")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "1 behavior(s) out of date (--check)", "error")
	if cliExists(root, "suite-ran") {
		t.Error("--check ran the suite anyway")
	}
}

func TestTheSuiteOutputReachesTheTerminal(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "echo suite-says-hello; echo suite-says-oops 1>&2"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "suite-says-hello", "suite standard output")
	wantContains(t, res.stderr, "suite-says-oops", "suite standard error")
}

func TestATestCommandThatCannotStartAtAllFailsTheRun(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "true"})

	res := runKatana(t, cliOptions{env: map[string]string{"SHELL": "/no/such/shell"}}, "run", "--dir", root)

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "running test command:", "error")
}

func TestKatanaExitsWithTheSuitesOwnExitCode(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "exit 7"})

	res := runKatana(t, cliOptions{}, "run", "--dir", root)

	wantExit(t, res, 7)
}

func TestTheCommandRunsThroughTheShellNamedBySHELL(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "touch suite-ran"})
	shell := filepath.Join(root, "myshell")
	if err := os.WriteFile(shell,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \""+root+"/shell.log\"\nexec /bin/sh \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := runKatana(t, cliOptions{env: map[string]string{"SHELL": shell}}, "run", "--dir", root)

	wantExit(t, res, 0)
	wantContains(t, cliRead(t, root, "shell.log"), "-c", "the flag the shell was given")
	if !cliExists(root, "suite-ran") {
		t.Error("the suite did not run through the named shell")
	}
}

func TestWithNoSHELLTheCommandFallsBackToBinSh(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "touch suite-ran"})

	// An empty SHELL is the same as none: /bin/sh is what is left.
	res := runKatana(t, cliOptions{env: map[string]string{"SHELL": ""}}, "run", "--dir", root)

	wantExit(t, res, 0)
	if !cliExists(root, "suite-ran") {
		t.Errorf("the suite did not run:\n%s", res.out())
	}
}

// --- Saving a test report --------------------------------------------------

// goStyleOutput is a suite whose output katana can read case by case.
const goStyleOutput = `printf '%s\n' '--- PASS: TestOne (0.01s)' '--- FAIL: TestTwo (0.00s)' '    cart_test.go:9: nope' '--- SKIP: TestThree (0.00s)'`

func reportPath(t *testing.T, root, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		t.Fatalf("no report directory %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".html") {
			return filepath.Join(root, filepath.FromSlash(dir), e.Name())
		}
	}
	t.Fatalf("no HTML report in %s", dir)
	return ""
}

func TestSaveWritesASelfContainedReportOfEveryCase(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: goStyleOutput})

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--save")

	wantExit(t, res, 0)
	page := cliRead(t, filepath.Dir(reportPath(t, root, "out")), filepath.Base(reportPath(t, root, "out")))
	for _, want := range []string{"TestOne", "TestTwo", "TestThree", "nope", "behaviors/cart.md"} {
		wantContains(t, page, want, "report")
	}
}

func TestSaveAddsVerboseToTheTestCommandWhereTheRunnerSupportsIt(t *testing.T) {
	skipWithoutShell(t)
	// echo stands in for the runner: what matters is that katana recognises the
	// go test command line and appends -v to it.
	root := generatedProject(t, projectSpec{testCmd: "echo go test ./..."})

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--save")

	wantExit(t, res, 0)
	wantContains(t, res.stderr, "added -v to the test command so each case is recorded", "note")
	wantContains(t, res.stdout, "go test ./... -v", "adjusted command")
}

func TestTheSuiteOutputIsCopiedAsItStreamsWithSave(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "echo suite-says-hello"})

	without := runKatana(t, cliOptions{}, "run", "--dir", root)
	with := runKatana(t, cliOptions{}, "run", "--dir", root, "--save")

	wantExit(t, without, 0)
	wantExit(t, with, 0)
	wantContains(t, without.stdout, "suite-says-hello", "suite output")
	wantContains(t, with.stdout, "suite-says-hello", "suite output with --save")
}

func TestTheReportDirectoryDefaultsToOutUnderTheProjectRoot(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: goStyleOutput})

	// Run from somewhere else entirely: the report still lands in the project.
	res := runKatana(t, cliOptions{dir: t.TempDir()}, "run", "--dir", root, "--save")

	wantExit(t, res, 0)
	reportPath(t, root, "out")
}

func TestOutIsResolvedAgainstTheProjectRootUnlessItIsAbsolute(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: goStyleOutput})

	res := runKatana(t, cliOptions{dir: t.TempDir()}, "run", "--dir", root, "--save", "--out", "reports")
	wantExit(t, res, 0)
	reportPath(t, root, "reports")

	elsewhere := t.TempDir()
	res = runKatana(t, cliOptions{}, "run", "--dir", root, "--save", "--out", elsewhere)
	wantExit(t, res, 0)
	reportPath(t, elsewhere, ".")
}

func TestOnSuccessRunPrintsTheReportPathWithTheCounts(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: goStyleOutput})

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--save")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, ".html (1 passed, 1 failed, 1 skipped)", "report line")
}

func TestAReportThatCannotBeWrittenFailsAPassingRun(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "true"})
	// A file where the report directory should be: the directory cannot be made.
	cliWrite(t, root, "blocked", "not a directory\n")

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--save", "--out", "blocked")

	wantExit(t, res, 1)
	wantContains(t, res.stderr, "writing test report:", "error")
}

func TestAFailedSuiteKeepsItsExitCodeWhenTheReportCannotBeWritten(t *testing.T) {
	skipWithoutShell(t)
	root := generatedProject(t, projectSpec{testCmd: "exit 4"})
	cliWrite(t, root, "blocked", "not a directory\n")

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--save", "--out", "blocked")

	wantExit(t, res, 4)
	wantContains(t, res.stderr, "katana: writing test report:", "error line")
}

func TestEachBehaviorInTheReportCarriesItsStatusStackAndStaleness(t *testing.T) {
	skipWithoutShell(t)
	root := cliProject(t, projectSpec{testCmd: goStyleOutput})

	res := runKatana(t, cliOptions{}, "run", "--dir", root, "--save")

	wantExit(t, res, 0)
	path := reportPath(t, root, "out")
	page := cliRead(t, filepath.Dir(path), filepath.Base(path))
	for _, want := range []string{
		"behaviors/cart.md",
		"tests/cart_test.go",
		tracker.StatusNew.String(),
		"go/go-test via fake",
		`class="stale"`,
	} {
		wantContains(t, page, want, "report behaviors")
	}
}

// --- Listing harnesses -----------------------------------------------------

func TestHarnessesPrintsARowPerKnownHarness(t *testing.T) {
	res := runKatana(t, cliOptions{}, "harnesses")

	wantExit(t, res, 0)
	for _, heading := range []string{"NAME", "INSTALLED", "INVOCATION", "PROMPT VIA", "DESCRIPTION"} {
		wantContains(t, res.stdout, heading, "table heading")
	}
	for _, s := range harness.Describe() {
		wantContains(t, res.stdout, s.Name, "harness row")
		wantContains(t, res.stdout, strings.Join(s.Args, " "), "invocation")
		wantContains(t, res.stdout, string(s.Prompt), "prompt delivery")
		wantContains(t, res.stdout, s.Docs, "description")
	}
}

func TestTheInstalledColumnSaysNoForAHarnessThatIsNotOnThePath(t *testing.T) {
	res := runKatana(t, cliOptions{env: map[string]string{"PATH": t.TempDir()}}, "harnesses")

	wantExit(t, res, 0)
	for _, line := range strings.Split(res.stdout, "\n") {
		// The table draws borders, so the name is not at the start of the line.
		if !strings.HasPrefix(strings.TrimLeft(line, "│ \t"), "claude") {
			continue
		}
		wantContains(t, line, "no", "installed column")
		return
	}
	t.Errorf("no row for claude:\n%s", res.stdout)
}

func TestTheInstalledColumnShowsTheResolvedPathWhenTheHarnessIsInstalled(t *testing.T) {
	skipWithoutShell(t)
	dir := t.TempDir()
	installed := filepath.Join(dir, "claude")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := runKatana(t, cliOptions{env: map[string]string{"PATH": dir}}, "harnesses")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, installed, "installed column")
}

func TestHarnessesExplainsThatTheDefaultsAreNotAContract(t *testing.T) {
	res := runKatana(t, cliOptions{}, "harnesses")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		"Defaults are katana's best-known invocation for each CLI, not a contract with\nthe upstream tool.",
		"explanation")
	for _, override := range []string{"command: codex", "args: [\"exec\", \"--full-auto\"]", "prompt: arg"} {
		wantContains(t, res.stdout, override, "override example")
	}
}

// --- Updating katana -------------------------------------------------------

// releaseServer serves one katana release from a local GitHub API stand-in.
func releaseServer(t *testing.T, tag string, binary []byte) *httptest.Server {
	t.Helper()

	name := fmt.Sprintf("katana_%s_%s_%s", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sum := sha256.Sum256(binary)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/repos/adaptive-scale/katana/releases/latest",
			"/repos/adaptive-scale/katana/releases/tags/" + tag:
			json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"html_url": "https://example.test/releases/" + tag,
				"assets": []map[string]string{
					{"name": name, "browser_download_url": srv.URL + "/assets/" + name},
					{"name": "checksums.txt", "browser_download_url": srv.URL + "/assets/checksums.txt"},
				},
			})
		case "/assets/" + name:
			w.Write(binary)
		case "/assets/checksums.txt":
			w.Write([]byte(checksums))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// updatableKatana copies this test binary somewhere disposable, so an update
// that replaces "the running binary" replaces the copy and not the suite.
func updatableKatana(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(cliBinary(t))
	if err != nil {
		t.Fatalf("copying the test binary: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "katana")
	if runtime.GOOS == "windows" {
		dest += ".exe"
	}
	if err := os.WriteFile(dest, body, 0o755); err != nil {
		t.Fatalf("copying the test binary: %v", err)
	}
	return dest
}

func TestUpdateReplacesTheRunningBinaryAndReportsTheTag(t *testing.T) {
	srv := releaseServer(t, "v2.0.0", []byte("the new katana"))
	dest := updatableKatana(t)

	res := runKatana(t, cliOptions{
		exe:     dest,
		version: "v1.0.0",
		env:     map[string]string{"KATANA_GITHUB_API": srv.URL},
	}, "update")

	wantExit(t, res, 0)
	// katana resolves symlinks before replacing itself, so the path it reports
	// is the resolved one — on macOS the temp directory lives under /private.
	resolved := dest
	if p, err := filepath.EvalSymlinks(dest); err == nil {
		resolved = p
	}
	wantContains(t, res.stdout, "updated katana to v2.0.0 ("+resolved+")", "report")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "the new katana" {
		t.Errorf("the installed binary is %q, want the released one", body)
	}
}

func TestCheckReportsAnAvailableReleaseWithoutInstallingIt(t *testing.T) {
	srv := releaseServer(t, "v2.0.0", []byte("the new katana"))
	dest := updatableKatana(t)

	res := runKatana(t, cliOptions{
		exe:     dest,
		version: "v1.0.0",
		env:     map[string]string{"KATANA_GITHUB_API": srv.URL},
	}, "update", "--check")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "katana v2.0.0 is available (you have v1.0.0)", "report")
	wantContains(t, res.stdout, "https://example.test/releases/v2.0.0", "release url")
	wantContains(t, res.stdout, "run `katana update` to install it", "advice")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "the new katana" {
		t.Error("--check installed the release")
	}
}

func TestCheckReportsAnUpToDateVersion(t *testing.T) {
	srv := releaseServer(t, "v2.0.0", []byte("the new katana"))

	res := runKatana(t, cliOptions{
		version: "v2.0.0",
		env:     map[string]string{"KATANA_GITHUB_API": srv.URL},
	}, "update", "--check")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "katana v2.0.0 is up to date", "report")
}

func TestWithoutCheckAnAlreadyCurrentVersionInstallsNothing(t *testing.T) {
	srv := releaseServer(t, "v2.0.0", []byte("the new katana"))
	dest := updatableKatana(t)

	res := runKatana(t, cliOptions{
		exe:     dest,
		version: "v2.0.0",
		env:     map[string]string{"KATANA_GITHUB_API": srv.URL},
	}, "update")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "katana v2.0.0 is already up to date", "report")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "the new katana" {
		t.Error("an already-current katana was reinstalled anyway")
	}
}

func TestForceReinstallsEvenWhenTheRunningVersionIsCurrent(t *testing.T) {
	srv := releaseServer(t, "v2.0.0", []byte("the new katana"))
	dest := updatableKatana(t)

	res := runKatana(t, cliOptions{
		exe:     dest,
		version: "v2.0.0",
		env:     map[string]string{"KATANA_GITHUB_API": srv.URL},
	}, "update", "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "updated katana to v2.0.0", "report")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "the new katana" {
		t.Errorf("--force did not reinstall, binary is %q", body)
	}
}

func TestAPinnedVersionIsInstalledEvenWhenItIsOlderThanTheRunningOne(t *testing.T) {
	srv := releaseServer(t, "v1.0.0", []byte("the older katana"))
	dest := updatableKatana(t)

	res := runKatana(t, cliOptions{
		exe:     dest,
		version: "v3.0.0",
		env:     map[string]string{"KATANA_GITHUB_API": srv.URL},
	}, "update", "--version", "v1.0.0")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "updated katana to v1.0.0", "report")
	wantAbsent(t, res.stdout, "already up to date", "the shortcut for an unpinned run")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "the older katana" {
		t.Errorf("the pinned release was not installed, binary is %q", body)
	}
}

func TestUpdateDocumentsTheBackgroundCheckAndTheTokens(t *testing.T) {
	res := runKatana(t, cliOptions{}, "update", "--help")

	for _, want := range []string{
		"checks for a new release once a day in the background",
		"KATANA_NO_UPDATE_CHECK=1",
		"already off in CI and for locally built binaries",
		"GITHUB_TOKEN",
		"KATANA_GITHUB_TOKEN",
	} {
		wantContains(t, res.stderr, want, "update usage")
	}
}

// --- Narrating a run with --verbose ----------------------------------------

func TestVerboseGenerateReportsTheSpecTargetStackAndHarness(t *testing.T) {
	root := cliProject(t, projectSpec{
		specs: map[string]string{"cart": "# cart\n\n## Rules\n\n- One.\n- Two.\n"},
	})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "spec     behaviors/cart.md (32 B, 6 lines)", "spec line")
	wantContains(t, res.stdout, "target   tests/cart_test.go (new file)", "target line")
	wantContains(t, res.stdout, "stack    go / go-test", "stack line")
	wantContains(t, res.stdout, "harness  "+cliBinary(t)+" -test.run=^TestKatanaAgentHelper$ (prompt via stdin)", "harness line")
}

func TestVerboseDescribesAnExistingTargetAsReplacing(t *testing.T) {
	root := cliProject(t, projectSpec{})
	cliWrite(t, root, "tests/cart_test.go", strings.Repeat("x", 42))

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose", "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "target   tests/cart_test.go (replacing 42 B)", "target line")
}

func TestVerboseShowsPerBehaviorExtraInstructionsAsTheirFirstLineOnly(t *testing.T) {
	root := cliProject(t, projectSpec{
		paths: []string{"behaviors/cart.md"},
	})
	cfg := cliRead(t, root, config.FileName)
	cfg += "    instructions: |\n      Stub the gateway.\n      Never call it for real.\n"
	cliWrite(t, root, config.FileName, cfg)

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	// A multi-line value is shown as its first line, marked with an ellipsis.
	wantContains(t, res.stdout, "extra    Stub the gateway. …", "extra line")
}

func TestAQuotedFirstLineIsTrimmedToOneHundredAndTwentyCharacters(t *testing.T) {
	long := strings.Repeat("a", 200)
	root := cliProject(t, projectSpec{paths: []string{"behaviors/cart.md"}})
	cfg := cliRead(t, root, config.FileName)
	cfg += "    instructions: " + strconv.Quote(long) + "\n"
	cliWrite(t, root, config.FileName, cfg)

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	for _, line := range strings.Split(res.stdout, "\n") {
		if !strings.HasPrefix(line, "  extra    ") {
			continue
		}
		if want := "  extra    " + strings.Repeat("a", 120) + "…"; line != want {
			t.Fatalf("extra line is %d characters:\n%s", len(line), line)
		}
		return
	}
	t.Errorf("no extra line was narrated:\n%s", res.stdout)
}

func TestVerboseDiscoverReportsTheUnitItsFilesTheTargetAndTheHarness(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go": billingSource,
		"internal/billing/refund.go": "package billing\n\nfunc Refund() {}\n",
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "source   internal/billing (2 go file(s), ", "source line")
	wantContains(t, res.stdout, "• internal/billing/charge.go", "file list")
	wantContains(t, res.stdout, "• internal/billing/refund.go", "file list")
	wantContains(t, res.stdout, "target   behaviors/internal/billing.md (new file)", "target line")
	wantContains(t, res.stdout, "harness  "+cliBinary(t)+" -test.run=^TestKatanaAgentHelper$ (prompt via stdin)", "harness line")
}

func TestVerboseDiscoverDescribesAnExistingTargetAsUpdating(t *testing.T) {
	root := discoverProject(t, projectSpec{files: map[string]string{
		"internal/billing/charge.go":    billingSource,
		"behaviors/internal/billing.md": strings.Repeat("x", 37),
	}})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--verbose", "--force")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "target   behaviors/internal/billing.md (updating 37 B)", "target line")
}

func TestVerbosePrintsThePromptWithItsSizeAndLineCount(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "prompt   ", "prompt line")
	wantContains(t, res.stdout, " lines\n", "prompt line count")
	wantContains(t, res.stdout, "│ You are generating an automated test suite", "prompt preview")
	wantContains(t, res.stdout, "running harness…", "progress")
}

func TestVerboseReportsTheWrittenFileAndTheTestCasesInIt(t *testing.T) {
	root := cliProject(t, projectSpec{})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		fmt.Sprintf("wrote    tests/cart_test.go (%d B, 7 lines, 2 test case(s))", len(agentTests)),
		"wrote line")
	// The same index is what goes into the tracker.
	for _, name := range []string{"TestOne", "TestTwo"} {
		wantContains(t, res.stdout, name, "test case list")
	}
	tr, err := tracker.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := tr.Get("behaviors/cart.md")
	if strings.Join(e.Tests, ",") != "TestOne,TestTwo" {
		t.Errorf("tracker index = %q, want the names that were narrated", e.Tests)
	}
}

func TestVerboseReportsTheSectionsAndStatementsOfADiscoveredFile(t *testing.T) {
	root := discoverProject(t, projectSpec{
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout,
		fmt.Sprintf("wrote    behaviors/internal/billing.md (%d B, 2 section(s), 2 statement(s))", len(agentSpecFile)),
		"wrote line")
}

// The fallback for a written behavior file katana cannot read back — reporting
// just its name and byte count — is not covered here. Discovery reads the same
// path through the same call a moment earlier and fails the unit outright when
// that read fails, so the fallback is only reachable if the file becomes
// unreadable between the two reads, which cannot be arranged from outside.

func TestAnythingTheHarnessSaidIsShownAsOneLine(t *testing.T) {
	root := discoverProject(t, projectSpec{
		files: map[string]string{"internal/billing/charge.go": billingSource},
	})

	res := runKatana(t, cliOptions{}, "discover", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "harness said: wrote behaviors/internal/billing.md …", "harness reply")
	wantAbsent(t, res.stdout, "harness said: wrote behaviors/internal/billing.md\nlet me know", "harness reply")
}

func TestPromptAndFilePreviewsAreCappedAtFortyLines(t *testing.T) {
	var spec strings.Builder
	spec.WriteString("# cart\n\n## Rules\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&spec, "- Rule number %d holds.\n", i)
	}
	root := cliProject(t, projectSpec{specs: map[string]string{"cart": spec.String()}})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

	wantExit(t, res, 0)
	wantContains(t, res.stdout, "… ", "preview remainder")
	wantContains(t, res.stdout, " more lines …", "preview remainder")
	// Forty lines of the prompt, no more.
	preview := 0
	for _, line := range strings.Split(res.stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "│") {
			preview++
		}
	}
	if preview != 40 {
		t.Errorf("the prompt preview is %d lines, want 40:\n%s", preview, res.stdout)
	}
}

func TestSizesAreShownInBytesKilobytesOrMegabytes(t *testing.T) {
	cases := []struct {
		name string
		size int
		want string
	}{
		{"below a kilobyte", 500, "500 B"},
		{"below a megabyte", 2048, "2.0 KB"},
		{"above a megabyte", 1024 * 1024 * 3 / 2, "1.5 MB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A behavior file of exactly the size under test.
			head := "# cart\n\n"
			body := head + strings.Repeat("a", c.size-len(head))
			root := cliProject(t, projectSpec{specs: map[string]string{"cart": body}})

			res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose")

			wantExit(t, res, 0)
			wantContains(t, res.stdout, "spec     behaviors/cart.md ("+c.want+", ", "spec size")
		})
	}
}

func TestWithMoreThanOneWorkerVerboseHarnessOutputStaysInThatItemsBlock(t *testing.T) {
	root := cliProject(t, projectSpec{
		agent:     agentSpec{stderr: "AGENT-NOISE"},
		behaviors: []string{"cart", "checkout"},
	})

	res := runKatana(t, cliOptions{}, "generate", "--dir", root, "--verbose", "--jobs", "2")

	wantExit(t, res, 0)
	// The harness's own live output went into the buffered block, which is
	// printed to katana's standard output, rather than straight to the terminal.
	if strings.Count(res.stdout, "AGENT-NOISE") != 2 {
		t.Errorf("the harness output did not land in each block:\n%s", res.stdout)
	}
	wantAbsent(t, res.stderr, "AGENT-NOISE", "standard error")
}
