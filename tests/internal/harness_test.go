// This file covers behaviors/internal/harness.md: which agent command katana
// shells out to, how the prompt reaches it, what environment and working
// directory it runs in, what one invocation reports back, and how a failed
// invocation is described to the user.
//
// A harness is an executable, so the only way to drive one from outside the
// package is to be one: the invocation tests re-run this test binary as the
// stand-in agent (see TestHarnessHelperProcess at the bottom), which reports
// the command line, working directory, prompt and environment it was given, or
// prints and exits however the case under test needs. The configuration tests
// need no process at all — they read the spec a Runner resolves to.

package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adaptive-scale/katana/internal/harness"
)

// builtinNames is the alphabetical list the specification names, used both as
// the expected listing and as the tail of the unknown-harness error.
var builtinNames = []string{"claude", "codex", "hermes", "opencode", "pi"}

// --- Built-in harnesses ----------------------------------------------------

func TestFiveHarnessesAreBuiltIn(t *testing.T) {
	got := harness.Names()

	if len(got) != 5 {
		t.Fatalf("Names() = %v, want the five built-in harnesses %v", got, builtinNames)
	}
	for _, want := range builtinNames {
		if _, ok := harness.Builtin(want); !ok {
			t.Errorf("harness %q is not built in", want)
		}
	}
}

func TestBuiltInHarnessNamesAreReturnedInAlphabeticalOrder(t *testing.T) {
	if got := harness.Names(); !sameStrings(got, builtinNames) {
		t.Errorf("Names() = %v, want %v", got, builtinNames)
	}
}

func TestTheDescriptiveListingReturnsTheFullBuiltInSpecsSortedByName(t *testing.T) {
	got := harness.Describe()

	if len(got) != len(builtinNames) {
		t.Fatalf("Describe() returned %d harnesses, want %d", len(got), len(builtinNames))
	}
	for i, name := range builtinNames {
		if got[i].Name != name {
			t.Errorf("Describe()[%d].Name = %q, want %q (sorted by name)", i, got[i].Name, name)
			continue
		}
		want, _ := harness.Builtin(name)
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("Describe()[%d] = %+v, want the full built-in spec %+v", i, got[i], want)
		}
	}
}

func TestTheClaudeHarnessRunsClaudeInPrintModeWithAutoPermissionsOverStdin(t *testing.T) {
	assertBuiltinSpec(t, harness.Spec{
		Name:      "claude",
		Command:   "claude",
		Args:      []string{"-p", "--permission-mode", "auto"},
		Prompt:    harness.PromptStdin,
		ModelFlag: "--model",
		Docs:      "Claude Code CLI, non-interactive print mode, auto permissions",
	})
}

func TestTheCodexHarnessRunsCodexExecWithAWriteSandboxAndThePromptOnStdin(t *testing.T) {
	assertBuiltinSpec(t, harness.Spec{
		Name:      "codex",
		Command:   "codex",
		Args:      []string{"exec", "--sandbox", "workspace-write"},
		Prompt:    harness.PromptStdin,
		ModelFlag: "--model",
		Docs:      "Codex CLI, non-interactive exec mode, workspace-write sandbox",
	})
}

func TestTheOpencodeHarnessRunsOpencodeRunWithThePromptAsAnArgument(t *testing.T) {
	assertBuiltinSpec(t, harness.Spec{
		Name:      "opencode",
		Command:   "opencode",
		Args:      []string{"run"},
		Prompt:    harness.PromptArg,
		ModelFlag: "--model",
		Docs:      "opencode CLI, single-shot run mode",
	})
}

func TestThePiHarnessRunsPiInPromptModeOverStdin(t *testing.T) {
	assertBuiltinSpec(t, harness.Spec{
		Name:      "pi",
		Command:   "pi",
		Args:      []string{"-p"},
		Prompt:    harness.PromptStdin,
		ModelFlag: "--model",
		Docs:      "pi CLI, non-interactive prompt mode",
	})
}

func TestTheHermesHarnessRunsHermesInPromptModeOverStdin(t *testing.T) {
	assertBuiltinSpec(t, harness.Spec{
		Name:      "hermes",
		Command:   "hermes",
		Args:      []string{"-p"},
		Prompt:    harness.PromptStdin,
		ModelFlag: "--model",
		Docs:      "hermes CLI, non-interactive prompt mode",
	})
}

func TestLookingUpABuiltInHarnessIgnoresWhitespaceAndLetterCase(t *testing.T) {
	want, _ := harness.Builtin("claude")

	for _, written := range []string{" CLAUDE ", "Claude", "\tclaude\n", "CLAUDE"} {
		got, ok := harness.Builtin(written)
		if !ok {
			t.Errorf("Builtin(%q) was not found, want the claude spec", written)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Builtin(%q) = %+v, want %+v", written, got, want)
		}
	}
}

func TestANameThatIsNotBuiltInIsReportedAsNotFound(t *testing.T) {
	for _, name := range []string{"not-a-harness", "", "clau", "claude-code"} {
		if spec, ok := harness.Builtin(name); ok {
			t.Errorf("Builtin(%q) reported found, resolving to %+v", name, spec)
		}
	}
}

// --- Choosing and configuring a harness ------------------------------------

