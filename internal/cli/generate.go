package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/generator"
	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/testindex"
	"github.com/adaptive-scale/katana/internal/tracker"
)

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("katana generate", flag.ContinueOnError)
	var only stringList
	var jobs int
	var (
		dir     = fs.String("dir", "", "project directory (defaults to the current directory)")
		force   = fs.Bool("force", false, "regenerate every behavior, including up-to-date and hand-edited ones")
		dryRun  = fs.Bool("dry-run", false, "report what would be generated without running the harness")
		verbose = fs.Bool("verbose", false, "show what is being generated: spec, target, harness command, prompt and the harness output as it runs")
	)
	fs.Var(&only, "file", "limit to this behavior file (repeatable)")
	fs.IntVar(&jobs, "jobs", 0, "generate this many behaviors at once (default: harness.jobs in katana.yaml, else 4)")
	fs.IntVar(&jobs, "j", 0, "shorthand for --jobs")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana generate [flags]

Generates tests for behaviors that changed since the last run. A behavior whose
generated file was edited by hand is reported and skipped unless --force is set,
so katana never silently discards your edits.

Behaviors are generated several at a time, since each one waits on an agent CLI
rather than on this machine. Each behavior's output is printed as one block when
it finishes, so concurrent agents never interleave. --jobs 1 runs them one after
another; --verbose implies that unless --jobs says otherwise.

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

	want := cfg.HarnessJobs()
	if jobsSet {
		want = jobs
	}
	workers, note := resolveJobs(want, len(todo), *verbose && !jobsSet)
	if note != "" {
		fmt.Printf("note: %s\n", note)
	}

	// A missing or misconfigured harness affects every behavior that uses it, so
	// it is worth finding out about before spending minutes of agent time rather
	// than after — and once, rather than once per behavior.
	runners := newRunnerCache(cfg, *verbose)
	harnesses := make([]string, 0, len(todo))
	for _, it := range todo {
		harnesses = append(harnesses, it.Harness)
	}
	if err := runners.warm(harnesses); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if workers > 1 {
		fmt.Printf("generating %d behavior(s), %d at a time\n", len(todo), workers)
	} else {
		fmt.Printf("generating %d behavior(s)\n", len(todo))
	}

	prog := newProgress(os.Stdout, os.Stderr, len(todo), workers == 1)

	var generated, failures int
	runPool(ctx, workers, todo,
		func(ctx context.Context, it item) generation {
			return generateOne(ctx, cfg, runners, prog, it, *verbose)
		},
		func(res generation) {
			if res.err != nil {
				failures++
				return
			}
			generated++
			t.Record(tracker.Entry{
				Source:        res.item.Source,
				SourceHash:    res.item.SourceHash,
				Output:        res.item.Output,
				OutputHash:    res.outHash,
				Tests:         res.tests,
				Language:      res.item.Language,
				Framework:     res.item.Framework,
				Harness:       res.item.Harness,
				GeneratedAt:   time.Now().UTC(),
				KatanaVersion: Version,
			})
			// Persist after each success so an interrupted run does not lose the
			// work already done.
			saveTracker(t)
		})

	if ctx.Err() != nil {
		fmt.Println("  interrupted; stopping")
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
	if generated < len(todo) {
		// An interrupt leaves whatever had not been started yet untouched.
		fmt.Printf("generated %d of %d behavior(s)\n", generated, len(todo))
		return nil
	}
	fmt.Printf("generated %d behavior(s)\n", len(todo))
	return nil
}

// resolveJobs settles how many behaviors are generated at once, given what was
// asked for and how many there are to do. It returns the worker count and, when
// that is fewer than requested for a reason the user cannot see, a note saying
// why.
//
// verboseDefault is set when --verbose was given and --jobs was not: live
// narration only reads as narration when one generation is producing it.
func resolveJobs(want, items int, verboseDefault bool) (int, string) {
	if want < 1 {
		want = 1
	}
	if verboseDefault && want > 1 && items > 1 {
		return 1, "--verbose narrates one behavior at a time; pass --jobs N to generate in parallel"
	}
	if want > items {
		want = items
	}
	return want, ""
}

// generation is the outcome of one behavior. Results are collected on the main
// goroutine so the tracker is only ever written from one place.
type generation struct {
	item    item
	outcome *generator.Outcome
	outHash string
	tests   []string
	elapsed time.Duration
	err     error
}

// generateOne generates a single behavior and reports it as one block.
func generateOne(ctx context.Context, cfg *config.Config, runners *runnerCache, prog *progress, it item, verbose bool) generation {
	lg := prog.begin(it.task())
	res := runBehavior(ctx, cfg, runners, lg, it, verbose)

	if res.err != nil {
		fmt.Fprintf(lg.errOut, "  failed: %v\n", res.err)
	} else {
		via := "written by harness"
		switch {
		case res.outcome.FromStdout:
			via = "recovered from harness stdout"
		case res.outcome.Unchanged:
			via = "unchanged; harness judged existing tests sufficient"
		}
		cases := ""
		if n := len(res.tests); n > 0 {
			cases = fmt.Sprintf("%d test case(s), ", n)
		}
		fmt.Fprintf(lg.out, "  ok: %d bytes, %s%s, %s\n",
			res.outcome.Bytes, cases, via, res.elapsed.Round(time.Millisecond))
	}
	prog.finish(it.task(), lg)
	return res
}

