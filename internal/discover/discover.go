package discover

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

// Discoverer writes behavior specifications for source code, by having a
// harness read it.
type Discoverer struct {
	runner *harness.Runner
	root   string

	// OnPrompt, when set, is called with the finished prompt just before the
	// harness is invoked. `katana discover --verbose` uses it to show what is
	// being asked for while the harness is still working on it.
	OnPrompt func(prompt string)
}

// New returns a Discoverer that runs runner with root as the working directory.
func New(runner *harness.Runner, root string) *Discoverer {
	return &Discoverer{runner: runner, root: root}
}

// Outcome reports what one unit produced.
type Outcome struct {
	// WroteFile is true when the harness wrote the behavior file itself.
	WroteFile bool
	// FromStdout is true when katana had to fall back to the harness's stdout.
	FromStdout bool
	// Unchanged is true when an existing specification came back byte-identical,
	// which means the harness read the code and found nothing to correct.
	Unchanged bool
	// Skipped is true when the harness reported that the source states no
	// product behavior worth specifying.
	Skipped bool
	// Reason is why it was skipped.
	Reason string
	// Bytes is the size of the behavior file.
	Bytes int
	// HarnessOutput is the harness's own reply, kept for diagnostics.
	HarnessOutput string
}

// Discover runs the harness over one unit and ensures the behavior file exists.
//
// As in generation, the harness is asked to write the file itself and katana
// falls back to reading its stdout. Discovery adds a third ending: a unit whose
// source states no behavior is skipped rather than described, so a package of
// constants does not become a specification full of invented rules.
func (d *Discoverer) Discover(ctx context.Context, req Request) (*Outcome, error) {
	absOutput := filepath.Join(d.root, filepath.FromSlash(req.Unit.Output))

	before, err := readIfExists(absOutput)
	if err != nil {
		return nil, err
	}
	req.ExistingBehavior = before

	prompt := BuildPrompt(req)
	if d.OnPrompt != nil {
		d.OnPrompt(prompt)
	}

	res, runErr := d.runner.Run(ctx, prompt)
	if runErr != nil {
		return nil, runErr
	}

	after, err := readIfExists(absOutput)
	if err != nil {
		return nil, err
	}

	// A skip only counts while nothing new is on disk. A harness that wrote a
	// specification and then mentioned skipping something has still written it.
	if after == before {
		if reason := skipReason(res.Stdout); reason != "" {
			return &Outcome{
				Skipped:       true,
				Reason:        reason,
				Bytes:         len(after),
				HarnessOutput: strings.TrimSpace(res.Stdout),
			}, nil
		}
	}

	if after != "" {
		return &Outcome{
			WroteFile:     true,
			Unchanged:     after == before,
			Bytes:         len(after),
			HarnessOutput: strings.TrimSpace(res.Stdout),
		}, nil
	}

	// Fallback: recover the specification from stdout.
	if body := extractMarkdown(res.Stdout); body != "" {
		if err := writeFile(absOutput, body); err != nil {
			return nil, err
		}
		return &Outcome{
			FromStdout:    true,
			Bytes:         len(body),
			HarnessOutput: strings.TrimSpace(res.Stdout),
		}, nil
	}

	err = fmt.Errorf("harness %q did not write %s and printed no specification; harness said: %s",
		d.runner.Spec().Name, req.Unit.Output, summarize(res.Stdout, res.Stderr))
	if hint := harness.PermissionHint(d.runner.Spec().Name, res.Stdout, res.Stderr); hint != "" {
		err = fmt.Errorf("%w\n  %s", err, hint)
	}
	return nil, err
}

// skipReason returns the reason a harness gave for writing nothing, or "" when
// it did not report one.
//
// The marker has to be a line of its own: an agent explaining that it skipped a
// generated file inside a paragraph is commentary, not the reply katana asked
// for, and the file it did write should win.
func skipReason(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#*->` "))
		if !strings.HasPrefix(strings.ToUpper(line), SkipMarker) {
			continue
		}
		// Agents like to put a reply in bold; the emphasis is not the reason.
		reason := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(line[len(SkipMarker):]), "*`_"))
		if reason == "" {
			return "no behavior to specify"
		}
		const max = 200
		if len(reason) > max {
			reason = reason[:max] + "…"
		}
		return reason
	}
	return ""
}

// extractMarkdown pulls a behavior file out of harness stdout.
//
// It prefers the largest fenced block, since that is the shape katana asked for
// when the harness cannot write files. Unfenced stdout is used only when it
// reads as a specification rather than as an agent confirming what it did.
func extractMarkdown(stdout string) string {
	if best := fence.Largest(stdout); best != "" && looksLikeSpec(best) {
		return strings.TrimSpace(best) + "\n"
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || !looksLikeSpec(trimmed) {
		return ""
	}
	return trimmed + "\n"
}

// looksLikeSpec reports whether s is a behavior specification rather than an
// agent's reply about one.
//
// A behavior file is a heading followed by bullets; katana asked for exactly
// that, and the check requires both. The cost of being wrong is asymmetric — a
// false negative is a clear error saying the harness produced nothing usable,
// while a false positive writes an agent's chatter into the behavior file that
// every future test is generated from.
func looksLikeSpec(s string) bool {
	heading, bullets := false, 0
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#"):
			heading = true
		case strings.HasPrefix(t, "- "), strings.HasPrefix(t, "* "):
			bullets++
		}
	}
	return heading && bullets >= 2
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