func TestAKnownHarnessNameStartsFromItsBuiltInSpecWithOverridesAppliedOnTop(t *testing.T) {
	builtin, _ := harness.Builtin("codex")

	got := resolveSpec(t, "codex", harness.Spec{Command: "my-codex"})

	if got.Command != "my-codex" {
		t.Errorf("Command = %q, want the override %q", got.Command, "my-codex")
	}
	// Everything not overridden is still the built-in's.
	if got.Name != builtin.Name || !sameStrings(got.Args, builtin.Args) ||
		got.Prompt != builtin.Prompt || got.ModelFlag != builtin.ModelFlag || got.Docs != builtin.Docs {
		t.Errorf("resolved spec = %+v, want the rest of the built-in %+v", got, builtin)
	}
}

func TestAnUnknownHarnessNameIsAcceptedWhenAnExplicitCommandIsSupplied(t *testing.T) {
	got := resolveSpec(t, "my-agent", harness.Spec{Command: "/opt/bin/my-agent"})

	if got.Name != "my-agent" {
		t.Errorf("Name = %q, want %q", got.Name, "my-agent")
	}
	if got.Command != "/opt/bin/my-agent" {
		t.Errorf("Command = %q, want the supplied command", got.Command)
	}
	if len(got.Args) != 0 {
		t.Errorf("Args = %v, want no preset arguments", got.Args)
	}
	if got.Prompt != harness.PromptStdin {
		t.Errorf("Prompt = %q, want %q", got.Prompt, harness.PromptStdin)
	}
	if got.ModelFlag != "--model" {
		t.Errorf("ModelFlag = %q, want %q", got.ModelFlag, "--model")
	}
}

func TestAnUnknownHarnessNameWithNoExplicitCommandIsRejected(t *testing.T) {
	want := `unknown harness "my-agent"; built-in harnesses are claude, codex, hermes, opencode, pi ` +
		`(or set harness.command to use another agent CLI)`

	// A command that is only whitespace is no command at all.
	for _, command := range []string{"", "   ", "\t\n"} {
		runner, err := harness.New("my-agent", harness.Spec{Command: command}, harness.Options{})
		if err == nil {
			t.Errorf("command %q: New returned a runner for an unknown harness: %+v", command, runner.Spec())
			continue
		}
		if err.Error() != want {
			t.Errorf("command %q: error = %q, want %q", command, err.Error(), want)
		}
	}
}

func TestASuppliedCommandReplacesTheBuiltInExecutable(t *testing.T) {
	if got := resolveSpec(t, "claude", harness.Spec{Command: "claude-wrapper"}); got.Command != "claude-wrapper" {
		t.Errorf("Command = %q, want %q", got.Command, "claude-wrapper")
	}
}

func TestAnEmptyCommandLeavesTheBuiltInExecutableInPlace(t *testing.T) {
	if got := resolveSpec(t, "claude", harness.Spec{Command: ""}); got.Command != "claude" {
		t.Errorf("Command = %q, want the built-in %q", got.Command, "claude")
	}
}

func TestASuppliedArgumentListReplacesTheBuiltInArguments(t *testing.T) {
	want := []string{"exec", "--full-auto"}

	if got := resolveSpec(t, "claude", harness.Spec{Args: want}); !sameStrings(got.Args, want) {
		t.Errorf("Args = %v, want %v", got.Args, want)
	}
}

func TestAnEmptyButPresentArgumentListLeavesNoPresetArguments(t *testing.T) {
	got := resolveSpec(t, "claude", harness.Spec{Args: []string{}})

	if len(got.Args) != 0 {
		t.Errorf("Args = %v, want no preset arguments", got.Args)
	}
}

func TestOmittingTheArgumentListLeavesTheBuiltInArgumentsInPlace(t *testing.T) {
	builtin, _ := harness.Builtin("claude")

	got := resolveSpec(t, "claude", harness.Spec{Command: "claude-wrapper"})

	if !sameStrings(got.Args, builtin.Args) {
		t.Errorf("Args = %v, want the built-in %v", got.Args, builtin.Args)
	}
}

func TestASuppliedPromptDeliveryModeReplacesTheBuiltInOne(t *testing.T) {
	// claude delivers on stdin by default.
	if got := resolveSpec(t, "claude", harness.Spec{Prompt: harness.PromptArg}); got.Prompt != harness.PromptArg {
		t.Errorf("Prompt = %q, want %q", got.Prompt, harness.PromptArg)
	}
}

func TestAnEmptyPromptDeliveryModeLeavesTheBuiltInModeInPlace(t *testing.T) {
	// codex delivers over stdin by default.
	if got := resolveSpec(t, "codex", harness.Spec{Prompt: ""}); got.Prompt != harness.PromptStdin {
		t.Errorf("Prompt = %q, want the built-in %q", got.Prompt, harness.PromptStdin)
	}
}

func TestASuppliedModelFlagReplacesTheBuiltInOne(t *testing.T) {
	if got := resolveSpec(t, "claude", harness.Spec{ModelFlag: "-m"}); got.ModelFlag != "-m" {
		t.Errorf("ModelFlag = %q, want %q", got.ModelFlag, "-m")
	}
}

func TestAnEmptyModelFlagLeavesTheBuiltInFlagInPlace(t *testing.T) {
	if got := resolveSpec(t, "claude", harness.Spec{ModelFlag: ""}); got.ModelFlag != "--model" {
		t.Errorf("ModelFlag = %q, want the built-in %q", got.ModelFlag, "--model")
	}
}

