// Package cli parses box's command line and dispatches it.
package cli

import (
	"fmt"
	"io"
	"runtime/debug"
)

// ExitBox is box's own failure before anything runs, as with docker run.
const ExitBox = 125

const usage = `usage: box [flags] <program> [program args...]
       box -l | --list
       box -h | --help | --version

Runs <program> inside a bubblewrap sandbox limited to the current folder.
See docs/implementation-plan.md for the full design.
`

// Run executes box with args (without argv[0]) and returns the exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitBox
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "--version":
		fmt.Fprintln(stdout, "box", version())
		return 0
	}
	fmt.Fprintln(stderr, "box: running programs is not implemented yet (milestone 2)")
	return ExitBox
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
