// Command katana generates test code from written product behavior and keeps it
// in sync as those behaviors change.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/adaptive-scale/katana/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		// flag.ErrHelp means the user asked for usage; the flag package has
		// already printed it.
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "katana: %v\n", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
