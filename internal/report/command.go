package report

import (
	"regexp"
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
