package cli

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/adaptive-scale/katana/internal/tracker"
)

func runStatus(args []string) error {
	fs := flag.NewFlagSet("katana status", flag.ContinueOnError)
	var only stringList
	var (
		dir    = fs.String("dir", "", "project directory (defaults to the current directory)")
		strict = fs.Bool("strict", false, "exit non-zero when any behavior is out of date")
	)
	fs.Var(&only, "file", "limit to this behavior file (repeatable)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana status [flags]

Shows which behaviors are out of date with respect to their generated tests.

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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tBEHAVIOR\tTESTS\tSTACK")
	stale := 0
	for _, it := range items {
		if it.Status != tracker.StatusUpToDate {
			stale++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s/%s via %s\n",
			it.Status, it.Source, it.Output, it.Language, it.Framework, it.Harness)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Printf("\n%d behavior(s), %d out of date\n", len(items), stale)
	if stale > 0 {
		fmt.Println("run `katana generate` to bring them up to date")
		if *strict {
			return fmt.Errorf("%d behavior(s) out of date", stale)
		}
	}
	return nil
}
