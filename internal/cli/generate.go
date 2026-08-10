package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adaptive-scale/katana/internal/generator"
	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("katana generate", flag.ContinueOnError)
	var only stringList
	var (
		dir     = fs.String("dir", "", "project directory (defaults to the current directory)")
		force   = fs.Bool("force", false, "regenerate every behavior, including up-to-date and hand-edited ones")
		dryRun  = fs.Bool("dry-run", false, "report what would be generated without running the harness")
		verbose = fs.Bool("verbose", false, "show what is being generated: spec, target, harness command, prompt and the harness output as it runs")
	)
	fs.Var(&only, "file", "limit to this behavior file (repeatable)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana generate [flags]

Generates tests for behaviors that changed since the last run. A behavior whose
generated file was edited by hand is reported and skipped unless --force is set,
so katana never silently discards your edits.

--verbose narrates each generation — the specification being read, the file being
written, the harness command line, the prompt katana sends, the harness's own
output as it runs, and a preview of the tests that came back.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan(cfg, t, only)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no behaviors matched")
		return nil
	}

	var todo []item
	var skipped []item
	for _, it := range items {
		switch {
		case *force:
			todo = append(todo, it)
		case it.Status.NeedsGeneration():
			todo = append(todo, it)
		default:
			skipped = append(skipped, it)
		}
	}

	for _, it := range skipped {
		if it.Status == tracker.StatusOutputModified {
			fmt.Printf("  skip  %s → %s (%s; pass --force to regenerate over it)\n",
				it.Source, it.Output, it.Status)
		}
	}

	if len(todo) == 0 {
		fmt.Printf("all %d behavior(s) up to date\n", len(items))
		return nil
	}

	if *dryRun {
		fmt.Printf("%d behavior(s) would be generated:\n", len(todo))
		for _, it := range todo {
			fmt.Printf("  %-22s %s → %s [%s/%s via %s]\n",
				it.Status, it.Source, it.Output, it.Language, it.Framework, it.Harness)
		}
		return nil
	}

	// Reuse one runner per harness name; most projects use exactly one.
	runners := map[string]*harness.Runner{}
	getRunner := func(name string) (*harness.Runner, error) {
		if r, ok := runners[name]; ok {
			return r, nil
		}
		r, err := newRunner(cfg, name, *verbose)
		if err != nil {
			return nil, err
		}
		if err := r.Available(); err != nil {
			return nil, err
		}
		runners[name] = r
		return r, nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("generating %d behavior(s)\n", len(todo))
	var failures int

	for i, it := range todo {
		fmt.Printf("[%d/%d] %s → %s (%s)\n", i+1, len(todo), it.Source, it.Output, it.Status)

		if ctx.Err() != nil {
			fmt.Println("  interrupted; stopping")
			break
		}

		runner, err := getRunner(it.Harness)
		if err != nil {
			// A missing or misconfigured harness affects every remaining
			// behavior, so there is nothing to gain from continuing.
			saveTracker(t)
			return err
		}

		source, err := os.ReadFile(it.AbsSource(cfg.Root))
		if err != nil {
			return err
		}

		gen := generator.New(runner, cfg.Root)
		if *verbose {
			describeRequest(os.Stdout, cfg.Root, runner, it, string(source))
			gen.OnPrompt = func(prompt string) {
				describePrompt(os.Stdout, prompt)
				fmt.Println("  running harness…")
			}
		}

		start := time.Now()
		out, err := gen.Generate(ctx, generator.Request{
			BehaviorPath:      it.Source,
			BehaviorContent:   string(source),
			OutputPath:        it.Output,
			Language:          it.Language,
			Framework:         it.Framework,
			ExtraInstructions: it.Instructions,
		})
		if err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "  failed: %v\n", err)
			continue
		}
		if *verbose {
			describeOutcome(os.Stdout, it.Output, it.AbsOutput(cfg.Root), out)
		}

		outHash, err := tracker.HashFile(it.AbsOutput(cfg.Root))
		if err != nil {
			return err
		}
		t.Record(tracker.Entry{
			Source:        it.Source,
			SourceHash:    it.SourceHash,
			Output:        it.Output,
			OutputHash:    outHash,
			Language:      it.Language,
			Framework:     it.Framework,
			Harness:       it.Harness,
			GeneratedAt:   time.Now().UTC(),
			KatanaVersion: Version,
		})
		// Persist after each success so an interrupted run does not lose the
		// work already done.
		saveTracker(t)

		via := "written by harness"
		switch {
		case out.FromStdout:
			via = "recovered from harness stdout"
		case out.Unchanged:
			via = "unchanged; harness judged existing tests sufficient"
		}
		fmt.Printf("  ok: %d bytes, %s, %s\n", out.Bytes, via, time.Since(start).Round(time.Millisecond))
	}

	// Behaviors deleted from the config leave stale tracker entries behind.
	if len(only) == 0 {
		if resolved, err := cfg.Resolve(); err == nil {
			keep := make(map[string]bool, len(resolved))
			for _, r := range resolved {
				keep[r.Source] = true
			}
			for _, gone := range t.Prune(keep) {
				fmt.Printf("  pruned tracker entry for removed behavior %s\n", gone)
			}
		}
	}
	if err := t.Save(); err != nil {
		return err
	}

	if failures > 0 {
		return fmt.Errorf("%d of %d behavior(s) failed to generate", failures, len(todo))
	}
	fmt.Printf("generated %d behavior(s)\n", len(todo))
	return nil
}

// saveTracker persists progress mid-run; a failure to write is reported but
// does not abort generation already in flight.
func saveTracker(t *tracker.Tracker) {
	if err := t.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not update tracker: %v\n", err)
	}
}
