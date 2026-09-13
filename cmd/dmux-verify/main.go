package main

import (
	"os"

	"github.com/Alisjj/durablemux-verifier/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
