package report

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parsers maps a normalized framework name to the parser for that runner's
// output. Each parser is deliberately narrow: it recognises the lines its runner
// prints for a test case and ignores everything else.
var parsers = map[string]func([]string) []Case{
	"go":     parseGo,
	"pytest": parsePytest,
	"js":     parseJS,
	"cargo":  parseCargo,
	"dotnet": parseDotnet,
	"xctest": parseXCTest,
}

// parserOrder fixes the order parsers are tried in when the configured
// framework does not recognise the output, so a report is reproducible.
var parserOrder = []string{"go", "pytest", "js", "cargo", "dotnet", "xctest"}

// Frameworks lists the frameworks katana can recover per-case results from.
// Anything else still gets a report, just without the per-case table.
func Frameworks() []string {
	return []string{"go-test", "pytest", "jest", "vitest", "mocha", "cargo-test", "xunit", "xctest"}
}

// Parse recovers test cases from a runner's captured output. The framework is a
// hint from katana.yaml, not a guarantee — `test.command` can run anything — so
// an unrecognised output falls back to whichever parser reads the most of it.
func Parse(framework, out string) []Case {
	ls := lines(stripANSI(out))
	if p, ok := parsers[normalizeFramework(framework)]; ok {
		if cases := p(ls); len(cases) > 0 {
			return cases
		}
	}
	var best []Case
	for _, name := range parserOrder {
		if cases := parsers[name](ls); len(cases) > len(best) {
			best = cases
		}
	}
	return best
}

// normalizeFramework maps the framework names katana.yaml accepts — and the
// language names, since a project may set only one — onto a parser key.
func normalizeFramework(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "go", "golang", "go-test", "gotest", "gotestsum":
		return "go"
	case "pytest", "py.test", "python", "py":
		return "pytest"
	case "jest", "vitest", "mocha", "jasmine", "ava", "javascript", "typescript", "js", "ts":
		return "js"
	case "cargo", "cargo-test", "rust":
		return "cargo"
	case "xunit", "nunit", "mstest", "dotnet", "csharp":
		return "dotnet"
	case "xctest", "swift-testing", "swift":
		return "xctest"
	}
	return ""
}

// --- go test ---------------------------------------------------------------

var (
	goResult = regexp.MustCompile(`^\s*--- (PASS|FAIL|SKIP): (\S+) \(([0-9.]+)s\)`)
	goRun    = regexp.MustCompile(`^\s*=== (RUN|PAUSE|CONT|NAME)\b`)
	goPkg    = regexp.MustCompile(`^(ok|FAIL|\?)\s+(\S+)(?:\s+(.*))?$`)
)

// parseGo reads `go test` output. Package lines come after the tests they cover,
// so cases are attributed to a package when its summary line arrives.
//
// Detail printed by t.Error and friends appears between `=== RUN` and the
// `--- FAIL` line, so it is buffered and attached to the next result.
func parseGo(ls []string) []Case {
	var cases []Case
	var pending []string
	unattributed := 0

	attribute := func(pkg string) {
		for i := unattributed; i < len(cases); i++ {
			cases[i].Suite = pkg
		}
		unattributed = len(cases)
	}

	for _, line := range ls {
		switch {
		case goRun.MatchString(line):
			pending = nil

		case goResult.MatchString(line):
			m := goResult.FindStringSubmatch(line)
			c := Case{Name: m[2], Status: goStatus(m[1]), Duration: seconds(m[3])}
			if c.Status != StatusPass {
				c.Output = dedent(pending)
			}
			pending = nil
			cases = append(cases, c)

		case goPkg.MatchString(line):
			m := goPkg.FindStringSubmatch(line)
			// A bracketed note in place of a timing means the package never ran
			// its tests: a build or setup failure. Record it as a failed case so
			// the report does not read as an empty, passing package.
			if m[1] == "FAIL" && strings.HasPrefix(m[3], "[") {
				cases = append(cases, Case{
					Suite:   m[2],
					Name:    strings.Trim(m[3], "[]"),
					Status:  StatusFail,
					Output:  dedent(pending),
					Blocked: true,
				})
			}
			attribute(m[2])
			pending = nil

		case strings.TrimSpace(line) == "PASS" || strings.TrimSpace(line) == "FAIL":
			pending = nil

		default:
			pending = appendDetail(pending, line)
		}
	}
	return cases
}

func goStatus(s string) Status {
	switch s {
	case "FAIL":
		return StatusFail
	case "SKIP":
		return StatusSkip
	}
	return StatusPass
}

// --- pytest ----------------------------------------------------------------

var (
	pytestVerbose = regexp.MustCompile(`^(\S+\.py)::(\S+)\s+(PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)\b`)
	pytestSummary = regexp.MustCompile(`^(PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)\s+(\S+\.py)::(\S+?)(?:\s+-\s+(.*))?$`)
	pytestFailure = regexp.MustCompile(`^_{3,}\s+(.+?)\s+_{3,}$`)
)

