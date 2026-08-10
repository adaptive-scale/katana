package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"github.com/adaptive-scale/katana/internal/harness"
)

func runHarnesses(args []string) error {
	fs := flag.NewFlagSet("katana harnesses", flag.ContinueOnError)
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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tINSTALLED\tINVOCATION\tPROMPT VIA\tDESCRIPTION")
	for _, s := range harness.Describe() {
		installed := "no"
		if path, err := exec.LookPath(s.Command); err == nil {
			installed = path
		}
		invocation := strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, installed, invocation, s.Prompt, s.Docs)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Print(`
Defaults are katana's best-known invocation for each CLI, not a contract with
the upstream tool. If a harness changes its flags, override them per project:

  harness:
    name: codex
    command: codex
    args: ["exec", "--full-auto"]
    prompt: arg
`)
	return nil
}
