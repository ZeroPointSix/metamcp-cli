// Command mcli is a command-line client for MetaMCP endpoints.
package main

import (
	"os"

	"github.com/ZeroPointSix/metamcp-cli/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
