package cli

import (
	"os"
	"path"
	"sort"

	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/testindex"
)

// Behaviors are specified one at a time and generated one at a time, but their
// tests do not land one at a time: several behaviors under one folder produce
// several files in one directory, and in a language where a directory is one
// namespace — Go is one — those files share a set of names.
//
// Two specifications describing the same rule about different parts of the
// product is normal and correct. Two generations picking the same name for it
// is not: the package stops compiling, and every test in every file beside it
// stops running while still reading as "up to date" in the tracker. So katana
// tells each generation which names its neighbours have already taken, and says
// so plainly when a collision lands anyway.

// scanDeclared maps each behavior's output file to the test cases it declares
// right now, read from disk rather than from the tracker: a file katana has no
// record of writing still occupies its names, and a failed generation leaves
// exactly that behind.
//
// A file that cannot be read contributes nothing. It is either not written yet
// or not katana's to explain, and neither is worth failing a generation over.
func scanDeclared(root string, items []plan.Item) map[string][]string {
	out := make(map[string][]string, len(items))
	for _, it := range items {
		body, err := os.ReadFile(it.AbsOutput(root))
		if err != nil {
			continue
		}
		if names := testindex.Names(string(body), it.Language); len(names) > 0 {
			out[it.Output] = names
		}
	}
	return out
}

// reservedFor lists the test names declared by the other files sharing a
// directory with output, sorted so the same neighbours produce the same prompt
// and an unchanged behavior is not regenerated differently each run.
func reservedFor(declared map[string][]string, output string) []string {
	dir := path.Dir(output)
	seen := map[string]bool{}
	var out []string
	for file, names := range declared {
		if file == output || path.Dir(file) != dir {
			continue
		}
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// clash is one test name declared by more than one file in a directory.
type clash struct {
	Name  string
	Files []string
}

// duplicateTests reports the names declared by more than one file in the same
// directory, sorted by name. Files sharing a directory but not a namespace —
// pytest modules, jest specs — are reported too: a duplicate name there is
// legal but still makes a failure ambiguous about which file it came from.
func duplicateTests(declared map[string][]string) []clash {
	type where struct{ dir, name string }
	byName := map[where][]string{}
	for file, names := range declared {
		dir := path.Dir(file)
		for _, n := range names {
			k := where{dir: dir, name: n}
			byName[k] = append(byName[k], file)
		}
	}

	var out []clash
	for k, files := range byName {
		if len(files) < 2 {
			continue
		}
		sort.Strings(files)
		out = append(out, clash{Name: k.name, Files: files})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Files[0] < out[j].Files[0]
	})
	return out
}
