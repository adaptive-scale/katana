package coverage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseGoProfile covers the format `go test -coverprofile` writes, blocks
// repeated across test binaries included: a profile that measured the same
// package twice must not report twice its statements.
func TestParseGoProfile(t *testing.T) {
	const profile = `mode: set
example.com/mod/internal/cli/run.go:23.31,34.2 4 1
example.com/mod/internal/cli/run.go:40.2,42.16 3 0
example.com/mod/internal/cli/run.go:23.31,34.2 4 0
example.com/mod/internal/ui/table.go:10.20,12.3 2 1
`
	p, err := Parse([]byte(profile))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Format != FormatGo || p.Mode != "set" {
		t.Fatalf("format=%q mode=%q, want go/set", p.Format, p.Mode)
	}
	if len(p.Files) != 2 {
		t.Fatalf("files = %d, want 2: %+v", len(p.Files), p.Files)
	}
	run := p.Files[0]
	if run.Path != "example.com/mod/internal/cli/run.go" {
		t.Fatalf("first file = %q", run.Path)
	}
	// 4 + 3 statements counted once each, and the block that ran in either
	// binary counts as covered.
	if run.Statements != 7 || run.Covered != 4 || run.Missed() != 3 {
		t.Fatalf("run.go = %+v, want 7 statements, 4 covered", run)
	}
	if total := p.Total(); total.Statements != 9 || total.Covered != 6 {
		t.Fatalf("total = %+v, want 9 statements, 6 covered", total)
	}
	if got := p.Total().Percent(); got < 66.6 || got > 66.7 {
		t.Fatalf("total percent = %v, want ~66.67", got)
	}
}

func TestParseGoProfileRejectsMalformedLines(t *testing.T) {
	if _, err := Parse([]byte("mode: set\nnot a profile line\n")); err == nil {
		t.Fatal("want an error for a malformed profile line")
	}
}

// TestParseLCOV covers what jest, vitest, nyc and llvm-cov write, including a
// file that appears in more than one record.
func TestParseLCOV(t *testing.T) {
	const report = `TN:
SF:/repo/src/cart.js
DA:1,1
DA:2,0
DA:3,4
LF:3
LH:2
end_of_record
TN:
SF:/repo/src/cart.js
DA:1,0
DA:2,7
DA:3,0
end_of_record
SF:/repo/src/tax.js
LF:10
LH:5
end_of_record
`
	p, err := Parse([]byte(report))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Format != FormatLCOV {
		t.Fatalf("format = %q", p.Format)
	}
	if len(p.Files) != 2 {
		t.Fatalf("files = %+v", p.Files)
	}
	cart := p.Files[0]
	if cart.Path != "/repo/src/cart.js" {
		t.Fatalf("first file = %q", cart.Path)
	}
	// Three lines, and every one of them ran in one record or the other.
	if cart.Statements != 3 || cart.Covered != 3 {
		t.Fatalf("cart.js = %+v, want 3 of 3", cart)
	}
	// A record with no DA lines falls back to the summary it does carry.
	if tax := p.Files[1]; tax.Statements != 10 || tax.Covered != 5 {
		t.Fatalf("tax.js = %+v, want 5 of 10", tax)
	}
}

// TestParseCobertura covers what coverage.py writes for pytest and what
// cargo-tarpaulin writes for Rust.
func TestParseCobertura(t *testing.T) {
	const report = `<?xml version="1.0" ?>
<coverage version="7.4.0">
  <packages>
    <package name="shop">
      <classes>
        <class filename="shop/cart.py">
          <lines>
            <line number="1" hits="1"/>
            <line number="2" hits="0"/>
          </lines>
        </class>
        <class filename="shop/cart.py">
          <lines>
            <line number="2" hits="3"/>
            <line number="9" hits="0"/>
          </lines>
        </class>
        <class filename="shop/tax.py">
          <lines>
            <line number="4" hits="0"/>
          </lines>
        </class>
      </classes>
    </package>
  </packages>
</coverage>
`
	p, err := Parse([]byte(report))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Format != FormatCobertura {
		t.Fatalf("format = %q", p.Format)
	}
	if len(p.Files) != 2 {
		t.Fatalf("files = %+v", p.Files)
	}
	// One file split across two classes is one file: three distinct lines, two
	// of which ran once the records are merged.
	if cart := p.Files[0]; cart.Path != "shop/cart.py" || cart.Statements != 3 || cart.Covered != 2 {
		t.Fatalf("cart.py = %+v, want 2 of 3", cart)
	}
	if tax := p.Files[1]; tax.Statements != 1 || tax.Covered != 0 {
		t.Fatalf("tax.py = %+v, want 0 of 1", tax)
	}
}

func TestParseRejectsAnUnknownFormat(t *testing.T) {
	if _, err := Parse([]byte("total coverage: 80%\n")); err == nil {
		t.Fatal("want an error for a report in no known format")
	}
}

func TestLoadReportsTheFileItCouldNotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coverage.txt")
	if err := os.WriteFile(path, []byte("not a report"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want one naming %s", err, path)
	}
	if _, err := Load(filepath.Join(dir, "absent.out")); !os.IsNotExist(err) {
		t.Fatalf("err = %v, want a not-exist error", err)
	}
}

// TestLocalizeResolvesReportPathsToTheProject covers the two shapes a report
// names files in — an import path and an absolute one — both of which have to
// come out as something a reader can open.
func TestLocalizeResolvesReportPathsToTheProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "internal", "cli", "run.go")
	if err := os.WriteFile(source, []byte("package cli\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := Localize(root, []File{
		{Path: "example.com/mod/internal/cli/run.go", Statements: 10, Covered: 4},
		{Path: source, Statements: 10, Covered: 6},
		{Path: "example.com/mod/internal/gone/absent.go", Statements: 2, Covered: 1},
	})
	if len(files) != 2 {
		t.Fatalf("files = %+v, want the two paths merged into one plus the unresolved one", files)
	}
	// Both spellings of the same file are the same file, and the better
	// covering of the two stands.
	if got := files[0]; got.Path != "example.com/mod/internal/gone/absent.go" {
		t.Fatalf("unresolved path = %q, want it left as it was", got.Path)
	}
	if got := files[1]; got.Path != "internal/cli/run.go" || got.Covered != 6 {
		t.Fatalf("resolved = %+v, want internal/cli/run.go covered 6", got)
	}
}

func TestByDirGroupsFilesIntoTheirDirectories(t *testing.T) {
	p := &Profile{Files: []File{
		{Path: "internal/cli/run.go", Statements: 10, Covered: 5},
		{Path: "internal/cli/status.go", Statements: 10, Covered: 10},
		{Path: "main.go", Statements: 4, Covered: 0},
	}}
	dirs := p.ByDir()
	if len(dirs) != 2 {
		t.Fatalf("dirs = %+v", dirs)
	}
	if dirs[0].Path != "." || dirs[0].Statements != 4 || dirs[0].Covered != 0 {
		t.Fatalf("top level = %+v, want . with 0 of 4", dirs[0])
	}
	if dirs[1].Path != "internal/cli" || dirs[1].Statements != 20 || dirs[1].Covered != 15 {
		t.Fatalf("internal/cli = %+v, want 15 of 20", dirs[1])
	}
	if p.Empty() {
		t.Fatal("a profile with statements is not empty")
	}
	if !(&Profile{}).Empty() {
		t.Fatal("a profile with nothing measured is empty")
	}
}

func TestPercentOfAFileWithNoStatements(t *testing.T) {
	if got := (File{}).Percent(); got != 0 {
		t.Fatalf("percent = %v, want 0", got)
	}
}

func TestSortByCoverageLeadsWithTheLeastCovered(t *testing.T) {
	got := SortByCoverage([]File{
		{Path: "b.go", Statements: 10, Covered: 10},
		{Path: "a.go", Statements: 10, Covered: 5},
		{Path: "c.go", Statements: 100, Covered: 50},
	})
	// Equal percentages fall back to the larger gap, then to the path, so the
	// order is the same on every run.
	want := []string{"c.go", "a.go", "b.go"}
	for i, name := range want {
		if got[i].Path != name {
			t.Fatalf("order = %+v, want %v", got, want)
		}
	}
}

