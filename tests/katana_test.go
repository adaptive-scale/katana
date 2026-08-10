// This file covers behaviors/katana.md: the program's entry point — what it
// hands to the command, and what the process reports back to the shell.
//
// Exit status and the split between the standard output and error streams are
// only observable from outside the process, so every test here builds the real
// katana binary once and runs it as a child process. Nothing calls into
// internal packages: the specification describes what the shell sees.

package tests

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// katanaBin is the katana binary under test, built once by TestMain.
var (
	katanaBin      string
	katanaBuildErr error
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "katana-entrypoint-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot create a build directory: %v\n", err)
		os.Exit(1)
	}
	katanaBin, katanaBuildErr = buildKatana(dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// buildKatana compiles the katana command into dir and returns the path to it.
// It is built without -ldflags, so the version stays "dev" and the release
// check the CLI runs alongside a command stays quiet.
func buildKatana(dir string) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "katana")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command(goTool(), "build", "-o", bin, ".")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building katana in %s: %v\n%s", root, err, out)
	}
	return bin, nil
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod, which is where the katana command lives.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found above the test directory")
		}
		dir = parent
	}
}

// goTool finds the go command: PATH first, then the toolchain running the test.
func goTool() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// katanaRun is the outcome of one katana invocation as the shell sees it.
type katanaRun struct {
	exitCode int
	stdout   string
	stderr   string
}

// runKatana runs katana in dir with args and reports what the shell would see.
// The update check is switched off so the only thing on the error stream is
// what the entry point put there.
func runKatana(t *testing.T, dir string, args ...string) katanaRun {
	t.Helper()
	if katanaBuildErr != nil {
		t.Fatalf("katana could not be built: %v", katanaBuildErr)
	}

	var stdout, stderr strings.Builder
	cmd := exec.Command(katanaBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "KATANA_NO_UPDATE_CHECK=1")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		if exitErr.ExitCode() < 0 {
			t.Fatalf("katana %s did not exit normally: %v", strings.Join(args, " "), err)
		}
	default:
		t.Fatalf("katana %s could not be run: %v", strings.Join(args, " "), err)
	}
	return katanaRun{
		exitCode: cmd.ProcessState.ExitCode(),
		stdout:   stdout.String(),
		stderr:   stderr.String(),
	}
}

// runFailing invokes a command that cannot succeed: `status` in an empty
// directory, which has no project to report on. It is used wherever a failure
// is needed, because it writes nothing of its own to either stream, so the
// failure message is all that is there to inspect.
func runFailing(t *testing.T) katanaRun {
	t.Helper()
	got := runKatana(t, t.TempDir(), "status")
	if got.exitCode == 0 {
		t.Fatalf("`katana status` in an empty directory succeeded; this fixture needs a command that fails\nstdout: %q\nstderr: %q", got.stdout, got.stderr)
	}
	return got
}

// --- Running a command -----------------------------------------------------

// If the program name were passed through as well, it would be taken for the
// command to run and rejected as unknown. An invocation with nothing after the
// program name therefore has to behave as a no-argument one.
func TestTheProgramNameIsNotPassedToTheCommand(t *testing.T) {
	got := runKatana(t, t.TempDir())

	if got.exitCode != 0 {
		t.Errorf("katana with no arguments exited %d, want 0; the program name looks like it was passed as an argument\nstderr: %q",
			got.exitCode, got.stderr)
	}
	if strings.Contains(got.stdout+got.stderr, katanaBin) {
		t.Errorf("the program path %q reached the command\nstdout: %q\nstderr: %q", katanaBin, got.stdout, got.stderr)
	}
}

