// Package coverage measures how much of a project's own code its tests execute.
//
// katana instruments nothing itself. Every runner it drives can already record
// coverage, and each one writes it in a format of its own, so this package
// holds the two things that knowledge amounts to: the arguments that turn
// coverage on for a runner, and how to read the file that comes out. What a
// caller gets back is the same shape whichever runner produced it — statements
// counted, statements run, per file — so `katana coverage` reads the same on a
// Go project as on a Python one.
package coverage

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// File is one source file's coverage: how many statements the runner counted in
// it, and how many of them ran. Runners that count lines rather than statements
// report lines here; the two are the same measurement to a reader, and calling
// them one thing keeps a mixed-language project comparable with itself.
type File struct {
	// Path is the file, project-relative once Localize has been applied. Before
	// that it is whatever the report said, which for a Go profile is an import
	// path and for LCOV is usually absolute.
	Path string `json:"path"`
	// Statements is how many statements the runner counted.
	Statements int `json:"statements"`
	// Covered is how many of them ran at least once.
	Covered int `json:"covered"`
}

// Missed is the statements that never ran.
func (f File) Missed() int { return f.Statements - f.Covered }

// Percent is the share of statements that ran, 0 to 100. A file the runner
// found no statements in reports 0, which is why Statements is worth checking
// before showing a number: nothing measured is not the same as nothing covered.
func (f File) Percent() float64 {
	if f.Statements <= 0 {
		return 0
	}
	return float64(f.Covered) * 100 / float64(f.Statements)
}

// Profile is a coverage report katana has read.
type Profile struct {
	// Format names the report that was parsed: go, lcov or cobertura.
	Format string
	// Mode is the Go cover mode — set, count or atomic — and is empty for the
	// formats that have no such notion.
	Mode string
	// Files are the measured files, sorted by path.
	Files []File
}

// Total sums the whole profile. Its path is empty: it is every file at once.
func (p *Profile) Total() File {
	var t File
	for _, f := range p.Files {
		t.Statements += f.Statements
		t.Covered += f.Covered
	}
	return t
}

// Empty reports whether the profile measured nothing at all, which is the case
// worth saying out loud: a runner that recorded no coverage produces a valid
// report full of zeroes, and "0.0%" would read as a project with no tests.
func (p *Profile) Empty() bool { return p.Total().Statements == 0 }

// ByDir groups the files by the directory they are in, which is the unit a
// reader thinks in — a package in Go, a module elsewhere. The top level is
// named `.`, as the rest of katana names it.
func (p *Profile) ByDir() []File {
	byDir := map[string]*File{}
	var order []string
	for _, f := range p.Files {
		dir := path.Dir(f.Path)
		if dir == "" {
			dir = "."
		}
		agg, ok := byDir[dir]
		if !ok {
			agg = &File{Path: dir}
			byDir[dir] = agg
			order = append(order, dir)
		}
		agg.Statements += f.Statements
		agg.Covered += f.Covered
	}
	sort.Strings(order)
	out := make([]File, 0, len(order))
	for _, dir := range order {
		out = append(out, *byDir[dir])
	}
	return out
}

// Localize rewrites report paths into paths inside the project, so a Go profile
// naming import paths and an LCOV report naming absolute ones both come out as
// something a reader can open.
//
// A path is resolved by keeping the longest tail of it that names a file under
// root: `example.com/mod/internal/cli/run.go` is `internal/cli/run.go` when that
// file exists. A path that resolves to nothing is left exactly as it was —
// guessing at a shorter one would invent a file that is not there.
func Localize(root string, files []File) []File {
	out := make([]File, 0, len(files))
	for _, f := range files {
		f.Path = localizePath(root, f.Path)
		out = append(out, f)
	}
	return Merge(out)
}

func localizePath(root, p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
		return filepath.ToSlash(p)
	}
	clean := path.Clean(filepath.ToSlash(p))
	segments := strings.Split(clean, "/")
	for i := range segments {
		candidate := strings.Join(segments[i:], "/")
		if candidate == "" || candidate == "." {
			break
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate))); err == nil {
			return candidate
		}
	}
	return clean
}

// Merge combines repeated paths into one entry each and sorts the result.
//
// Reports repeat themselves for ordinary reasons: a Go profile written by
// `go test ./... -coverpkg=./...` carries every package once per test binary,
// and LCOV appends a record per run. Two entries for one file would otherwise
// count its statements twice.
func Merge(files []File) []File {
	byPath := map[string]*File{}
	var order []string
	for _, f := range files {
		agg, ok := byPath[f.Path]
		if !ok {
			copied := f
			byPath[f.Path] = &copied
			order = append(order, f.Path)
			continue
		}
		// The same file measured twice is the same file, not twice the code.
		// Its statement count stands, and the better of the two coverings wins:
		// a statement that ran in either run ran.
		if f.Statements > agg.Statements {
			agg.Statements = f.Statements
		}
		if f.Covered > agg.Covered {
			agg.Covered = f.Covered
		}
	}
	sort.Strings(order)
	out := make([]File, 0, len(order))
	for _, p := range order {
		f := *byPath[p]
		if f.Covered > f.Statements {
			f.Covered = f.Statements
		}
		out = append(out, f)
	}
	return out
}

// SortByCoverage orders files from least covered to most, so the worst of a
// long list is at the top where it will be read. Ties fall back to the path, so
// the order is stable between runs.
func SortByCoverage(files []File) []File {
	out := append([]File(nil), files...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Percent() != b.Percent() {
			return a.Percent() < b.Percent()
		}
		if a.Missed() != b.Missed() {
			return a.Missed() > b.Missed()
		}
		return a.Path < b.Path
	})
	return out
}
