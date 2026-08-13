package coverage

import (
	"encoding/xml"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The report formats katana can read. Between them they cover every runner it
// knows how to instrument, and most of the ones it does not: LCOV is what the
// JavaScript world writes, Cobertura XML is what coverage.py and a good deal of
// CI tooling writes, and the Go profile is its own thing.
const (
	FormatGo        = "go"
	FormatLCOV      = "lcov"
	FormatCobertura = "cobertura"
)

// Detect names the format of a coverage report from its contents, or returns
// an empty string when it is none katana knows.
//
// Contents rather than file name: a profile is often written to whatever path
// the caller chose, and `coverage.xml` holding LCOV is a mistake worth reading
// through rather than failing on.
func Detect(data []byte) string {
	s := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(s, "mode:"):
		return FormatGo
	case strings.HasPrefix(s, "<?xml"), strings.HasPrefix(s, "<coverage"):
		return FormatCobertura
	case strings.HasPrefix(s, "TN:"), strings.HasPrefix(s, "SF:"),
		strings.Contains(s, "\nSF:"), strings.Contains(s, "end_of_record"):
		return FormatLCOV
	}
	return ""
}

// Parse reads a coverage report in any format katana understands.
func Parse(data []byte) (*Profile, error) {
	switch Detect(data) {
	case FormatGo:
		return parseGo(data)
	case FormatLCOV:
		return parseLCOV(data)
	case FormatCobertura:
		return parseCobertura(data)
	}
	return nil, fmt.Errorf("unrecognised coverage report: not a Go cover profile, LCOV or Cobertura XML")
}

// Load reads a coverage report from disk.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return p, nil
}

// --- go cover profile -------------------------------------------------------

// block identifies one covered region of a Go source file. `go test` writes a
// line per block, and a profile that covered several packages repeats the same
// block once per test binary that was linked against it, so blocks are keyed
// and merged rather than summed as they are read.
type block struct {
	file       string
	start, end string
}

// parseGo reads a `go test -coverprofile` file:
//
//	mode: set
//	example.com/mod/internal/cli/run.go:23.31,34.2 4 1
//
// which is the file, the region, how many statements are in it, and how often
// it ran.
func parseGo(data []byte) (*Profile, error) {
	p := &Profile{Format: FormatGo}
	stmts := map[block]int{}
	counts := map[block]int{}
	var order []string
	seen := map[string]bool{}

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "mode:"); ok {
			p.Mode = strings.TrimSpace(rest)
			continue
		}
		b, n, count, err := parseGoLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		stmts[b] = n
		counts[b] += count
		if !seen[b.file] {
			seen[b.file] = true
			order = append(order, b.file)
		}
	}

	byFile := map[string]*File{}
	for b, n := range stmts {
		f, ok := byFile[b.file]
		if !ok {
			f = &File{Path: b.file}
			byFile[b.file] = f
		}
		f.Statements += n
		if counts[b] > 0 {
			f.Covered += n
		}
	}
	for _, name := range order {
		p.Files = append(p.Files, *byFile[name])
	}
	p.Files = Merge(p.Files)
	return p, nil
}

func parseGoLine(line string) (block, int, int, error) {
	// The file name comes first and may contain anything but a newline, so the
	// last colon is the one that ends it.
	colon := strings.LastIndex(line, ":")
	if colon <= 0 {
		return block{}, 0, 0, fmt.Errorf("malformed profile line %q", line)
	}
	file, rest := line[:colon], line[colon+1:]
	fields := strings.Fields(rest)
	if len(fields) != 3 {
		return block{}, 0, 0, fmt.Errorf("malformed profile line %q", line)
	}
	start, end, ok := strings.Cut(fields[0], ",")
	if !ok {
		return block{}, 0, 0, fmt.Errorf("malformed profile line %q", line)
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		return block{}, 0, 0, fmt.Errorf("malformed statement count in %q", line)
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return block{}, 0, 0, fmt.Errorf("malformed execution count in %q", line)
	}
	return block{file: file, start: start, end: end}, n, count, nil
}

// --- lcov -------------------------------------------------------------------

