package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/adaptive-scale/katana/internal/harness"
	"github.com/adaptive-scale/katana/internal/ui"
)

func runHarnesses(args []string) error {
	fs := flag.NewFlagSet("katana harnesses", flag.ContinueOnError)
	color := fs.String("color", "auto", "colour the output: auto, always or never")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana harnesses

Lists the coding-agent harnesses katana knows how to drive, their default
invocation, and whether each one is installed on this machine.

Every field is overridable under `+"`harness:`"+` in katana.yaml, and setting
harness.command lets katana drive an agent CLI it has no built-in entry for.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode, err := ui.ParseMode(*color)
	if err != nil {
		return err
	}
	ui.SetMode(mode)
	p := ui.For(os.Stdout)

	table := ui.NewTable("NAME", "INSTALLED", "INVOCATION", "PROMPT VIA", "DESCRIPTION")
	for _, s := range harness.Describe() {
		installed := p.Red("no")
		if path, err := exec.LookPath(s.Command); err == nil {
			installed = p.Green(path)
		}
		invocation := strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
		table.Row(p.Bold(s.Name), installed, p.Cyan(invocation), string(s.Prompt), p.Dim(s.Docs))
	}
	if err := table.MaxWidth(ui.TerminalWidth(os.Stdout)).Render(os.Stdout, p); err != nil {
		return err
	}

	fmt.Print(`
Defaults are katana's best-known invocation for each CLI, not a contract with
the upstream tool. If a harness changes its flags, override them per project:

  harness:
    name: codex
    command: codex
    args: ["exec", "--sandbox", "workspace-write"]
    prompt: stdin
`)
	return nil
}