func TestTheResolvedSpecificationIsReadableForDiagnostics(t *testing.T) {
	want := harness.Spec{
		Name:      "codex",
		Command:   "my-codex",
		Args:      []string{"exec", "--full-auto"},
		Prompt:    harness.PromptStdin,
		ModelFlag: "-m",
		Docs:      "Codex CLI, non-interactive exec mode, workspace-write sandbox",
	}

	got := resolveSpec(t, "codex", harness.Spec{
		Command:   want.Command,
		Args:      want.Args,
		Prompt:    want.Prompt,
		ModelFlag: want.ModelFlag,
	})

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Spec() = %+v, want %+v", got, want)
	}
}

// --- Defaults applied at configuration time --------------------------------

func TestAnUnsetZeroOrNegativeTimeoutBoundsAnInvocationAtTenMinutes(t *testing.T) {
	// An unset timeout and a zero one are the same value in Go, so the cases
	// that can differ are zero and negative.
	for _, timeout := range []time.Duration{0, -time.Second, -10 * time.Minute} {
		runner, err := harness.New("claude", harness.Spec{}, harness.Options{Timeout: timeout})
		if err != nil {
			t.Fatalf("harness.New: %v", err)
		}
		if got := configuredTimeout(t, runner); got != 10*time.Minute {
			t.Errorf("timeout %s produced a bound of %s, want 10m0s", timeout, got)
		}
	}
}

func TestHarnessDiagnosticsGoToStandardErrorWhenNoWriterIsGiven(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = pw
	t.Cleanup(func() { os.Stderr = saved })

	// The runner takes its diagnostics writer as it is built, so the standard
	// error it should fall back to has to be in place first.
	agent := standInAgent{stdout: "streamed to the default writer\n"}
	runner, err := harness.New(standInName, standInSpec(t), harness.Options{
		Dir:     t.TempDir(),
		Verbose: true,
		Env:     agent.env(),
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	if _, err := runner.Run(context.Background(), agentPrompt); err != nil {
		t.Fatalf("Run: %v", err)
	}

	os.Stderr = saved
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	streamed, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	pr.Close()

	if !strings.Contains(string(streamed), "streamed to the default writer") {
		t.Errorf("harness diagnostics did not reach standard error; it received %q", streamed)
	}
}

// --- Redirecting verbose output --------------------------------------------

func TestAHarnessCanBeCopiedWithADifferentDiagnosticsWriterLeavingTheOriginal(t *testing.T) {
	original, redirected := &syncBuffer{}, &syncBuffer{}
	agent := standInAgent{stdout: "agent output\n"}
	runner := newStandInRunner(t, agent, standInSpec(t), harness.Options{Verbose: true, Stderr: original})

	mustRunHarness(t, runner.WithStderr(redirected), agentPrompt)

	if !strings.Contains(redirected.String(), "agent output") {
		t.Errorf("the copy's writer received %q, want the harness output", redirected.String())
	}
	if original.String() != "" {
		t.Errorf("the original's writer received %q, want nothing", original.String())
	}

	// The original still works, and still writes where it always did.
	mustRunHarness(t, runner, agentPrompt)
	if !strings.Contains(original.String(), "agent output") {
		t.Errorf("the original's writer received %q after its own run, want the harness output", original.String())
	}
}

// --- Checking the executable is present ------------------------------------

func TestAvailabilitySucceedsWhenTheExecutableIsFoundOnThePath(t *testing.T) {
	t.Setenv("PATH", installExecutable(t, "my-agent"))

	runner, err := harness.New("my-agent", harness.Spec{Command: "my-agent"}, harness.Options{})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}

	if err := runner.Available(); err != nil {
		t.Errorf("Available() = %v, want no error for an executable on PATH", err)
	}
}

func TestAvailabilityReportsAnExecutableThatIsNotOnThePath(t *testing.T) {
	t.Setenv("PATH", "")
	runner := missingRunner(t)

	err := runner.Available()

	if err == nil {
		t.Fatal("Available() = nil, want an error for a command that is not on PATH")
	}
	want := fmt.Sprintf("harness %q needs %q on your PATH but it was not found: ", standInName, missingCommand)
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), want)
	}
	// The message ends in the lookup failure itself, which stays inspectable.
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error %v does not carry the underlying lookup failure", err)
	}
}

func TestRunningAHarnessChecksAvailabilityFirstAndProducesNoResult(t *testing.T) {
	t.Setenv("PATH", "")
	runner := missingRunner(t)

	res, err := runner.Run(context.Background(), agentPrompt)

	if err == nil {
		t.Fatal("Run() = nil error, want the not-found error before any process starts")
	}
	if res != nil {
		t.Errorf("Run() returned a result %+v, want none", res)
	}
	if want := runner.Available().Error(); err.Error() != want {
		t.Errorf("error = %q, want the availability error %q", err.Error(), want)
	}
}

// --- Invoking the harness --------------------------------------------------

func TestTheCommandLineIsTheConfiguredArgumentsInOrder(t *testing.T) {
	want := []string{"alpha", "--beta", "gamma"}

	rep := invoke(t, standInSpec(t, want...), harness.Options{}, agentPrompt)

	if !sameStrings(rep.Args, want) {
		t.Errorf("command line = %v, want %v", rep.Args, want)
	}
}

