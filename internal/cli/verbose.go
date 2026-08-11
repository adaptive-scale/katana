package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/adaptive-scale/katana/internal/discover"
	"github.com/adaptive-scale/katana/internal/generator"
	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/testindex"
)

// previewLines bounds the prompt and generated-file previews `--verbose` prints.
// Both can run to thousands of lines, and the point is to show what is being
// generated, not to replace reading the file.
const previewLines = 40

// describeRequest narrates one generation before the harness starts, so the
// wait is spent looking at what was asked for.
func describeRequest(w io.Writer, root string, r *harness.Runner, it plan.Item, source string) {
	spec := r.Spec()
	target := "new file"
	if info, err := os.Stat(it.AbsOutput(root)); err == nil {
		target = "replacing " + byteSize(info.Size())
	}

	fmt.Fprintf(w, "  spec     %s (%s, %d lines)\n", it.Source, byteSize(int64(len(source))), countLines(source))
	fmt.Fprintf(w, "  target   %s (%s)\n", it.Output, target)
	fmt.Fprintf(w, "  stack    %s / %s\n", it.Language, it.Framework)
	fmt.Fprintf(w, "  harness  %s (prompt via %s)\n",
		strings.TrimSpace(spec.Command+" "+strings.Join(spec.Args, " ")), spec.Prompt)
	if it.Instructions != "" {
		fmt.Fprintf(w, "  extra    %s\n", firstLine(it.Instructions))
	}
}

// describeUnit narrates one discovery before the harness starts: which files it
// is about to read, and what it will write.
func describeUnit(w io.Writer, root string, r *harness.Runner, u discover.Unit, language string) {
	spec := r.Spec()
	target := "new file"
	if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(u.Output))); err == nil {
		target = "updating " + byteSize(info.Size())
	}

	fmt.Fprintf(w, "  source   %s (%d %s file(s), %s)\n", u.Name, len(u.Files), language, byteSize(u.Bytes))
	for _, f := range u.Files {
		fmt.Fprintf(w, "    • %s\n", f)
	}
	fmt.Fprintf(w, "  target   %s (%s)\n", u.Output, target)
	fmt.Fprintf(w, "  harness  %s (prompt via %s)\n",
		strings.TrimSpace(spec.Command+" "+strings.Join(spec.Args, " ")), spec.Prompt)
}

// describeSpec reports the behavior file a discovery produced, counted the way
// a reader would read it: sections, and the statements under them that each
// become a test case.
func describeSpec(w io.Writer, root, name string, out *discover.Outcome) {
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		fmt.Fprintf(w, "  wrote    %s (%d bytes)\n", name, out.Bytes)
		return
	}
	sections, statements := 0, 0
	for _, line := range strings.Split(string(body), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#"):
			sections++
		case strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "):
			statements++
		}
	}
	fmt.Fprintf(w, "  wrote    %s (%s, %d section(s), %d statement(s))\n",
		name, byteSize(int64(len(body))), sections, statements)
	fmt.Fprint(w, indent(preview(string(body), previewLines), "  │ "))
	if out.HarnessOutput != "" {
		fmt.Fprintf(w, "  harness said: %s\n", firstLine(out.HarnessOutput))
	}
}

// describePrompt prints the prompt katana is sending, capped so a large
// existing test file does not bury the terminal.
func describePrompt(w io.Writer, prompt string) {
	fmt.Fprintf(w, "  prompt   %s, %d lines\n", byteSize(int64(len(prompt))), countLines(prompt))
	fmt.Fprint(w, indent(preview(prompt, previewLines), "  │ "))
}

// describeOutcome reports what the harness produced: the size of the file, and
// the test cases katana can see in it — the same index that goes into the
// tracker, so what is narrated is what is recorded.
//
// A case the watcher already named as it was written is not repeated here; the
// count on the first line is the whole index either way.
func describeOutcome(w io.Writer, name, body, language string, out *generator.Outcome, watch *testWatcher) {
	names := testindex.Names(body, language)
	fmt.Fprintf(w, "  wrote    %s (%s, %d lines, %d test case(s))\n",
		name, byteSize(int64(len(body))), countLines(body), len(names))
	for _, n := range names {
		if watch.announced(n) {
			continue
		}
		fmt.Fprintf(w, "    • %s\n", n)
	}
	if out.HarnessOutput != "" {
		fmt.Fprintf(w, "  harness said: %s\n", firstLine(out.HarnessOutput))
	}
}

// preview returns the first n lines of s, noting how many were left out.
func preview(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n") + "\n"
	}
	return strings.Join(lines[:n], "\n") +
		fmt.Sprintf("\n… %d more lines …\n", len(lines)-n)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n") + "\n"
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " …"
	}
	const max = 120
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func byteSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
