// Command cowpen — throwaway copy-on-write workspaces for agent edits:
// diff, commit, or roll back atomically.
package main

import (
	"os"

	"github.com/JaydenCJ/cowpen/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