// parsePytest reads pytest output. The per-case lines only appear under -v, so
// the short summary at the bottom of a default run is read as well, and the
// FAILURES section is mined for each failing case's traceback.
func parsePytest(ls []string) []Case {
	var cases []Case
	index := map[string]int{}

	add := func(file, name string, status Status, detail string) {
		key := file + "::" + name
		if i, ok := index[key]; ok {
			if detail != "" && cases[i].Output == "" {
				cases[i].Output = detail
			}
			return
		}
		index[key] = len(cases)
		cases = append(cases, Case{Suite: file, Name: name, Status: status, Output: detail})
	}

	for _, line := range ls {
		if m := pytestVerbose.FindStringSubmatch(line); m != nil {
			add(m[1], m[2], pytestStatus(m[3]), "")
			continue
		}
		if m := pytestSummary.FindStringSubmatch(line); m != nil {
			add(m[2], m[3], pytestStatus(m[1]), m[4])
		}
	}
	if len(cases) == 0 {
		return nil
	}

	// Attach tracebacks from the FAILURES section, keyed by the case name in the
	// section header.
	var header string
	var body []string
	flush := func() {
		if header == "" {
			return
		}
		if i, ok := findCase(cases, header); ok && cases[i].Output == "" {
			cases[i].Output = dedent(body)
		}
		header, body = "", nil
	}
	for _, line := range ls {
		if m := pytestFailure.FindStringSubmatch(line); m != nil {
			flush()
			header = m[1]
			continue
		}
		if header != "" {
			if strings.HasPrefix(line, "===") {
				flush()
				continue
			}
			body = appendDetail(body, line)
		}
	}
	flush()
	return cases
}

func pytestStatus(s string) Status {
	switch s {
	case "FAILED", "ERROR":
		return StatusFail
	case "SKIPPED", "XFAIL":
		return StatusSkip
	}
	return StatusPass
}

// findCase locates the case a pytest failure header names. Headers are written
// as `test_name` or `TestClass.test_name`, while the case name uses `::`.
func findCase(cases []Case, header string) (int, bool) {
	want := strings.ReplaceAll(header, ".", "::")
	for i, c := range cases {
		if c.Name == want || lastSegment(c.Name) == lastSegment(want) {
			return i, true
		}
	}
	return 0, false
}

func lastSegment(s string) string {
	if i := strings.LastIndexAny(s, ":."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// --- jest / vitest / mocha -------------------------------------------------

var (
	jsPass = regexp.MustCompile(`^[✓✔]\s+(.+?)(?:\s+\((\d+)\s*ms\))?$`)
	jsFail = regexp.MustCompile(`^[✕✗×✘]\s+(.+?)(?:\s+\((\d+)\s*ms\))?$`)
	jsSkip = regexp.MustCompile(`^[○◯↓⊘✎]\s+(.+?)(?:\s+\((\d+)\s*ms\))?$`)
	jsFile = regexp.MustCompile(`^(?:PASS|FAIL|RUNS)\s+(\S+)`)
	// A vitest file summary reuses the tick, e.g. "✓ src/a.test.ts (3 tests) 5ms".
	jsFileSummary = regexp.MustCompile(`\(\d+ tests?\)`)
)

// parseJS reads the tick-and-cross output shared by jest, vitest and mocha.
func parseJS(ls []string) []Case {
	var cases []Case
	suite := ""

	for _, raw := range ls {
		line := strings.TrimSpace(raw)
		if m := jsFile.FindStringSubmatch(line); m != nil {
			suite = m[1]
			continue
		}
		for _, p := range []struct {
			re     *regexp.Regexp
			status Status
		}{{jsPass, StatusPass}, {jsFail, StatusFail}, {jsSkip, StatusSkip}} {
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if jsFileSummary.MatchString(line) {
				break // a per-file roll-up, not a case
			}
			cases = append(cases, Case{
				Suite:    suite,
				Name:     strings.TrimSpace(m[1]),
				Status:   p.status,
				Duration: millis(m[2]),
			})
			break
		}
	}
	return cases
}

// --- cargo test ------------------------------------------------------------

var (
	cargoResult  = regexp.MustCompile(`^test\s+(\S+)\s+\.\.\.\s+(ok|FAILED|ignored)`)
	cargoRunning = regexp.MustCompile(`^\s*Running\s+(.+?)\s+\(`)
	cargoStdout  = regexp.MustCompile(`^-{4}\s+(\S+)\s+stdout\s+-{4}$`)
)

// parseCargo reads `cargo test` output, including the failure bodies cargo
// prints under its "failures:" section.
func parseCargo(ls []string) []Case {
	var cases []Case
	suite := ""

	for _, line := range ls {
		if m := cargoRunning.FindStringSubmatch(line); m != nil {
			suite = strings.TrimSpace(m[1])
			continue
		}
		if m := cargoResult.FindStringSubmatch(line); m != nil {
			status := StatusPass
			switch m[2] {
			case "FAILED":
				status = StatusFail
			case "ignored":
				status = StatusSkip
			}
			cases = append(cases, Case{Suite: suite, Name: m[1], Status: status})
		}
	}
	if len(cases) == 0 {
		return nil
	}

	var name string
	var body []string
	flush := func() {
		if name == "" {
			return
		}
		for i := range cases {
			if cases[i].Name == name && cases[i].Output == "" {
				cases[i].Output = dedent(body)
				break
			}
		}
		name, body = "", nil
	}
	for _, line := range ls {
		if m := cargoStdout.FindStringSubmatch(line); m != nil {
			flush()
			name = m[1]
			continue
		}
		if name != "" {
			if strings.HasPrefix(line, "failures:") || cargoResult.MatchString(line) {
				flush()
				continue
			}
			body = appendDetail(body, line)
		}
	}
	flush()
	return cases
}

// --- dotnet test -----------------------------------------------------------

var dotnetResult = regexp.MustCompile(`^(Passed|Failed|Skipped)\s+(\S+)(?:\s+\[([^\]]+)\])?$`)

// parseDotnet reads the per-test lines of `dotnet test`, splitting the class off
// the fully qualified name so cases group by class.
func parseDotnet(ls []string) []Case {
	var cases []Case
	for _, raw := range ls {
		m := dotnetResult.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		status := StatusPass
		switch m[1] {
		case "Failed":
			status = StatusFail
		case "Skipped":
			status = StatusSkip
		}
		suite, name := splitQualified(m[2])
		cases = append(cases, Case{Suite: suite, Name: name, Status: status, Duration: bracketDuration(m[3])})
	}
	return cases
}

func splitQualified(full string) (suite, name string) {
	if i := strings.LastIndex(full, "."); i > 0 {
		return full[:i], full[i+1:]
	}
	return "", full
}

// bracketDuration reads the "[12 ms]" / "[1 s]" / "[< 1 ms]" timings dotnet
// prints. An unreadable timing is simply no timing.
func bracketDuration(s string) time.Duration {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "<"))
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	switch fields[1] {
	case "ms":
		return timed(v * float64(time.Millisecond))
	case "s":
		return timed(v * float64(time.Second))
	case "m":
		return timed(v * float64(time.Minute))
	}
	return 0
}