func TestAModelOverrideAppendsTheModelFlagAndItsValueAfterTheArguments(t *testing.T) {
	spec := standInSpec(t, "alpha")
	spec.ModelFlag = "-m"

	rep := invoke(t, spec, harness.Options{Model: "some-model"}, agentPrompt)

	want := []string{"alpha", "-m", "some-model"}
	if !sameStrings(rep.Args, want) {
		t.Errorf("command line = %v, want %v", rep.Args, want)
	}
}

func TestAModelOverrideAddsNoArgumentsWhenTheHarnessHasNoModelFlag(t *testing.T) {
	// No configuration reaches a harness without a model flag today: every
	// built-in defines --model, an empty override keeps it, and an unknown
	// name is given --model too. The rule that governs the case is still
	// checkable — the only model arguments are the resolved spec's own flag
	// and value, so a spec carrying no flag contributes nothing.
	spec := standInSpec(t, "alpha")
	runner := newStandInRunner(t, standInAgent{report: true}, spec, harness.Options{Model: "some-model"})

	rep := reportFrom(t, runner, agentPrompt)

	want := []string{"alpha"}
	if flag := runner.Spec().ModelFlag; flag != "" {
		want = append(want, flag, "some-model")
	}
	if !sameStrings(rep.Args, want) {
		t.Errorf("command line = %v, want %v", rep.Args, want)
	}
}

func TestNoModelOverrideAddsNoModelArgumentsEvenWithAModelFlag(t *testing.T) {
	spec := standInSpec(t, "alpha")
	spec.ModelFlag = "-m"

	rep := invoke(t, spec, harness.Options{Model: ""}, agentPrompt)

	if want := []string{"alpha"}; !sameStrings(rep.Args, want) {
		t.Errorf("command line = %v, want %v", rep.Args, want)
	}
}

func TestInPromptAsArgumentModeThePromptIsTheLastArgumentAfterTheModel(t *testing.T) {
	spec := standInSpec(t, "alpha")
	spec.Prompt = harness.PromptArg
	spec.ModelFlag = "-m"

	rep := invoke(t, spec, harness.Options{Model: "some-model"}, agentPrompt)

	want := []string{"alpha", "-m", "some-model", agentPrompt}
	if !sameStrings(rep.Args, want) {
		t.Errorf("command line = %v, want %v", rep.Args, want)
	}
	if rep.Stdin != "" {
		t.Errorf("standard input carried %q, want nothing", rep.Stdin)
	}
}

func TestInPromptOnStandardInputModeThePromptIsWrittenToStandardInput(t *testing.T) {
	spec := standInSpec(t, "alpha")
	spec.Prompt = harness.PromptStdin

	rep := invoke(t, spec, harness.Options{}, agentPrompt)

	if rep.Stdin != agentPrompt {
		t.Errorf("standard input carried %q, want the prompt %q", rep.Stdin, agentPrompt)
	}
	if want := []string{"alpha"}; !sameStrings(rep.Args, want) {
		t.Errorf("command line = %v, want %v — the prompt must not appear on it", rep.Args, want)
	}
}

func TestTheProcessRunsInTheConfiguredWorkingDirectory(t *testing.T) {
	dir := t.TempDir()

	rep := invoke(t, standInSpec(t), harness.Options{Dir: dir}, agentPrompt)

	if got, want := realPath(t, rep.Dir), realPath(t, dir); got != want {
		t.Errorf("the harness ran in %q, want %q", got, want)
	}
}

func TestTheInvocationIsAbandonedOnceTheTimeoutElapses(t *testing.T) {
	timeout := 150 * time.Millisecond
	// The agent sleeps far longer than the bound, so a run that returns
	// promptly can only be one that was abandoned.
	agent := standInAgent{sleep: 30 * time.Second}
	runner := newStandInRunner(t, agent, standInSpec(t), harness.Options{Timeout: timeout})

	start := time.Now()
	if _, err := runner.Run(context.Background(), agentPrompt); err == nil {
		t.Fatal("Run() = nil error, want the invocation to be abandoned")
	}

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Run() took %s, want it abandoned near the %s bound", elapsed, timeout)
	}
}

// --- Environment passed to the harness -------------------------------------

