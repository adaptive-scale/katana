package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/adaptive-scale/katana/internal/config"
	"github.com/adaptive-scale/katana/internal/tui"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runTUI(args []string) error {
	fs := flag.NewFlagSet("katana tui", flag.ContinueOnError)
	var (
		dir      = fs.String("dir", "", "project directory (defaults to the current directory)")
		snapshot = fs.Bool("snapshot", false, "print one frame and exit, for a terminal katana cannot take over")
		width    = fs.Int("width", 0, "columns to render a --snapshot at (default: the terminal's, else 110)")
		height   = fs.Int("height", 0, "rows to render a --snapshot at (default: the terminal's, else 40)")
		color    = fs.String("color", "auto", "colour the output: auto, always or never")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana tui [flags]

Opens katana's full-screen view of the project, which is also what `+"`katana`"+` on
its own does in a terminal.

The list is every behavior with its status, how many test cases it maps to, how
many of them passed in the last run, and a column per recent run showing how it
has been doing. Enter opens a behavior: its test cases with how each one last
fared, and a bar per run of the ones before.

r runs the selected behavior's tests and a runs the whole suite, streaming the
runner's output as it comes. A run is recorded exactly as `+"`katana run`"+` records
one, and the list updates from it the moment it finishes — so what is on screen
is what the next `+"`katana status`"+` would say.

Narrowing a run to one behavior needs a runner katana knows how to narrow: by
test name for `+"`go test`"+`, by file for pytest, jest, vitest and mocha. Anything
else runs the whole suite and says so.

--snapshot prints one frame and exits, for a log or a terminal that is not one.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, err := ui.ParseMode(*color)
	if err != nil {
		return err
	}
	ui.SetMode(mode)

	cfg, err := loadProject(*dir)
	if err != nil {
		return err
	}
	if *snapshot {
		w, h, ok := ui.TerminalSize(os.Stdout)
		if !ok {
			w, h = 110, 40
		}
		if *width > 0 {
			w = *width
		}
		if *height > 0 {
			h = *height
		}
		return tui.Snapshot(cfg, os.Stdout, w, h)
	}
	return tui.Run(cfg)
}

// runDefault is `katana` with nothing after it. In a terminal, inside a project,
// that is a request to see the project — the UI is a better answer than a wall
// of usage text. Anywhere else, and anywhere there is no katana.yaml to show,
// the usage is still what a bare invocation prints.
func runDefault() error {
	if !ui.IsTerminal(os.Stdout) || !ui.IsTerminal(os.Stdin) {
		usage(os.Stdout)
		return nil
	}
	cfg, err := loadProject("")
	if err != nil {
		usage(os.Stdout)
		fmt.Printf("\nNo %s here yet — run `katana init` to start one.\n", config.FileName)
		return nil
	}
	return tui.Run(cfg)
}