// --- XCTest ----------------------------------------------------------------

var xctestResult = regexp.MustCompile(`^Test Case '(.+?)'\s+(passed|failed|skipped)\s+\(([0-9.]+) seconds?\)`)

// parseXCTest reads `swift test` / xcodebuild output. Case names are printed
// either as "-[FooTests testBar]" or as "FooTests.testBar".
func parseXCTest(ls []string) []Case {
	var cases []Case
	for _, raw := range ls {
		m := xctestResult.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			continue
		}
		status := StatusPass
		switch m[2] {
		case "failed":
			status = StatusFail
		case "skipped":
			status = StatusSkip
		}
		suite, name := splitXCTestName(m[1])
		cases = append(cases, Case{Suite: suite, Name: name, Status: status, Duration: seconds(m[3])})
	}
	return cases
}

func splitXCTestName(s string) (suite, name string) {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "-["), "]")
	if i := strings.LastIndexAny(s, " ."); i > 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// --- shared helpers --------------------------------------------------------

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// stripANSI removes terminal colour codes and normalises line endings, so a
// coloured runner parses the same as a piped one.
func stripANSI(s string) string {
	s = ansi.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func lines(s string) []string { return strings.Split(s, "\n") }

// maxDetailLines bounds the failure detail kept per case; a test that prints a
// megabyte of diagnostics should not dominate the report.
const maxDetailLines = 200

func appendDetail(buf []string, line string) []string {
	if strings.TrimSpace(line) == "" && len(buf) == 0 {
		return buf
	}
	if len(buf) >= maxDetailLines {
		return buf
	}
	return append(buf, strings.TrimRight(line, " \t"))
}

// dedent joins detail lines, removing the indentation the runner added and any
// blank lines at either end.
func dedent(ls []string) string {
	out := make([]string, 0, len(ls))
	indent := -1
	for _, l := range ls {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for _, l := range ls {
		if indent > 0 && len(l) >= indent {
			l = l[indent:]
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func seconds(s string) time.Duration {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return timed(v * float64(time.Second))
}

func millis(s string) time.Duration {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return timed(v * float64(time.Millisecond))
}

// timed keeps a reported timing distinguishable from an absent one. Runners
// round: `go test` prints 0.00s for anything under 5ms. Rendering that as "no
// timing" would leave a fast suite looking untimed, so a reported zero becomes
// the smallest non-zero duration and renders as "<1ms".
func timed(f float64) time.Duration {
	if d := time.Duration(f); d > 0 {
		return d
	}
	return time.Nanosecond
}
