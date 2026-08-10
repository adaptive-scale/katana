// Package discover reads an existing codebase and writes the behavior it
// implements back out as behavior markdown.
//
// It is katana's loop run backwards. `katana generate` turns a written behavior
// into tests; discovery turns code that was never written down into the
// behavior files generation needs, so a project with years of untested code has
// somewhere to start. What comes back is a draft: it describes what the code
// does today, including whatever it does by accident, and is meant to be read
// and corrected before any test is generated from it.
package discover

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adaptive-scale/katana/internal/config"
)

// Grouping selects how source files are gathered into behavior files.
type Grouping string

const (
	// GroupDir writes one behavior file per source directory. A directory is a
	// package in most languages katana targets, which makes it the smallest
	// grouping that still holds a whole capability rather than a fragment.
	GroupDir Grouping = "dir"
	// GroupFile writes one behavior file per source file, for codebases whose
	// directories are too large to describe in one specification.
	GroupFile Grouping = "file"
)

// Groupings lists the accepted grouping names.
func Groupings() []string { return []string{string(GroupDir), string(GroupFile)} }

// Options configure a scan.
type Options struct {
	// Root is the project root. Every path into and out of a scan is relative
	// to it, in slash form.
	Root string
	// Language selects which files count as source.
	Language string
	// BehaviorDir is where the behavior files being discovered will live. It is
	// excluded from the scan: specifications are not source.
	BehaviorDir string
	// Group is the unit of discovery. Empty means GroupDir.
	Group Grouping
	// Paths limits the scan to these project-relative files or subtrees. Empty
	// scans the whole project.
	Paths []string
	// Exclude skips additional directories, matched either by name at any depth
	// or by project-relative path.
	Exclude []string
	// IncludeTests keeps test code, which is left out by default.
	IncludeTests bool
}

// Unit is one group of source files that becomes a single behavior file.
type Unit struct {
	// Name is what the unit is called on screen: the directory or file it came
	// from, relative to the project root.
	Name string
	// Dir is the project-relative directory the sources live in, "." at the root.
	Dir string
	// Files are the project-relative source files, sorted.
	Files []string
	// Bytes is their total size.
	Bytes int64
	// Output is the project-relative path of the behavior file to write.
	Output string
}

// skipDirs hold code nobody in this project wrote by hand — dependencies, build
// output, caches. Hidden directories are skipped too, so .git and .katana need
// no entry here.
//
// A directory named on the command line is always scanned, however it is named:
// this list decides what a walk descends into, never what the user asked for.
var skipDirs = map[string]bool{
	"vendor": true, "node_modules": true, "bower_components": true,
	"third_party": true, "thirdparty": true, "dist": true, "build": true,
	"out": true, "target": true, "bin": true, "obj": true, "coverage": true,
	"__pycache__": true, "site-packages": true, "venv": true, "_build": true,
	"pods": true, "deriveddata": true, "generated": true,
}

// generatedSuffixes name files a tool wrote. Whatever behavior they carry
// belongs to the generator, not to this codebase.
var generatedSuffixes = []string{
	".min.js", ".min.ts", ".pb.go", ".pb.gw.go", "_pb2.py", "_pb2_grpc.py",
	".pb.cc", ".pb.php", "_generated.go", ".gen.go", ".generated.cs",
	".designer.cs", ".g.cs", ".d.ts", ".g.dart",
}

// Scan finds the source files opts describes and gathers them into the units
// discovery works on, sorted by the file each one writes.
func Scan(opts Options) ([]Unit, error) {
	if opts.Group == "" {
		opts.Group = GroupDir
	}
	if opts.Group != GroupDir && opts.Group != GroupFile {
		return nil, fmt.Errorf("unknown grouping %q; use %s", opts.Group, strings.Join(Groupings(), " or "))
	}
	if len(config.Extensions(opts.Language)) == 0 {
		return nil, fmt.Errorf("katana does not know which files are %s source; discovery needs one of: %s",
			opts.Language, strings.Join(config.Languages(), ", "))
	}

	roots := opts.Paths
	if len(roots) == 0 {
		roots = []string{"."}
	}
	sizes := map[string]int64{}
	for _, r := range roots {
		if err := opts.collect(r, sizes); err != nil {
			return nil, err
		}
	}
	if len(sizes) == 0 {
		return nil, nil
	}

	units := opts.group(sizes)
	opts.assignOutputs(units)
	sort.Slice(units, func(i, j int) bool { return units[i].Output < units[j].Output })
	return units, nil
}

