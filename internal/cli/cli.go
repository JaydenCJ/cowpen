// Package cli parses arguments and dispatches cowpen's commands. All I/O
// goes through the injected writers, so the whole CLI is exercised
// in-process by the test suite — no subprocess spawning, no PATH games.
package cli

import (
	"fmt"
	"io"

	"github.com/JaydenCJ/cowpen/internal/version"
)

// Exit codes — the contract agents script against.
const (
	ExitOK      = 0 // success; for status/diff: tree is clean
	ExitChanges = 1 // status/diff: the tree differs from the pen
	ExitUsage   = 2 // bad flags or arguments
	ExitRuntime = 3 // I/O failure, missing pen, pending journal, …
)

const usage = `cowpen — throwaway copy-on-write workspaces for agent edits

Usage: cowpen [--root DIR] <command> [args]

Commands:
  new [-m NOTE]            open a pen: snapshot the tree, then edit freely
  status [--verify]        list changes since the top pen (exit 1 if any)
  diff [PATH...]           unified diff of changes (exit 1 if any)
  commit [-m NOTE]         accept changes and close the top pen
  rollback [--to ID]       restore the snapshot atomically, closing pens
  rollback --resume        finish an interrupted rollback
  list                     show open pens, bottom to top
  show ID                  one pen's manifest summary and current changes
  log                      history of opened / committed / rolled_back
  gc                       delete blobs no open pen references
  version                  print the version

Global flags:
  --root DIR               workspace root (default: nearest .cowpen upward)
  --format json            machine output (every command except diff/version)

Exit codes: 0 ok/clean · 1 changes present · 2 usage · 3 runtime error
`

// Run executes one cowpen invocation and returns its exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	g, rest, err := parseGlobal(args)
	if err != nil {
		fmt.Fprintf(stderr, "cowpen: %v\n", err)
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
	if g.version {
		fmt.Fprintf(stdout, "cowpen %s\n", version.Version)
		return ExitOK
	}
	if g.help || len(rest) == 0 {
		fmt.Fprint(stdout, usage)
		if len(rest) == 0 && !g.help {
			return ExitUsage
		}
		return ExitOK
	}
	cmd, cmdArgs := rest[0], rest[1:]
	c := &ctx{globals: g, stdout: stdout, stderr: stderr}
	switch cmd {
	case "new":
		return c.cmdNew(cmdArgs)
	case "status":
		return c.cmdStatus(cmdArgs)
	case "diff":
		return c.cmdDiff(cmdArgs)
	case "commit":
		return c.cmdCommit(cmdArgs)
	case "rollback":
		return c.cmdRollback(cmdArgs)
	case "list":
		return c.cmdList(cmdArgs)
	case "show":
		return c.cmdShow(cmdArgs)
	case "log":
		return c.cmdLog(cmdArgs)
	case "gc":
		return c.cmdGC(cmdArgs)
	case "version":
		fmt.Fprintf(stdout, "cowpen %s\n", version.Version)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "cowpen: unknown command %q\n", cmd)
		fmt.Fprint(stderr, usage)
		return ExitUsage
	}
}

type globals struct {
	root    string
	format  string // "" (human) or "json"
	version bool
	help    bool
}

// parseGlobal peels off global flags, which may appear before the command.
func parseGlobal(args []string) (globals, []string, error) {
	g := globals{}
	var rest []string
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--root":
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("--root needs a directory")
			}
			g.root = args[i+1]
			i += 2
		case a == "--format":
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("--format needs a value")
			}
			g.format = args[i+1]
			if g.format != "json" {
				return g, nil, fmt.Errorf("--format must be json")
			}
			i += 2
		case a == "--version":
			g.version = true
			i++
		case a == "-h" || a == "--help":
			g.help = true
			i++
		default:
			rest = append(rest, a)
			i++
		}
	}
	return g, rest, nil
}

type ctx struct {
	globals globals
	stdout  io.Writer
	stderr  io.Writer
}

func (c *ctx) fail(err error) int {
	fmt.Fprintf(c.stderr, "cowpen: %v\n", err)
	return ExitRuntime
}

func (c *ctx) usageErr(format string, a ...any) int {
	fmt.Fprintf(c.stderr, "cowpen: "+format+"\n", a...)
	return ExitUsage
}

func (c *ctx) json() bool { return c.globals.format == "json" }
