package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/adaptive-scale/katana/internal/tracker"
)

func runTest(args []string) error {
	fs := flag.NewFlagSet("katana run", flag.ContinueOnError)
	var (
		dir   = fs.String("dir", "", "project directory (defaults to the current directory)")
		check = fs.Bool("check", false, "fail before running if any behavior is out of date")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana run [flags] [-- extra args]

Runs the test command from katana.yaml. Arguments after -- are appended to it,
so `+"`katana run -- -run TestCheckout`"+` narrows a Go test run.

katana warns when a behavior has changed since its tests were generated; pass
--check to make that a hard failure instead, which is the useful form in CI.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	extra := fs.Args()

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Test.Command) == "" {
		return errors.New("no test.command set in katana.yaml")
	}

	// Report staleness before running, so a green suite is never mistaken for
	// a suite that covers the current specification.
	t, err := tracker.Load(cfg.Root)
	if err != nil {
		return err
	}
	items, err := plan(cfg, t, nil)
	if err != nil {
		return err
	}
	var stale []item
	for _, it := range items {
		if it.Status != tracker.StatusUpToDate {
			stale = append(stale, it)
		}
	}
	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d behavior(s) out of date with their tests:\n", len(stale))
		for _, it := range stale {
			fmt.Fprintf(os.Stderr, "  %-22s %s\n", it.Status, it.Source)
		}
		fmt.Fprintln(os.Stderr, "  run `katana generate` first")
		if *check {
			return fmt.Errorf("%d behavior(s) out of date (--check)", len(stale))
		}
		fmt.Fprintln(os.Stderr)
	}

	command := cfg.Test.Command
	if len(extra) > 0 {
		command += " " + strings.Join(quoteAll(extra), " ")
	}

	workDir := cfg.Root
	if cfg.Test.Dir != "" {
		workDir = filepath.Join(cfg.Root, filepath.FromSlash(cfg.Test.Dir))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("running: %s\n", command)
	cmd := exec.CommandContext(ctx, shell(), shellFlag(), command)
	cmd.Dir = workDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Propagate the suite's own exit code so CI sees the real result.
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("running test command: %w", err)
	}
	return nil
}

func shell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

func shellFlag() string {
	if runtime.GOOS == "windows" {
		return "/C"
	}
	return "-c"
}

// quoteAll shell-quotes pass-through arguments so paths with spaces survive.
func quoteAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return out
}
