package report

import (
	"regexp"
	"sort"
	"strings"
)

var (
	goTestCmd  = regexp.MustCompile(`(^|\s)go\s+test(\s|$)`)
	pytestCmd  = regexp.MustCompile(`(^|\s)(pytest|py\.test)(\s|$)`)
	goVerbose  = regexp.MustCompile(`(^|\s)(-v|-json|-test\.v)(\s|=|$)`)
	pyVerbose  = regexp.MustCompile(`(^|\s)(-v+|-q+|--verbose|--quiet|--tb)(\s|=|$)`)
	shellMetaC = regexp.MustCompile("[|&;<>`$()]")
)

// Verbose returns command adjusted so the runner prints a line per test case,
// and whether anything was added.
//
// Some runners only report individual cases in verbose mode — a default
// `go test ./...` prints one line per package — and "the suite passed" is not a
// report worth saving. Only the runners katana knows the flag for are touched,
// and never a command with shell syntax in it, where appending an argument
// would land in the wrong place.
func Verbose(framework, command string) (string, bool) {
	if shellMetaC.MatchString(command) {
		return command, false
	}
	switch {
	case normalizeFramework(framework) == "go" || goTestCmd.MatchString(command):
		if goTestCmd.MatchString(command) && !goVerbose.MatchString(command) {
			return strings.TrimSpace(command) + " -v", true
		}
	case normalizeFramework(framework) == "pytest" || pytestCmd.MatchString(command):
		if pytestCmd.MatchString(command) && !pyVerbose.MatchString(command) {
			return strings.TrimSpace(command) + " -v", true
		}
	}
	return command, false
}

// unsafeArg matches anything katana will not put on a command line it assembled
// itself. A behavior's test file and case names come from the project, not from
// katana, and the command is handed to a shell.
var unsafeArg = regexp.MustCompile(`[^\w./:@+=-]`)

// Filter returns command narrowed to one behavior's tests, and whether anything
// was narrowed. testFile is the behavior's generated test file, project-relative;
// cases are the test names the tracker recorded for it.
//
// Not every runner can be narrowed the same way, and some cannot be narrowed by
// katana at all. A caller that gets false back has a choice to make — run the
// whole suite, or nothing — and it is better placed to make it than a guess
// here would be: running the wrong tests is worse than running too many.
func Filter(framework, command, testFile string, cases []string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" || shellMetaC.MatchString(command) {
		return command, false
	}

	switch key := frameworkFor(framework, command); key {
	case "go":
		// `go test -run` splits its pattern on "/" to match subtests level by
		// level, so an anchored alternation of full subtest names would never
		// match. The top-level name of each case is what selects it, and the
		// subtests below it come along.
		names := topLevel(cases)
		if len(names) == 0 {
			return command, false
		}
		return command + " -run '^(" + strings.Join(names, "|") + ")$'", true
	case "pytest", "js":
		// pytest, jest, vitest and mocha all take a path and run what is in it,
		// which is exactly the behavior's tests: katana generated the file for
		// that behavior alone.
		if testFile == "" || unsafeArg.MatchString(testFile) {
			return command, false
		}
		return command + " " + testFile, true
	}
	return command, false
}

// frameworkFor names the runner a command uses, preferring what katana.yaml
// says and falling back to what the command line looks like.
func frameworkFor(framework, command string) string {
	if f := normalizeFramework(framework); f != "" {
		return f
	}
	switch {
	case goTestCmd.MatchString(command):
		return "go"
	case pytestCmd.MatchString(command):
		return "pytest"
	}
	return ""
}

// topLevel reduces case names to the distinct top-level tests they belong to,
// dropping any that could not be put on a command line safely.
func topLevel(cases []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cases {
		name, _, _ := strings.Cut(strings.TrimSpace(c), "/")
		if name == "" || seen[name] || unsafeArg.MatchString(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