// Every word the user typed has to arrive, in order: the first one selects the
// command, and the rest are the command's own arguments. `status --dir <path>`
// names the directory it was pointed at, so a flag and its value surviving the
// hand-off is visible from outside.
func TestEverythingTypedAfterTheProgramNameReachesTheCommand(t *testing.T) {
	dir := t.TempDir()

	// Run from somewhere else, so the directory can only have come from the
	// argument rather than from the working directory.
	got := runKatana(t, os.TempDir(), "status", "--dir", dir)

	if got.exitCode == 0 {
		t.Fatalf("`katana status --dir %s` succeeded, want the empty directory reported as a failure", dir)
	}
	if !strings.Contains(got.stderr, dir) {
		t.Errorf("stderr = %q, want it to name the directory %q given after the command", got.stderr, dir)
	}
}

func TestSuccessfulCommandExitsSuccessfully(t *testing.T) {
	// `version` needs no project and touches nothing, so it is the plainest
	// command that succeeds.
	got := runKatana(t, t.TempDir(), "version")

	if got.exitCode != 0 {
		t.Errorf("`katana version` exited %d, want 0\nstderr: %q", got.exitCode, got.stderr)
	}
}

func TestSuccessfulCommandWritesNothingToTheErrorStream(t *testing.T) {
	got := runKatana(t, t.TempDir(), "version")

	if got.stderr != "" {
		t.Errorf("`katana version` wrote %q to the error stream, want nothing", got.stderr)
	}
}

// --- Reporting failures ----------------------------------------------------

func TestFailingCommandExitsWithStatusOne(t *testing.T) {
	got := runFailing(t)

	if got.exitCode != 1 {
		t.Errorf("exit status = %d, want 1\nstderr: %q", got.exitCode, got.stderr)
	}
}

func TestFailureMessageIsThePrefixTheErrorTextAndANewline(t *testing.T) {
	got := runFailing(t)

	// The specification fixes the envelope, not the wording of any particular
	// error, so the error text is only checked for being present and for being
	// the one line the prefix introduces.
	if !strings.HasPrefix(got.stderr, "katana: ") {
		t.Fatalf("stderr = %q, want it to start with %q", got.stderr, "katana: ")
	}
	if !strings.HasSuffix(got.stderr, "\n") {
		t.Errorf("stderr = %q, want it to end with a newline", got.stderr)
	}
	text := strings.TrimSuffix(strings.TrimPrefix(got.stderr, "katana: "), "\n")
	if strings.TrimSpace(text) == "" {
		t.Errorf("stderr = %q, want the error text after the prefix", got.stderr)
	}
	if strings.Contains(text, "\n") {
		t.Errorf("stderr = %q, want one message: the prefix, the error text and a newline", got.stderr)
	}
}

func TestFailureMessageGoesToTheErrorStream(t *testing.T) {
	got := runFailing(t)

	if !strings.Contains(got.stderr, "katana: ") {
		t.Errorf("the error stream holds %q, want the failure message on it", got.stderr)
	}
}

func TestFailureMessageIsNeverWrittenToTheStandardOutputStream(t *testing.T) {
	got := runFailing(t)

	if strings.Contains(got.stdout, "katana: ") {
		t.Errorf("the failure message reached the standard output stream: %q", got.stdout)
	}
}

// --- Asking for usage ------------------------------------------------------

// A command's own --help is the request the entry point has to recognise: the
// command reports it as a help request rather than as work it did.
func TestHelpRequestExitsWithStatusOne(t *testing.T) {
	got := runKatana(t, t.TempDir(), "generate", "--help")

	if got.exitCode != 1 {
		t.Errorf("`katana generate --help` exited %d, want 1\nstdout: %q\nstderr: %q", got.exitCode, got.stdout, got.stderr)
	}
}

func TestHelpRequestProducesNoFailureMessage(t *testing.T) {
	got := runKatana(t, t.TempDir(), "generate", "--help")

	// The usage text itself is the command's output; only a `katana: ` message
	// would be the entry point reporting a failure over the top of it.
	for _, line := range strings.Split(got.stdout+"\n"+got.stderr, "\n") {
		if strings.HasPrefix(line, "katana: ") {
			t.Errorf("a help request produced the failure message %q", line)
		}
	}
}
