package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/discover"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("katana discover", flag.ContinueOnError)
	var paths, exclude stringList
	var jobs int
	var (
		dir          = fs.String("dir", "", "project directory (defaults to the current directory)")
		language     = fs.String("language", "", "language to read (defaults to defaults.language in katana.yaml)")
		out          = fs.String("out", "", "directory to write behavior files into (defaults to where katana.yaml already keeps them)")
		group        = fs.String("group", string(discover.GroupDir), "unit of discovery: "+strings.Join(discover.Groupings(), " or "))
		force        = fs.Bool("force", false, "rewrite behavior files that already exist, updating them against the code")
		includeTests = fs.Bool("include-tests", false, "read test code too, instead of only product code")
		dryRun       = fs.Bool("dry-run", false, "report what would be discovered without running the harness")
		verbose      = fs.Bool("verbose", false, "show what is being read: the files, the harness command, the prompt and the harness output as it runs")
	)
	fs.Var(&paths, "path", "limit discovery to this file or subtree (repeatable)")
	fs.Var(&exclude, "exclude", "skip this directory, by name or path (repeatable)")
	fs.IntVar(&jobs, "jobs", 0, "discover this many units at once (default: harness.jobs in katana.yaml, else 4)")
	fs.IntVar(&jobs, "j", 0, "shorthand for --jobs")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: katana discover [flags]

Reads the code you already have and writes the behavior it implements into your
behaviors directory, so a project with no specifications has somewhere to start.
It is `+"`katana generate`"+` run backwards: discover writes behavior files, generate
turns them into tests.

Only the language configured in %s is read, and only product code —
test files, dependencies, build output and generated files are left out. Source
files are grouped a directory at a time, and the behavior tree mirrors the
source tree, so internal/billing becomes behaviors/internal/billing.md and the
tests generated from it land under the matching directory.

What comes back is a draft. It describes what the code does today, including
whatever it does by accident, so read and correct it before running
`+"`katana generate`"+` — from that point on the behavior file is the source of truth.
Existing behavior files are left alone unless --force is given.