// runBehavior does the work for one behavior, writing any narration to lg.
func runBehavior(ctx context.Context, cfg *config.Config, runners *runnerCache, lg genLog, it item, verbose bool) generation {
	res := generation{item: it}

	runner, err := runners.get(it.Harness)
	if err != nil {
		res.err = err
		return res
	}
	source, err := os.ReadFile(it.AbsSource(cfg.Root))
	if err != nil {
		res.err = err
		return res
	}
	// A buffered block owns its own stream; a live one keeps streaming harness
	// output where it has always gone.
	if verbose && lg.buffered() {
		runner = runner.WithStderr(lg.out)
	}

	gen := generator.New(runner, cfg.Root)
	if verbose {
		describeRequest(lg.out, cfg.Root, runner, it, string(source))
		gen.OnPrompt = func(prompt string) {
			describePrompt(lg.out, prompt)
			fmt.Fprintln(lg.out, "  running harness…")
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
	res.elapsed = time.Since(start)
	if err != nil {
		res.err = err
		return res
	}
	// The generated file is read once and used three times: to narrate what came
	// back, to hash it, and to index the tests it declares.
	body, outHash, err := readGenerated(it.AbsOutput(cfg.Root))
	if err != nil {
		res.err = err
		return res
	}
	if verbose {
		describeOutcome(lg.out, it.Output, body, it.Language, out)
	}
	res.outcome, res.outHash = out, outHash
	res.tests = testindex.Names(body, it.Language)
	return res
}

// readGenerated returns the generated file's contents and its hash. A file that
// is not there hashes to the empty string, as tracker.HashFile does: the
// generator has already failed the behavior if nothing was written, so the only
// way to get here without a file is one that vanished under the run, and
// "output missing" on the next status is a better report of that than an error
// against a generation that did work.
func readGenerated(path string) (body, hash string, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return string(b), tracker.HashBytes(b), nil
}

// genLog is where one behavior's output goes. With several workers both streams
// are the same buffer, so the block stays in the order it was written; with one
// they are the process's own stdout and stderr.
type genLog struct {
	out    io.Writer
	errOut io.Writer
	buf    *bytes.Buffer
}

func (l genLog) buffered() bool { return l.buf != nil }

// progress serializes the output of concurrent generations.
//
// With one worker, output streams to the terminal as it happens, which is what
// --verbose is for. With more, each behavior's narration is held in its own
// buffer and printed as one block when that behavior finishes, so two agents
// working at once never interleave mid-line.
type progress struct {
	mu     sync.Mutex
	out    io.Writer
	errOut io.Writer
	total  int
	done   int
	live   bool
}

func newProgress(out, errOut io.Writer, total int, live bool) *progress {
	return &progress{out: out, errOut: errOut, total: total, live: live}
}

// task is the one-line identity of a unit of work: what it was read from, what
// it writes, and why it is being done.
type task struct {
	source string
	output string
	status string
}

func (i item) task() task {
	return task{source: i.Source, output: i.Output, status: i.Status.String()}
}

// begin announces that a piece of work has been picked up and returns the log
// its own output belongs on.
func (p *progress) begin(t task) genLog {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.live {
		p.done++
		fmt.Fprintf(p.out, "[%d/%d] %s → %s (%s)\n", p.done, p.total, t.source, t.output, t.status)
		return genLog{out: p.out, errOut: p.errOut}
	}
	// One line, so the user can see what is in flight without waiting for the
	// first agent to finish.
	fmt.Fprintf(p.out, "  start %s → %s (%s)\n", t.source, t.output, t.status)
	buf := &bytes.Buffer{}
	return genLog{out: buf, errOut: buf, buf: buf}
}

// finish prints everything the work had to say as a single block. The counter
// follows completion order, which is the only order a parallel run has.
func (p *progress) finish(t task, lg genLog) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !lg.buffered() {
		return // already on screen, in the order it happened
	}
	p.done++
	fmt.Fprintf(p.out, "[%d/%d] %s → %s (%s)\n", p.done, p.total, t.source, t.output, t.status)
	p.out.Write(lg.buf.Bytes())
}

// runnerCache builds one Runner per harness name and shares it across workers;
// most projects use exactly one. A Runner holds no mutable state, so only the
// map itself needs guarding. A harness that failed to build is remembered too,
// so a missing executable is looked up once rather than once per behavior.
type runnerCache struct {
	mu      sync.Mutex
	cfg     *config.Config
	verbose bool
	runners map[string]*harness.Runner
	errs    map[string]error
}

func newRunnerCache(cfg *config.Config, verbose bool) *runnerCache {
	return &runnerCache{
		cfg:     cfg,
		verbose: verbose,
		runners: map[string]*harness.Runner{},
		errs:    map[string]error{},
	}
}

// warm builds the runner for every harness the planned work needs, so a harness
// that cannot run at all is reported before any behavior is started.
func (c *runnerCache) warm(names []string) error {
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if _, err := c.get(n); err != nil {
			return err
		}
	}
	return nil
}

func (c *runnerCache) get(name string) (*harness.Runner, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if r, ok := c.runners[name]; ok {
		return r, nil
	}
	if err, ok := c.errs[name]; ok {
		return nil, err
	}
	r, err := newRunner(c.cfg, name, c.verbose)
	if err == nil {
		err = r.Available()
	}
	if err != nil {
		c.errs[name] = err
		return nil, err
	}
	c.runners[name] = r
	return r, nil
}

// saveTracker persists progress mid-run; a failure to write is reported but
// does not abort generation already in flight.
func saveTracker(t *tracker.Tracker) {
	if err := t.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not update tracker: %v\n", err)
	}
}
