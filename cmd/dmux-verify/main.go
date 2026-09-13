package main

import (
	"os"

	"durablemux-verifier/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
