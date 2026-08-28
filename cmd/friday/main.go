// Command friday is the local-first agent harness CLI.
package main

import (
	"os"

	"github.com/ataidesorg/friday/internal/cli"
)

func main() { os.Exit(cli.Main()) }