func TestWithNoExtraEntriesTheHarnessInheritsTheParentEnvironmentUnchanged(t *testing.T) {
	// With nothing configured, the only way to reach the stand-in agent is
	// through the environment it inherits.
	t.Setenv(agentEnvOn, "1")
	t.Setenv(agentEnvReport, "1")
	t.Setenv(agentEnvExit, "0")
	t.Setenv("KATANA_HARNESS_INHERITED", "from the parent")

	dir := t.TempDir()
	runner, err := harness.New(standInName, standInSpec(t), harness.Options{
		Dir:    dir,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	rep := reportFrom(t, runner, agentPrompt)

	got, want := append([]string(nil), rep.Env...), os.Environ()
	// os/exec points the harness's PWD at its working directory, so that one
	// entry is expected to differ from the parent's.
	for i, kv := range want {
		if strings.HasPrefix(kv, "PWD=") {
			want[i] = "PWD=" + dir
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !sameStrings(got, want) {
		t.Errorf("the harness environment differs from the parent's:\n got %v\nwant %v", got, want)
	}
}

func TestExtraEntriesAreAddedToTheParentEnvironmentAndTakeEffectOverIt(t *testing.T) {
	t.Setenv("KATANA_HARNESS_INHERITED", "from the parent")
	t.Setenv("KATANA_HARNESS_SHADOWED", "from the parent")

	rep := invoke(t, standInSpec(t), harness.Options{Env: map[string]string{
		"KATANA_HARNESS_SHADOWED": "from the configuration",
		"KATANA_HARNESS_EXTRA":    "added",
	}}, agentPrompt)

	for _, tc := range []struct{ name, want string }{
		{"KATANA_HARNESS_INHERITED", "from the parent"},
		{"KATANA_HARNESS_SHADOWED", "from the configuration"},
		{"KATANA_HARNESS_EXTRA", "added"},
	} {
		got, ok := envValue(rep.Env, tc.name)
		if !ok {
			t.Errorf("%s is missing from the harness environment", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestExtraEnvironmentEntriesAreAddedInAlphabeticalOrderOfTheirNames(t *testing.T) {
	rep := invoke(t, standInSpec(t), harness.Options{Env: map[string]string{
		"KATANA_HARNESS_ORDER_C": "3",
		"KATANA_HARNESS_ORDER_A": "1",
		"KATANA_HARNESS_ORDER_B": "2",
	}}, agentPrompt)

	got := envNames(rep.Env, "KATANA_HARNESS_ORDER_")
	want := []string{"KATANA_HARNESS_ORDER_A", "KATANA_HARNESS_ORDER_B", "KATANA_HARNESS_ORDER_C"}
	if !sameStrings(got, want) {
		t.Errorf("extra entries were added as %v, want %v", got, want)
	}
}

// --- What an invocation reports ---------------------------------------------

func TestASuccessfulInvocationReturnsItsOutputStreamsAndHowLongItTook(t *testing.T) {
	agent := standInAgent{stdout: "wrote the tests\n", stderr: "a note on progress\n", sleep: 50 * time.Millisecond}

	res := mustRunHarness(t, newStandInRunner(t, agent, standInSpec(t), harness.Options{}), agentPrompt)

	if res.Stdout != agent.stdout {
		t.Errorf("Stdout = %q, want %q", res.Stdout, agent.stdout)
	}
	if res.Stderr != agent.stderr {
		t.Errorf("Stderr = %q, want %q", res.Stderr, agent.stderr)
	}
	if res.Duration < agent.sleep {
		t.Errorf("Duration = %s, want at least the %s the harness took", res.Duration, agent.sleep)
	}
}

func TestTheCapturedOutputIsReturnedAlongsideTheErrorWhenTheInvocationFails(t *testing.T) {
	agent := standInAgent{stdout: "half a test file\n", stderr: "it went wrong\n", exit: 4}

	res, err := newStandInRunner(t, agent, standInSpec(t), harness.Options{}).Run(context.Background(), agentPrompt)

	if err == nil {
		t.Fatal("Run() = nil error, want the failure to be reported")
	}
	if res == nil {
		t.Fatal("Run() returned no result, want the captured output alongside the error")
	}
	if res.Stdout != agent.stdout {
		t.Errorf("Stdout = %q, want %q", res.Stdout, agent.stdout)
	}
	if res.Stderr != agent.stderr {
		t.Errorf("Stderr = %q, want %q", res.Stderr, agent.stderr)
	}
}

func TestInVerboseModeBothStreamsAreAlsoStreamedToTheDiagnosticsWriter(t *testing.T) {
	var streamed syncBuffer
	agent := standInAgent{stdout: "on standard output\n", stderr: "on standard error\n"}

	res := mustRunHarness(t, newStandInRunner(t, agent, standInSpec(t), harness.Options{
		Verbose: true,
		Stderr:  &streamed,
	}), agentPrompt)

	for _, want := range []string{"on standard output", "on standard error"} {
		if !strings.Contains(streamed.String(), want) {
			t.Errorf("the diagnostics writer received %q, want it to carry %q", streamed.String(), want)
		}
	}
	// Streaming does not replace capturing.
	if res.Stdout != agent.stdout || res.Stderr != agent.stderr {
		t.Errorf("captured output = %q / %q, want %q / %q", res.Stdout, res.Stderr, agent.stdout, agent.stderr)
	}
}

func TestInNonVerboseModeNothingIsStreamedAndOutputIsOnlyCaptured(t *testing.T) {
	var streamed syncBuffer
	agent := standInAgent{stdout: "on standard output\n", stderr: "on standard error\n"}

	res := mustRunHarness(t, newStandInRunner(t, agent, standInSpec(t), harness.Options{
		Verbose: false,
		Stderr:  &streamed,
	}), agentPrompt)

	if streamed.String() != "" {
		t.Errorf("the diagnostics writer received %q, want nothing", streamed.String())
	}
	if res.Stdout != agent.stdout || res.Stderr != agent.stderr {
		t.Errorf("captured output = %q / %q, want %q / %q", res.Stdout, res.Stderr, agent.stdout, agent.stderr)
	}
}

// --- Failure reporting -------------------------------------------------------

func TestExceedingTheTimeLimitIsReportedAsATimeout(t *testing.T) {
	timeout := 150 * time.Millisecond
	agent := standInAgent{sleep: 30 * time.Second}
	runner := newStandInRunner(t, agent, standInSpec(t), harness.Options{Timeout: timeout})

	_, err := runner.Run(context.Background(), agentPrompt)

	if err == nil {
		t.Fatal("Run() = nil error, want a timeout")
	}
	want := fmt.Sprintf("harness %q timed out after %s (raise harness.timeout in katana.yaml)", standInName, timeout)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestANonZeroExitIsRecordedWithItsStatusAndReportedWithTheStderrExcerpt(t *testing.T) {
	agent := standInAgent{stderr: "the agent gave up\n", exit: 7}

	res, err := newStandInRunner(t, agent, standInSpec(t), harness.Options{}).Run(context.Background(), agentPrompt)

	if err == nil {
		t.Fatal("Run() = nil error, want the non-zero exit to be reported")
	}
	if res == nil {
		t.Fatal("Run() returned no result, want the exit code recorded")
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
	want := fmt.Sprintf("harness %q exited with status 7: the agent gave up", standInName)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestAnyOtherFailureToRunTheProcessIsReportedAsRunningTheHarness(t *testing.T) {
	// The executable is there, so availability passes; the process cannot
	// start because its working directory does not exist.
	missingDir := filepath.Join(t.TempDir(), "not-created")
	runner := newStandInRunner(t, standInAgent{}, standInSpec(t), harness.Options{Dir: missingDir})

	_, err := runner.Run(context.Background(), agentPrompt)

	if err == nil {
		t.Fatal("Run() = nil error, want the failure to start to be reported")
	}
	want := fmt.Sprintf("running harness %q: ", standInName)
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want it to start with %q", err.Error(), want)
	}
	if strings.TrimSpace(strings.TrimPrefix(err.Error(), want)) == "" {
		t.Errorf("error = %q, want the underlying cause after the prefix", err.Error())
	}
}

func TestTheRecordedExitCodeStaysZeroForTimeoutsAndForFailuresThatAreNotAnExit(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		runner := newStandInRunner(t, standInAgent{sleep: 30 * time.Second}, standInSpec(t),
			harness.Options{Timeout: 150 * time.Millisecond})

		res, err := runner.Run(context.Background(), agentPrompt)

		if err == nil {
			t.Fatal("Run() = nil error, want a timeout")
		}
		if res == nil {
			t.Fatal("Run() returned no result, want one carrying the exit code")
		}
		if res.ExitCode != 0 {
			t.Errorf("ExitCode = %d after a timeout, want 0", res.ExitCode)
		}
	})

	t.Run("failure to start", func(t *testing.T) {
		missingDir := filepath.Join(t.TempDir(), "not-created")
		runner := newStandInRunner(t, standInAgent{exit: 9}, standInSpec(t), harness.Options{Dir: missingDir})

		res, err := runner.Run(context.Background(), agentPrompt)

		if err == nil {
			t.Fatal("Run() = nil error, want the failure to start to be reported")
		}
		if res == nil {
			t.Fatal("Run() returned no result, want one carrying the exit code")
		}
		if res.ExitCode != 0 {
			t.Errorf("ExitCode = %d for a failure that is not a process exit, want 0", res.ExitCode)
		}
	})
}

// --- The standard-error excerpt in exit errors -------------------------------

func TestTheStderrExcerptIsStrippedOfSurroundingWhitespace(t *testing.T) {
	if got := exitErrorExcerpt(t, "\n\n   the agent gave up   \n\t\n"); got != "the agent gave up" {
		t.Errorf("excerpt = %q, want %q", got, "the agent gave up")
	}
}

func TestStderrThatIsEmptyOrOnlyWhitespaceIsReportedAsNoStderrOutput(t *testing.T) {
	for _, stderr := range []string{"", "   ", "\n\n", " \t\n "} {
		if got := exitErrorExcerpt(t, stderr); got != "(no stderr output)" {
			t.Errorf("stderr %q: excerpt = %q, want %q", stderr, got, "(no stderr output)")
		}
	}
}

func TestStderrOfTenLinesOrFewerIsIncludedInFull(t *testing.T) {
	for _, n := range []int{1, 9, 10} {
		want := numberedLines(n)

		if got := exitErrorExcerpt(t, want); got != want {
			t.Errorf("%d lines: excerpt = %q, want it in full: %q", n, got, want)
		}
	}
}

func TestStderrOfMoreThanTenLinesIsTruncatedToTenLinesAndAnEllipsis(t *testing.T) {
	got := exitErrorExcerpt(t, numberedLines(14))

	want := numberedLines(10) + "\n  ..."
	if got != want {
		t.Errorf("excerpt = %q, want %q", got, want)
	}
}

// --- Permission-denial hint ---------------------------------------------------

func TestAHintIsProducedForEachDenialMarkerInEitherStreamIgnoringCase(t *testing.T) {
	markers := []string{"denied", "not allowed", "no write access", "read-only", "grant write", "without permission"}

	for _, marker := range markers {
		capitalized := strings.ToUpper(marker[:1]) + marker[1:]
		for _, written := range []string{marker, strings.ToUpper(marker), capitalized} {
			output := "the agent said: " + written + " — nothing was saved"

			if hint := harness.PermissionHint("codex", output, ""); hint == "" {
				t.Errorf("no hint for %q on standard output", written)
			}
			if hint := harness.PermissionHint("codex", "", output); hint == "" {
				t.Errorf("no hint for %q on standard error", written)
			}
		}
	}
}

func TestNoHintIsProducedWhenNoDenialMarkerAppears(t *testing.T) {
	// The bare word "permission" must not trigger one: harnesses echo back the
	// permission flag katana puts on their command line.
	for _, output := range []string{
		"",
		"running with -p --permission-mode auto",
		"permission",
		"wrote tests/checkout_test.go",
	} {
		if hint := harness.PermissionHint("codex", output, ""); hint != "" {
			t.Errorf("stdout %q produced the hint %q, want none", output, hint)
		}
		if hint := harness.PermissionHint("codex", "", output); hint != "" {
			t.Errorf("stderr %q produced the hint %q, want none", output, hint)
		}
	}
}

func TestTheGenericHintPointsAtHarnessArgs(t *testing.T) {
	want := "hint: the harness looks like it was denied file-write permission; " +
		"grant it write access to the output path, e.g. via harness.args in katana.yaml"

	for _, name := range []string{"opencode", "pi", "hermes", "my-agent"} {
		if got := harness.PermissionHint(name, "write access denied", ""); got != want {
			t.Errorf("harness %q: hint = %q, want %q", name, got, want)
		}
	}
}

func TestTheHintForTheCodexHarnessPointsAtTheSandboxArguments(t *testing.T) {
	want := "hint: the harness looks like it was denied file-write permission; " +
		`run it with a writable sandbox, e.g. harness.args: ["exec", "--sandbox", "workspace-write"] in katana.yaml`

	if got := harness.PermissionHint("codex", "write access denied", ""); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
}

func TestTheHintForTheClaudeHarnessPointsAtThePermissionModeArguments(t *testing.T) {
	want := "hint: the harness looks like it was denied file-write permission; " +
		`run it with write access, e.g. harness.args: ["-p", "--permission-mode", "auto"] in katana.yaml`

	if got := harness.PermissionHint("claude", "write access denied", ""); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
}

// --- The stand-in agent --------------------------------------------------------

const (
	agentEnvOn     = "KATANA_HARNESS_AGENT"
	agentEnvReport = "KATANA_HARNESS_AGENT_REPORT"
	agentEnvStdout = "KATANA_HARNESS_AGENT_STDOUT"
	agentEnvStderr = "KATANA_HARNESS_AGENT_STDERR"
	agentEnvSleep  = "KATANA_HARNESS_AGENT_SLEEP_MS"
	agentEnvExit   = "KATANA_HARNESS_AGENT_EXIT"
)

const (
	// standInName is the harness name katana reports in its errors.
	standInName = "stand-in-agent"
	// agentPrompt stands in for the generation prompt katana hands an agent.
	agentPrompt = "Write tests for behaviors/checkout.md"
	// missingCommand is a command no machine has on its PATH.
	missingCommand = "katana-no-such-agent"
	// argsTerminator separates the arguments the test binary needs from the
	// ones the harness put on the command line. The flag package stops at it,
	// so a harness argument that looks like a flag is not parsed as one.
	argsTerminator = "--"
)

// TestHarnessHelperProcess is not a test of its own: it is the coding agent
// every invocation test runs. A harness is an executable, so the only way to
// drive one from outside the package is to be one — this test binary re-runs
// itself with the environment below describing what the agent should do.
func TestHarnessHelperProcess(t *testing.T) {
	if os.Getenv(agentEnvOn) != "1" {
		t.Skip("the stand-in agent, run only as a child process of a harness test")
	}
	// The prompt arrives on stdin in stdin mode, and not at all in argument
	// mode; a real agent reads it either way.
	prompt, _ := io.ReadAll(os.Stdin)

	if ms, err := strconv.Atoi(os.Getenv(agentEnvSleep)); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}

	if os.Getenv(agentEnvReport) == "1" {
		dir, err := os.Getwd()
		if err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(90)
		}
		body, err := json.Marshal(agentReport{
			Args:  argsAfterTerminator(os.Args[1:]),
			Dir:   dir,
			Stdin: string(prompt),
			Env:   os.Environ(),
		})
		if err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(91)
		}
		os.Stdout.Write(body)
	} else {
		os.Stdout.WriteString(os.Getenv(agentEnvStdout))
		os.Stderr.WriteString(os.Getenv(agentEnvStderr))
	}

	code, _ := strconv.Atoi(os.Getenv(agentEnvExit))
	os.Exit(code) // exit before the testing package prints its own report
}

// standInAgent describes what the stand-in agent does for one invocation.
// Everything an invocation can observe about a harness — what it printed on
// each stream, how long it took and how it exited — is a field here.
type standInAgent struct {
	stdout string        // printed on standard output
	stderr string        // printed on standard error
	sleep  time.Duration // how long it works before answering
	exit   int           // exit status
	report bool          // print a report of the invocation instead of the streams
}

func (a standInAgent) env() map[string]string {
	env := map[string]string{
		agentEnvOn:     "1",
		agentEnvStdout: a.stdout,
		agentEnvStderr: a.stderr,
		agentEnvSleep:  strconv.Itoa(int(a.sleep / time.Millisecond)),
		agentEnvExit:   strconv.Itoa(a.exit),
	}
	if a.report {
		env[agentEnvReport] = "1"
	}
	return env
}

// agentReport is what the stand-in agent saw of its own invocation.
type agentReport struct {
	Args  []string `json:"args"`
	Dir   string   `json:"dir"`
	Stdin string   `json:"stdin"`
	Env   []string `json:"env"`
}

// standInSpec points a harness at the test binary re-running itself as the
// stand-in agent. extra are the harness's own arguments: they follow the
// terminator, so they are exactly what an invocation test asserts on.
func standInSpec(t *testing.T, extra ...string) harness.Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot find the test binary to use as a harness: %v", err)
	}
	args := append([]string{"-test.run=^TestHarnessHelperProcess$", argsTerminator}, extra...)
	return harness.Spec{Command: exe, Args: args}
}

// newStandInRunner builds a runner for the stand-in agent, filling in the
// options a test does not care about: a temp working directory and a quiet
// diagnostics writer.
func newStandInRunner(t *testing.T, a standInAgent, spec harness.Spec, opts harness.Options) *harness.Runner {
	t.Helper()
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	for k, v := range a.env() {
		opts.Env[k] = v
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}
	runner, err := harness.New(standInName, spec, opts)
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	return runner
}

// missingRunner is a harness whose executable is not installed anywhere.
func missingRunner(t *testing.T) *harness.Runner {
	t.Helper()
	runner, err := harness.New(standInName, harness.Spec{Command: missingCommand}, harness.Options{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	return runner
}

// invoke runs the stand-in agent in report mode and returns what it saw.
func invoke(t *testing.T, spec harness.Spec, opts harness.Options, prompt string) agentReport {
	t.Helper()
	return reportFrom(t, newStandInRunner(t, standInAgent{report: true}, spec, opts), prompt)
}

func reportFrom(t *testing.T, runner *harness.Runner, prompt string) agentReport {
	t.Helper()
	res := mustRunHarness(t, runner, prompt)
	var rep agentReport
	if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
		t.Fatalf("the stand-in agent did not report its invocation: %v\nstdout: %q", err, res.Stdout)
	}
	return rep
}

func mustRunHarness(t *testing.T, runner *harness.Runner, prompt string) *harness.Result {
	t.Helper()
	res, err := runner.Run(context.Background(), prompt)
	if err != nil {
		if res != nil {
			t.Fatalf("Run: %v\nharness stderr: %s", err, res.Stderr)
		}
		t.Fatalf("Run: %v", err)
	}
	return res
}

// exitErrorExcerpt runs an agent that fails after printing stderr, and returns
// the excerpt the resulting error carried. The excerpt is only observable
// through that message.
func exitErrorExcerpt(t *testing.T, stderr string) string {
	t.Helper()
	runner := newStandInRunner(t, standInAgent{stderr: stderr, exit: 1}, standInSpec(t), harness.Options{})

	_, err := runner.Run(context.Background(), agentPrompt)
	if err == nil {
		t.Fatal("Run() = nil error, want the non-zero exit to be reported")
	}
	prefix := fmt.Sprintf("harness %q exited with status 1: ", standInName)
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Fatalf("error = %q, want it to start with %q", err.Error(), prefix)
	}
	return strings.TrimPrefix(err.Error(), prefix)
}

// --- Assertions and small helpers ----------------------------------------------

func assertBuiltinSpec(t *testing.T, want harness.Spec) {
	t.Helper()
	got, ok := harness.Builtin(want.Name)
	if !ok {
		t.Fatalf("harness %q is not built in; the built-ins are %v", want.Name, harness.Names())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Builtin(%q) = %+v, want %+v", want.Name, got, want)
	}
}

// resolveSpec is the spec a runner ends up with for a name and its overrides.
func resolveSpec(t *testing.T, name string, override harness.Spec) harness.Spec {
	t.Helper()
	runner, err := harness.New(name, override, harness.Options{})
	if err != nil {
		t.Fatalf("harness.New(%q): %v", name, err)
	}
	return runner.Spec()
}

// configuredTimeout is the bound a runner applies to one invocation. The bound
// is otherwise only observable by waiting it out, which a test cannot afford,
// so this reads it off the runner's own options.
func configuredTimeout(t *testing.T, runner *harness.Runner) time.Duration {
	t.Helper()
	opts := reflect.ValueOf(runner).Elem().FieldByName("opts")
	if !opts.IsValid() {
		t.Fatal("a Runner no longer keeps its options in a field named opts")
	}
	timeout := opts.FieldByName("Timeout")
	if !timeout.IsValid() {
		t.Fatal("a Runner's options no longer carry a Timeout")
	}
	return time.Duration(timeout.Int())
}

// installExecutable writes an executable named name into a directory of its
// own and returns that directory, for tests about PATH lookup. Availability
// never runs the file, so its contents only have to make it a plausible one.
func installExecutable(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in executable is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// argsAfterTerminator drops the arguments the test binary needs for itself,
// leaving the command line the harness built.
func argsAfterTerminator(args []string) []string {
	for i, a := range args {
		if a == argsTerminator {
			return args[i+1:]
		}
	}
	return nil
}

// envValue is the value a process ends up with for name: the last entry wins,
// which is how a process reads its own environment.
func envValue(env []string, name string) (string, bool) {
	value, found := "", false
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == name {
			value, found = v, true
		}
	}
	return value, found
}

// envNames lists the names of the entries starting with prefix, in the order
// the process received them.
func envNames(env []string, prefix string) []string {
	var names []string
	for _, entry := range env {
		if k, _, ok := strings.Cut(entry, "="); ok && strings.HasPrefix(k, prefix) {
			names = append(names, k)
		}
	}
	return names
}

// realPath resolves symlinks, so a temp directory compares equal to the
// working directory a process reports for it.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

// numberedLines is n lines of stderr, for the excerpt tests.
func numberedLines(n int) string {
	lines := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	return strings.Join(lines, "\n")
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// syncBuffer collects the diagnostics writer's input. A verbose harness copies
// both of its streams there at once, so the writer has to tolerate that.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