// collect walks one requested path, adding every source file it finds to sizes.
func (o Options) collect(root string, sizes map[string]int64) error {
	rel := path.Clean(filepath.ToSlash(root))
	abs := filepath.Join(o.Root, filepath.FromSlash(rel))

	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("%q: %w", root, err)
	}
	if !info.IsDir() {
		// A file named outright is taken at its word, test or not: the user
		// pointed at it.
		if !config.IsSourcePath(o.Language, rel) {
			return fmt.Errorf("%q is not a %s source file", root, o.Language)
		}
		sizes[rel] = info.Size()
		return nil
	}

	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, err := filepath.Rel(o.Root, p)
		if err != nil {
			return err
		}
		r = filepath.ToSlash(r)

		if d.IsDir() {
			if p == abs {
				return nil // the directory asked for is always descended into
			}
			if o.skipDir(d.Name(), r) {
				return fs.SkipDir
			}
			return nil
		}
		if !config.IsSourcePath(o.Language, r) {
			return nil
		}
		if !o.IncludeTests && config.IsTestPath(o.Language, r) {
			return nil
		}
		if isGenerated(p, d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sizes[r] = info.Size()
		return nil
	})
}

// skipDir reports whether a walk should turn back at this directory.
func (o Options) skipDir(name, rel string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	if skipDirs[strings.ToLower(name)] {
		return true
	}
	if rel == config.Dir || rel == path.Clean(o.BehaviorDir) {
		return true
	}
	for _, e := range o.Exclude {
		e = strings.TrimSuffix(path.Clean(filepath.ToSlash(e)), "/")
		if e == "" || e == "." {
			continue
		}
		if e == name || e == rel || strings.HasPrefix(rel, e+"/") {
			return true
		}
	}
	return false
}

// group gathers the scanned files into units according to o.Group.
func (o Options) group(sizes map[string]int64) []Unit {
	if o.Group == GroupFile {
		units := make([]Unit, 0, len(sizes))
		for f, size := range sizes {
			units = append(units, Unit{Name: f, Dir: path.Dir(f), Files: []string{f}, Bytes: size})
		}
		return units
	}

	byDir := map[string]*Unit{}
	for f, size := range sizes {
		dir := path.Dir(f)
		u, ok := byDir[dir]
		if !ok {
			u = &Unit{Name: dir, Dir: dir}
			byDir[dir] = u
		}
		u.Files = append(u.Files, f)
		u.Bytes += size
	}

	units := make([]Unit, 0, len(byDir))
	for _, u := range byDir {
		sort.Strings(u.Files)
		units = append(units, *u)
	}
	return units
}

// assignOutputs gives each unit the behavior file it writes.
//
// The behavior tree mirrors the source tree — internal/config becomes
// behaviors/internal/config.md — so the tests generated from it land under
// tests/internal/ and all three trees line up directory for directory.
func (o Options) assignOutputs(units []Unit) {
	claimed := map[string]int{}
	for i := range units {
		units[i].Output = o.behaviorPath(units[i], "")
		claimed[units[i].Output]++
	}
	// Two units can want the same file: handler.js beside handler.jsx, or a
	// package sharing its name with the project it sits in. Both keep what
	// distinguishes them rather than one silently overwriting the other.
	for i := range units {
		if claimed[units[i].Output] > 1 {
			if d := o.disambiguator(units[i]); d != "" {
				units[i].Output = o.behaviorPath(units[i], d)
			}
		}
	}
}

// disambiguator is what a unit adds to its behavior file name when another unit
// wants the same one.
func (o Options) disambiguator(u Unit) string {
	if o.Group == GroupFile {
		return strings.TrimPrefix(path.Ext(u.Files[0]), ".")
	}
	if u.Dir == "." || u.Dir == "" {
		return "root"
	}
	return ""
}

func (o Options) behaviorPath(u Unit, suffix string) string {
	dir := path.Clean(o.BehaviorDir)
	if dir == "" || dir == "/" {
		dir = "behaviors"
	}

	var name string
	switch {
	case o.Group == GroupFile:
		base := path.Base(u.Files[0])
		name = path.Join(path.Dir(u.Files[0]), strings.TrimSuffix(base, path.Ext(base)))
	case u.Dir == "." || u.Dir == "":
		// Files at the root have no directory to be named after, so the project
		// itself names them.
		name = projectName(o.Root)
	default:
		name = u.Dir
	}
	if suffix != "" {
		name += "_" + suffix
	}
	return path.Join(dir, path.Clean(name)+".md")
}

// projectName is the name a behavior file at the project root is given.
func projectName(root string) string {
	base := filepath.Base(filepath.Clean(root))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "root"
	}
	return base
}

// isGenerated reports whether a file was written by a tool. The name catches
// the well-known shapes; the header catches the rest, since a generator that
// expects its output to be committed says so in the first lines.
func isGenerated(abs, name string) bool {
	lower := strings.ToLower(name)
	for _, s := range generatedSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}

	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	head := strings.ToLower(string(buf[:n]))
	return strings.Contains(head, "do not edit") || strings.Contains(head, "@generated")
}
