// Command ink is the local-first agent CLI.
package main

import (
	"os"

	"github.com/ataidesorg/ink/internal/cli"
)

func main() { os.Exit(cli.Main()) }
