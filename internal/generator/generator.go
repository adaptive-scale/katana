// Package generator turns a behavior specification into a test file by
// delegating to a configured harness.
package generator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/adaptive-scale/katana/internal/fence"
	"github.com/adaptive-scale/katana/internal/harness"
)

// Generator produces test files for behaviors using one harness.
type Generator struct {
	runner *harness.Runner
	root   string

	// OnPrompt, when set, is called with the finished prompt just before the
	// harness is invoked. `katana generate --verbose` uses it to show what is
	// being asked for while the harness is still working on it.
	OnPrompt func(prompt string)
}

// New returns a Generator that runs runner with root as the working directory.
func New(runner *harness.Runner, root string) *Generator {
	return &Generator{runner: runner, root: root}
}

// Outcome reports what a single generation produced.
type Outcome struct {
	// WroteFile is true when the harness wrote the target file itself.
	WroteFile bool
	// FromStdout is true when katana had to fall back to the harness's stdout.
	FromStdout bool
	// Unchanged is true when the file came back byte-identical, which means the
	// harness judged the existing tests to already satisfy the specification.
	Unchanged bool
	// Bytes is the size of the generated file.
	Bytes int
	// HarnessOutput is the harness's own reply, kept for diagnostics.
	HarnessOutput string
}

// Generate runs the harness for one behavior and ensures the output file exists.
//
// The harness is asked to write the file directly. If it did not — some agents
// print instead of writing, depending on their permission mode — katana falls
// back to treating stdout as the file body.
func (g *Generator) Generate(ctx context.Context, req Request) (*Outcome, error) {
	absOutput := filepath.Join(g.root, filepath.FromSlash(req.OutputPath))

	before, err := readIfExists(absOutput)
	if err != nil {
		return nil, err
	}
	req.ExistingTests = before

	prompt := BuildPrompt(req)
	if g.OnPrompt != nil {
		g.OnPrompt(prompt)
	}

	res, runErr := g.runner.Run(ctx, prompt)
	if runErr != nil {
		return nil, runErr
	}

	after, err := readIfExists(absOutput)
	if err != nil {
		return nil, err
	}

	// Preferred path: the file is in place after a clean harness exit.
	//
	// Byte-identical content counts as success, not failure. A harness that
	// reads an updated specification and concludes the existing tests already
	// cover it has done its job; failing here would leave the behavior wedged
	// as permanently out of date, since the tracker would never advance.
	if after != "" {
		return &Outcome{
			WroteFile:     true,
			Unchanged:     after == before,
			Bytes:         len(after),
			HarnessOutput: strings.TrimSpace(res.Stdout),
		}, nil
	}

	// Fallback: recover the file body from stdout.
	body := extractCode(res.Stdout)
	if body != "" {
		if err := writeFile(absOutput, body); err != nil {
			return nil, err
		}
		return &Outcome{
			FromStdout:    true,
			Bytes:         len(body),
			HarnessOutput: strings.TrimSpace(res.Stdout),
		}, nil
	}

	err = fmt.Errorf("harness %q did not write %s and printed no test code; harness said: %s",
		g.runner.Spec().Name, req.OutputPath, summarize(res.Stdout, res.Stderr))
	if hint := harness.PermissionHint(g.runner.Spec().Name, res.Stdout, res.Stderr); hint != "" {
		err = fmt.Errorf("%w\n  %s", err, hint)
	}
	return nil, err
}

// extractCode pulls a file body out of harness stdout.
//
// It prefers the largest fenced code block, since agents wrap code in fences and
// surround it with prose. With no fence, stdout is used verbatim only when it
// does not look like conversational prose.
func extractCode(stdout string) string {
	if best := fence.Largest(stdout); best != "" {
		return strings.TrimSpace(best) + "\n"
	}

	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || !looksLikeCode(trimmed) {
		return ""
	}
	return trimmed + "\n"
}

// codeMarkers are line prefixes that only appear in source, never in an agent's
// conversational reply. They span the languages katana generates for.
var codeMarkers = []string{
	"package ", "import ", "from ", "def ", "func ", "class ", "fn ",
	"#include", "using ", "namespace ", "module ", "require ", "@Test",
	"describe(", "it(", "test(", "const ", "let ", "var ", "public ",
	"private ", "internal ", "assert", "expect(", "self.", "#[",
}

// looksLikeCode reports whether unfenced stdout is a source file rather than an
// agent's confirmation message.
//
// The check is deliberately asymmetric: a false negative costs a clear error
// telling the user the harness produced nothing usable, while a false positive
// writes English into a test file and fails at compile time with a confusing
// message. So this requires positive evidence of code, not merely the absence
// of prose.
func looksLikeCode(s string) bool {
	lines := strings.Split(s, "\n")
	if len(lines) < 3 {
		return false // a one-line confirmation is not a file
	}

	nonEmpty, codeish := 0, 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		nonEmpty++

		// Structural punctuation and indentation are strong signals in every
		// language katana targets.
		if strings.HasSuffix(t, "{") || strings.HasSuffix(t, "}") ||
			strings.HasSuffix(t, ";") || strings.HasSuffix(t, ":") ||
			t == "}" || t == ")" || strings.HasPrefix(l, "\t") ||
			strings.HasPrefix(l, "    ") {
			codeish++
			continue
		}
		for _, m := range codeMarkers {
			if strings.HasPrefix(t, m) {
				codeish++
				break
			}
		}
	}
	if nonEmpty == 0 {
		return false
	}
	// A real source file is overwhelmingly structural. Requiring a third of its
	// lines to look like code accepts well-commented tests while rejecting a
	// paragraph that happens to contain one code-like line.
	return codeish >= 3 && codeish*3 >= nonEmpty
}

func summarize(stdout, stderr string) string {
	out := strings.TrimSpace(stdout)
	if out == "" {
		out = strings.TrimSpace(stderr)
	}
	if out == "" {
		return "(no output)"
	}
	const max = 400
	out = strings.ReplaceAll(out, "\n", " ")
	if len(out) > max {
		out = out[:max] + "..."
	}
	return out
}

func readIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
