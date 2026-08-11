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
	"github.com/adaptive-scale/katana/internal/plan"
	"github.com/adaptive-scale/katana/internal/testindex"
	"github.com/adaptive-scale/katana/internal/tracker"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runGenerate(args []string) error {
	fs := flag.NewFlagSet("katana generate", flag.ContinueOnError)
	var only stringList
	var jobs int
	var (
		dir     = fs.String("dir", "", "project directory (defaults to the current directory)")
		force   = fs.Bool("force", false, "regenerate every behavior, including up-to-date ones and those whose tests katana did not write")
		dryRun  = fs.Bool("dry-run", false, "report what would be generated without running the harness")
		verbose = fs.Bool("verbose", false, "show what is being generated: spec, target, harness command, prompt, each test case as it is written, and the harness output as it runs")
		color   = fs.String("color", "auto", "colour the output: auto, always or never")
	)
	fs.Var(&only, "file", "limit to this behavior file (repeatable)")
	fs.IntVar(&jobs, "jobs", 0, "generate this many behaviors at once (default: harness.jobs in katana.yaml, else 4)")
	fs.IntVar(&jobs, "j", 0, "shorthand for --jobs")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana generate [flags]

Generates tests for behaviors that changed since the last run. A behavior whose
tests are already there and whose specification has not changed since they were
generated is left alone.

A test file katana did not write — edited by hand, or already present for a
behavior the tracker has no record of — is reported and skipped unless --force
is set, so katana never silently discards work it did not do.

Behaviors are generated several at a time, since each one waits on an agent CLI
rather than on this machine. Each behavior's output is printed as one block when
it finishes, so concurrent agents never interleave. --jobs 1 runs them one after
another; --verbose implies that unless --jobs says otherwise.

--verbose narrates each generation — the specification being read, the file being
written, the harness command line, the prompt katana sends, the harness's own
output as it runs, and each test case by name as the harness writes it, so a long
generation says what it is producing rather than only what it produced.

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
	mode, err := ui.ParseMode(*color)
	if err != nil {
		return err
	}
	ui.SetMode(mode)
	p := ui.For(os.Stdout)

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan.Build(cfg, t, only)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no behaviors matched")
		return nil
	}

	// Neighbours are every configured behavior, not only the ones being
	// generated: the names a --file run must not collide with are mostly in the
	// files it is deliberately leaving alone.
	all := items
	if len(only) > 0 {
		if all, err = plan.Build(cfg, t, nil); err != nil {
			return err
		}
	}

	var todo []plan.Item
	var skipped []plan.Item
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

	// Some skips are "nothing to do" and some are "katana is holding back from a
	// file it did not write". Only the second kind is worth a line, and only the
	// second kind makes "all up to date" the wrong thing to say afterwards.
	held := 0
	for _, it := range skipped {
		switch it.Status {
		case tracker.StatusOutputModified, tracker.StatusOutputUntracked:
			held++
			fmt.Printf("  %s  %s → %s (%s; pass --force to regenerate over it)\n",
				p.Yellow("skip"), it.Source, it.Output, p.StatusText(it.Status))
		}
	}

	if len(todo) == 0 {
		if held > 0 {
			fmt.Printf("nothing to generate: %d behavior(s) up to date, %d left alone (pass --force to regenerate over them)\n",
				len(items)-held, held)
			return nil
		}
		fmt.Printf("all %d behavior(s) up to date\n", len(items))
		return nil
	}

	if *dryRun {
		fmt.Printf("%d behavior(s) would be generated:\n", len(todo))
		table := ui.NewTable("STATUS", "BEHAVIOR", "TESTS", "STACK")
		for _, it := range todo {
			table.Row(p.StatusText(it.Status), it.Source, it.Output, p.Dim(it.Stack()))
		}
		return table.MaxWidth(ui.TerminalWidth(os.Stdout)).Render(os.Stdout, p)
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

	// Read once, before anything is overwritten: what the neighbours declare now
	// is what a generation starting now has to avoid. Behaviors generated
	// concurrently are read as they stood at the start, which guards the names
	// they keep; the check after the run is what catches the rest.
	declared := scanDeclared(cfg.Root, all)

	var generated, failures int
	runPool(ctx, workers, todo,
		func(ctx context.Context, it plan.Item) generation {
			return generateOne(ctx, cfg, runners, prog, it, reservedFor(declared, it.Output), *verbose)
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

	// Said after the tracker is written, because a collision does not undo the
	// generation: the files are on disk and recorded, and what is wrong with
	// them is the pair, not either one.
	reportClashes(p, duplicateTests(scanDeclared(cfg.Root, all)))

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

// reportClashes names the test cases that ended up declared by two files in one
// directory. It is a warning rather than a failure: both generations did what
// they were asked, and the file to change is a judgement about which
// specification owns the name.
//
// It is worth saying loudly all the same. In Go this is the difference between
// a package that runs and a package that does not compile, and a package that
// does not compile reports no failing test — it reports nothing at all, while
// every behavior mapped into it still reads as up to date.
func reportClashes(p ui.Printer, clashes []clash) {
	if len(clashes) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n%s %d test name(s) are declared by more than one file in the same directory:\n",
		p.Yellow("warning:"), len(clashes))
	for _, c := range clashes {
		fmt.Fprintf(os.Stderr, "  %s\n", c.Name)
		for _, f := range c.Files {
			fmt.Fprintf(os.Stderr, "    %s\n", p.Dim(f))
		}
	}
	fmt.Fprintln(os.Stderr, "  where a directory is one namespace, such as a Go package, this stops it")
	fmt.Fprintln(os.Stderr, "  compiling and nothing in it runs. Reword one of the specifications so the")
	fmt.Fprintln(os.Stderr, "  two name different cases, then regenerate it with --force.")
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
	item    plan.Item
	outcome *generator.Outcome
	outHash string
	tests   []string
	elapsed time.Duration
	err     error
}

// generateOne generates a single behavior and reports it as one block.
func generateOne(ctx context.Context, cfg *config.Config, runners *runnerCache, prog *progress, it plan.Item, reserved []string, verbose bool) generation {
	lg := prog.begin(taskFor(it))
	res := runBehavior(ctx, cfg, runners, lg, it, reserved, verbose)

	if res.err != nil {
		fmt.Fprintf(lg.errOut, "  %s %v\n", prog.p.Red("failed:"), res.err)
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
		fmt.Fprintf(lg.out, "  %s %d bytes, %s%s, %s\n",
			prog.p.Green("ok:"), res.outcome.Bytes, cases, via, res.elapsed.Round(time.Millisecond))
	}
	prog.finish(taskFor(it), lg)
	return res
}

// runBehavior does the work for one behavior, writing any narration to lg.
func runBehavior(ctx context.Context, cfg *config.Config, runners *runnerCache, lg genLog, it plan.Item, reserved []string, verbose bool) generation {
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

	// The harness writes the test file as it goes, so watching it says which
	// cases have landed while the agent is still working on the rest.
	var watch *testWatcher
	if verbose {
		watch = watchTests(lg.out, it.AbsOutput(cfg.Root), it.Language)
	}

	start := time.Now()
	out, err := gen.Generate(ctx, generator.Request{
		BehaviorPath:      it.Source,
		BehaviorContent:   string(source),
		OutputPath:        it.Output,
		Language:          it.Language,
		Framework:         it.Framework,
		Reserved:          reserved,
		ExtraInstructions: it.Instructions,
	})
	res.elapsed = time.Since(start)
	if watch != nil {
		// Stopped before anything else writes to lg.out, so the watcher is the
		// only thing narrating for as long as it is running.
		watch.stop()
	}
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
		describeOutcome(lg.out, it.Output, body, it.Language, out, watch)
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

// syncWriter serializes writes to one destination. A verbose generation has two
// writers going at once — the harness's own output, copied by os/exec, and the
// watcher naming test cases as they land — and a bytes.Buffer tolerates neither
// concurrently nor mid-line.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

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
	p      ui.Printer
	total  int
	done   int
	live   bool
}

func newProgress(out, errOut io.Writer, total int, live bool) *progress {
	return &progress{out: out, errOut: errOut, p: ui.For(out), total: total, live: live}
}

// task is the one-line identity of a unit of work: what it was read from, what
// it writes, and why it is being done.
type task struct {
	source string
	output string
	status string
}

// taskFor is the one-line identity of the work a behavior needs.
func taskFor(i plan.Item) task {
	return task{source: i.Source, output: i.Output, status: i.Status.String()}
}

// begin announces that a piece of work has been picked up and returns the log
// its own output belongs on.
func (p *progress) begin(t task) genLog {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.live {
		p.done++
		fmt.Fprintln(p.out, p.headline(t))
		return genLog{out: p.out, errOut: p.errOut}
	}
	// One line, so the user can see what is in flight without waiting for the
	// first agent to finish.
	fmt.Fprintf(p.out, "  %s %s → %s (%s)\n", p.p.Dim("start"), t.source, t.output, p.p.Dim(t.status))
	buf := &bytes.Buffer{}
	w := &syncWriter{w: buf}
	return genLog{out: w, errOut: w, buf: buf}
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
	fmt.Fprintln(p.out, p.headline(t))
	p.out.Write(lg.buf.Bytes())
}

// headline is the line that announces one behavior's generation, counted
// against the total so a long run says how far along it is.
func (p *progress) headline(t task) string {
	return fmt.Sprintf("%s %s → %s (%s)",
		p.p.Bold(fmt.Sprintf("[%d/%d]", p.done, p.total)), t.source, t.output, p.p.Dim(t.status))
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