// parseLCOV reads the format jest, vitest, nyc and llvm-cov write:
//
//	SF:/abs/path/to/file.js
//	DA:12,3
//	LF:40
//	LH:31
//	end_of_record
//
// DA lines are preferred where they exist, because they say which lines ran
// rather than only how many; LF and LH are the fallback for a report that
// summarises without them.
func parseLCOV(data []byte) (*Profile, error) {
	p := &Profile{Format: FormatLCOV}
	var (
		file string
		// Records are accumulated per file rather than per record: a suite run
		// in several projects, or a file imported by two of them, is reported
		// once per run, and a line that ran in any of them ran.
		hits    = map[string]map[int]int{}
		found   = map[string]int{}
		covered = map[string]int{}
		order   []string
	)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "end_of_record":
			// A record without its terminator still counts; the next SF ends it.
			file = ""
		case strings.HasPrefix(line, "SF:"):
			file = strings.TrimSpace(strings.TrimPrefix(line, "SF:"))
			if file == "" {
				continue
			}
			if _, seen := hits[file]; !seen {
				hits[file] = map[int]int{}
				order = append(order, file)
			}
		case file == "":
			// Anything outside a record belongs to no file.
		case strings.HasPrefix(line, "DA:"):
			if lineNo, count, ok := parseLCOVData(strings.TrimPrefix(line, "DA:")); ok {
				hits[file][lineNo] += count
			}
		case strings.HasPrefix(line, "LF:"):
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "LF:"))); err == nil && n > found[file] {
				found[file] = n
			}
		case strings.HasPrefix(line, "LH:"):
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "LH:"))); err == nil && n > covered[file] {
				covered[file] = n
			}
		}
	}

	for _, name := range order {
		f := File{Path: name}
		if lines := hits[name]; len(lines) > 0 {
			f.Statements = len(lines)
			for _, n := range lines {
				if n > 0 {
					f.Covered++
				}
			}
		} else {
			// A report that summarises without naming its lines still says how
			// many there were and how many ran.
			f.Statements, f.Covered = found[name], covered[name]
		}
		p.Files = append(p.Files, f)
	}
	p.Files = Merge(p.Files)
	return p, nil
}

func parseLCOVData(s string) (line, count int, ok bool) {
	// DA:<line>,<count>[,<checksum>]
	fields := strings.Split(strings.TrimSpace(s), ",")
	if len(fields) < 2 {
		return 0, 0, false
	}
	line, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return 0, 0, false
	}
	count, err = strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil {
		// A count of `-` marks a line that was not instrumented in this run.
		return line, 0, true
	}
	return line, count, true
}

// --- cobertura xml ----------------------------------------------------------

type coberturaReport struct {
	XMLName  xml.Name `xml:"coverage"`
	Packages []struct {
		Classes []struct {
			Filename string `xml:"filename,attr"`
			Lines    []struct {
				Number int    `xml:"number,attr"`
				Hits   string `xml:"hits,attr"`
			} `xml:"lines>line"`
		} `xml:"classes>class"`
	} `xml:"packages>package"`
}

// parseCobertura reads the XML coverage.py writes with `--cov-report=xml`, and
// which a good deal of other tooling writes too. A file may appear as more than
// one class — one per class in it, for the languages that have them — so lines
// are gathered per file before they are counted.
func parseCobertura(data []byte) (*Profile, error) {
	var report coberturaReport
	if err := xml.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parsing Cobertura XML: %w", err)
	}

	hits := map[string]map[int]int{}
	var order []string
	for _, pkg := range report.Packages {
		for _, class := range pkg.Classes {
			name := strings.TrimSpace(class.Filename)
			if name == "" {
				continue
			}
			lines, ok := hits[name]
			if !ok {
				lines = map[int]int{}
				hits[name] = lines
				order = append(order, name)
			}
			for _, l := range class.Lines {
				n, err := strconv.Atoi(strings.TrimSpace(l.Hits))
				if err != nil {
					// Some writers put a condition-coverage fraction here. A
					// line reported at all was instrumented; treat what cannot
					// be read as unrun rather than dropping the line.
					n = 0
				}
				lines[l.Number] += n
			}
		}
	}

	p := &Profile{Format: FormatCobertura}
	for _, name := range order {
		f := File{Path: name, Statements: len(hits[name])}
		for _, n := range hits[name] {
			if n > 0 {
				f.Covered++
			}
		}
		p.Files = append(p.Files, f)
	}
	p.Files = Merge(p.Files)
	return p, nil
}