// TestInstrumentationPerRunner is the heart of it: for each language katana
// supports, the arguments it appends have to be the ones that runner actually
// takes, and the file it then reads has to be the one that runner writes.
func TestInstrumentationPerRunner(t *testing.T) {
	dest := "/tmp/cov"
	cases := []struct {
		name      string
		framework string
		command   string
		packages  string
		wantCmd   string
		wantArgs  []string
		wantFile  string
		format    string
	}{
		{
			name: "go test", framework: "go-test", command: "go test ./...", packages: "./...",
			wantArgs: []string{"-coverprofile=/tmp/cov/coverage.out", "-coverpkg=./..."},
			wantFile: "/tmp/cov/coverage.out", format: FormatGo,
		},
		{
			name: "go test without coverpkg", framework: "go-test", command: "go test ./...",
			wantArgs: []string{"-coverprofile=/tmp/cov/coverage.out"},
			wantFile: "/tmp/cov/coverage.out", format: FormatGo,
		},
		{
			name: "pytest", framework: "pytest", command: "pytest",
			wantArgs: []string{"--cov", "--cov-report=xml:/tmp/cov/coverage.xml"},
			wantFile: "/tmp/cov/coverage.xml", format: FormatCobertura,
		},
		{
			name: "jest", framework: "jest", command: "npx jest",
			wantArgs: []string{"--coverage", "--coverageReporters=lcovonly", "--coverageDirectory=/tmp/cov"},
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			// npm keeps everything before -- for itself.
			name: "jest under npm test", framework: "jest", command: "npm test",
			wantArgs: []string{"--", "--coverage", "--coverageReporters=lcovonly", "--coverageDirectory=/tmp/cov"},
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			name: "vitest", framework: "vitest", command: "npx vitest run",
			wantArgs: []string{"--coverage.enabled=true", "--coverage.reporter=lcovonly",
				"--coverage.reportsDirectory=/tmp/cov"},
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			name: "node's own runner", framework: "", command: "node --test",
			wantArgs: []string{"--experimental-test-coverage", "--test-reporter=spec",
				"--test-reporter-destination=stdout", "--test-reporter=lcov",
				"--test-reporter-destination=/tmp/cov/lcov.info"},
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			// mocha measures nothing itself, so it is run under nyc.
			name: "mocha", framework: "mocha", command: "npx mocha tests/",
			wantCmd:  "npx nyc --reporter=lcovonly --report-dir='/tmp/cov' npx mocha tests/",
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			// cargo test keeps its own flags; only its verb changes.
			name: "cargo test", framework: "cargo-test", command: "cargo test --workspace",
			wantCmd:  "cargo llvm-cov --workspace",
			wantArgs: []string{"--lcov", "--output-path", "/tmp/cov/lcov.info"},
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			name: "a project already using cargo-llvm-cov", framework: "rust", command: "cargo llvm-cov",
			wantArgs: []string{"--lcov", "--output-path", "/tmp/cov/lcov.info"},
			wantFile: "/tmp/cov/lcov.info", format: FormatLCOV,
		},
		{
			name: "a project already using tarpaulin", framework: "rust", command: "cargo tarpaulin",
			wantArgs: []string{"--out", "xml", "--output-dir", "/tmp/cov"},
			wantFile: "/tmp/cov/cobertura.xml", format: FormatCobertura,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := For(Options{Framework: c.framework, Command: c.command, Dest: dest, Packages: c.packages})
			if !in.Known {
				t.Fatalf("%s: katana should know how to instrument this", c.name)
			}
			if in.Command != c.wantCmd {
				t.Fatalf("command = %q, want %q", in.Command, c.wantCmd)
			}
			if strings.Join(in.Args, " ") != strings.Join(c.wantArgs, " ") {
				t.Fatalf("args = %q, want %q", in.Args, c.wantArgs)
			}
			if filepath.ToSlash(in.Profile) != c.wantFile {
				t.Fatalf("profile = %q, want %q", in.Profile, c.wantFile)
			}
			if in.Format != c.format {
				t.Fatalf("format = %q, want %q", in.Format, c.format)
			}
		})
	}
}

// TestInstrumentationRecognisesTheRunnerFromTheCommand covers a project whose
// katana.yaml names no framework: the command line is the only evidence there
// is, and it is usually enough.
func TestInstrumentationRecognisesTheRunnerFromTheCommand(t *testing.T) {
	for command, want := range map[string]string{
		"go test ./...":         FormatGo,
		"python -m pytest -q":   FormatCobertura,
		"npx vitest run":        FormatLCOV,
		"yarn jest":             FormatLCOV,
		"cargo test":            FormatLCOV,
		"cargo tarpaulin --lib": FormatCobertura,
	} {
		in := For(Options{Command: command, Dest: t.TempDir()})
		if !in.Known || in.Format != want {
			t.Fatalf("%q: known=%v format=%q, want %q", command, in.Known, in.Format, want)
		}
	}
}

func TestInstrumentationOfAnUnknownRunner(t *testing.T) {
	in := For(Options{Framework: "junit5", Command: "mvn test", Dest: t.TempDir()})
	if in.Known || in.Profile != "" || len(in.Args) != 0 {
		t.Fatalf("in = %+v, want nothing katana claims to know", in)
	}
}

// TestInstrumentationNotesAnOverriddenProfile covers the one case where katana
// quietly wins an argument with the project's own command line, which is worth
// saying rather than leaving to be discovered.
func TestInstrumentationNotesAnOverriddenProfile(t *testing.T) {
	in := For(Options{Framework: "go-test", Command: "go test -coverprofile=mine.out ./...", Dest: "/tmp/cov"})
	if in.Note == "" {
		t.Fatal("want a note that katana's own -coverprofile takes precedence")
	}
}

func TestFindLocatesAReportAnotherToolLeft(t *testing.T) {
	root := t.TempDir()
	if _, ok := Find(root); ok {
		t.Fatal("an empty project has no report to find")
	}
	if err := os.MkdirAll(filepath.Join(root, "coverage"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "coverage", "lcov.info")
	if err := os.WriteFile(path, []byte("SF:x\nend_of_record\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, ok := Find(root)
	if !ok || found != path {
		t.Fatalf("found = %q, %v; want %q", found, ok, path)
	}
}