Flags:
`, config.FileName)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	jobsSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "jobs" || f.Name == "j" {
			jobsSet = true
		}
	})
	if jobsSet && jobs < 1 {
		return fmt.Errorf("--jobs must be at least 1, got %d", jobs)
	}

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	lang := config.NormalizeLanguage(firstNonEmpty(*language, cfg.Defaults.Language))
	behaviorDir := firstNonEmpty(*out, cfg.BehaviorDir())

	units, err := discover.Scan(discover.Options{
		Root:         cfg.Root,
		Language:     lang,
		BehaviorDir:  behaviorDir,
		Group:        discover.Grouping(*group),
		Paths:        paths,
		Exclude:      exclude,
		IncludeTests: *includeTests,
	})
	if err != nil {
		return err
	}
	if len(units) == 0 {
		fmt.Printf("no %s source found in %s\n", lang, describeScope(paths))
		fmt.Println("check defaults.language in " + config.FileName + ", or pass --language and --path")
		return nil
	}

	var todo, existing []discover.Unit
	for _, u := range units {
		if _, err := os.Stat(path.Join(cfg.Root, u.Output)); err == nil && !*force {
			existing = append(existing, u)
			continue
		}
		todo = append(todo, u)
	}
	p := ui.For(os.Stdout)
	for _, u := range existing {
		fmt.Printf("  %s  %s → %s (already written; pass --force to update it against the code)\n",
			p.Yellow("skip"), u.Name, u.Output)
	}
	if len(todo) == 0 {
		fmt.Printf("all %d unit(s) already have behavior files\n", len(units))
		return nil
	}

	if *dryRun {
		files := 0
		fmt.Printf("%d behavior file(s) would be discovered:\n", len(todo))
		table := ui.NewTable("UNIT", "BEHAVIOR", "FILES", "SIZE").RightAlign(2, 3)
		for _, u := range todo {
			files += len(u.Files)
			table.Row(u.Name, u.Output, fmt.Sprint(len(u.Files)), p.Dim(byteSize(u.Bytes)))
		}
		if err := table.MaxWidth(ui.TerminalWidth(os.Stdout)).Render(os.Stdout, p); err != nil {
			return err
		}
		fmt.Printf("reading %d %s file(s) with %s\n", files, lang, cfg.Harness.Name)
		return nil
	}

	want := cfg.HarnessJobs()
	if jobsSet {
		want = jobs
	}
	workers, note := resolveJobs(want, len(todo), *verbose && !jobsSet)
	if note != "" {
		fmt.Printf("note: %s\n", note)
	}

	runners := newRunnerCache(cfg, *verbose)
	if err := runners.warm([]string{cfg.Harness.Name}); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if workers > 1 {
		fmt.Printf("discovering %d unit(s), %d at a time\n", len(todo), workers)
	} else {
		fmt.Printf("discovering %d unit(s)\n", len(todo))
	}

	prog := newProgress(os.Stdout, os.Stderr, len(todo), workers == 1)

	var written []string
	var skipped, failures int
	runPool(ctx, workers, todo,
		func(ctx context.Context, u discover.Unit) discovery {
			return discoverOne(ctx, cfg, runners, prog, u, lang, *force, *verbose)
		},
		func(res discovery) {
			switch {
			case res.err != nil:
				failures++
			case res.outcome.Skipped:
				skipped++
			default:
				written = append(written, res.unit.Output)
			}
		})

	if ctx.Err() != nil {
		fmt.Println("  interrupted; stopping")
	}

	sort.Strings(written)
	fmt.Printf("\nwrote %d behavior file(s)", len(written))
	if skipped > 0 {
		fmt.Printf(", %d unit(s) had no behavior to specify", skipped)
	}
	fmt.Println()

	if len(written) > 0 {
		reportUncovered(cfg, written)
		fmt.Println("review what was written — it describes what the code does today, bugs included — then run `katana generate`")
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d unit(s) failed", failures, len(todo))
	}
	return nil
}

// discovery is the outcome of one unit. Results are collected on the main
// goroutine, so the counters are only ever touched from one place.
type discovery struct {
	unit    discover.Unit
	outcome *discover.Outcome
	elapsed time.Duration
	err     error
}

// discoverOne describes a single unit and reports it as one block.
func discoverOne(ctx context.Context, cfg *config.Config, runners *runnerCache, prog *progress, u discover.Unit, language string, force, verbose bool) discovery {
	status := "new"
	if force {
		if _, err := os.Stat(path.Join(cfg.Root, u.Output)); err == nil {
			status = "update"
		}
	}
	t := task{source: u.Name, output: u.Output, status: status}
	lg := prog.begin(t)
	res := runUnit(ctx, cfg, runners, lg, u, language, verbose)

	switch {
	case res.err != nil:
		fmt.Fprintf(lg.errOut, "  failed: %v\n", res.err)
	case res.outcome.Skipped:
		fmt.Fprintf(lg.out, "  skipped: %s\n", res.outcome.Reason)
	default:
		via := "written by harness"
		switch {
		case res.outcome.FromStdout:
			via = "recovered from harness stdout"
		case res.outcome.Unchanged:
			via = "unchanged; harness found nothing to correct"
		}
		fmt.Fprintf(lg.out, "  ok: %d bytes, %s, %s\n",
			res.outcome.Bytes, via, res.elapsed.Round(time.Millisecond))
	}
	prog.finish(t, lg)
	return res
}

// runUnit does the work for one unit, writing any narration to lg.
func runUnit(ctx context.Context, cfg *config.Config, runners *runnerCache, lg genLog, u discover.Unit, language string, verbose bool) discovery {
	res := discovery{unit: u}

	runner, err := runners.get(cfg.Harness.Name)
	if err != nil {
		res.err = err
		return res
	}
	// A buffered block owns its own stream; a live one keeps streaming harness
	// output where it has always gone.
	if verbose && lg.buffered() {
		runner = runner.WithStderr(lg.out)
	}

	d := discover.New(runner, cfg.Root)
	if verbose {
		describeUnit(lg.out, cfg.Root, runner, u, language)
		d.OnPrompt = func(prompt string) {
			describePrompt(lg.out, prompt)
			fmt.Fprintln(lg.out, "  running harness…")
		}
	}

	start := time.Now()
	out, err := d.Discover(ctx, discover.Request{Unit: u, Language: language})
	res.elapsed = time.Since(start)
	if err != nil {
		res.err = err
		return res
	}
	if verbose && !out.Skipped {
		describeSpec(lg.out, cfg.Root, u.Output, out)
	}
	res.outcome = out
	return res
}

// reportUncovered warns when discovered behavior files fall outside every
// configured behaviors path, since `katana generate` would then never see them.
// A config katana cannot resolve says nothing either way, so it says nothing.
func reportUncovered(cfg *config.Config, written []string) {
	resolved, err := cfg.Resolve()
	if err != nil {
		return
	}
	covered := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		covered[r.Source] = true
	}
	var orphans []string
	for _, w := range written {
		if !covered[w] {
			orphans = append(orphans, w)
		}
	}
	if len(orphans) == 0 {
		return
	}
	fmt.Printf("note: %d of them are outside the behaviors configured in %s and will not be generated from.\n",
		len(orphans), config.FileName)
	fmt.Printf("      add `- path: %s` under behaviors: to include them (e.g. %s)\n",
		path.Dir(orphans[0]), orphans[0])
}

// describeScope names where a scan looked, for the message that says it found
// nothing there.
func describeScope(paths []string) string {
	if len(paths) == 0 {
		return "this project"
	}
	return strings.Join(paths, ", ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
