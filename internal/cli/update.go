package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/adaptive-scale/katana/internal/update"
)

// updateTimeout bounds a whole `katana update`, download included.
const updateTimeout = 5 * time.Minute

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("katana update", flag.ContinueOnError)
	var (
		check = fs.Bool("check", false, "report whether a newer release exists, without installing it")
		tag   = fs.String("version", "", "install this release tag instead of the newest one")
		force = fs.Bool("force", false, "reinstall even when the running version is already current")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: katana update [flags]

Replaces the running katana binary with the newest published release, verifying
the release checksum when one is available.

katana also checks for a new release once a day in the background and mentions
it after a command finishes. Set KATANA_NO_UPDATE_CHECK=1 to turn that off; it
is already off in CI and for locally built binaries.

Releases of a private repository need a token in GITHUB_TOKEN (or
KATANA_GITHUB_TOKEN).

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	c := update.New(Version)
	rel, err := c.Latest(ctx)
	if *tag != "" {
		rel, err = c.ByTag(ctx, *tag)
	}
	if err != nil {
		return err
	}

	// An explicit --version is a request to move to that release, up or down,
	// so only the unpinned path treats "already current" as nothing to do.
	current := *tag == "" && !c.Outdated(rel)
	if *check {
		if current {
			fmt.Printf("katana %s is up to date\n", Version)
			return nil
		}
		fmt.Printf("katana %s is available (you have %s)\n", rel.Tag, Version)
		if rel.URL != "" {
			fmt.Println(rel.URL)
		}
		fmt.Println("run `katana update` to install it")
		return nil
	}
	if current && !*force {
		fmt.Printf("katana %s is already up to date\n", Version)
		return nil
	}

	path, err := c.Apply(ctx, rel, os.Stderr)
	if err != nil {
		return err
	}
	fmt.Printf("updated katana to %s (%s)\n", rel.Tag, path)
	return nil
}
