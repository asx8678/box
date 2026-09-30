// Command box runs an installed program inside a bubblewrap sandbox,
// limited to the folder it is started in.
package main

import (
	"os"

	"github.com/asx8678/box/internal/cli"
	"github.com/asx8678/box/internal/sandbox"
)

func main() {
	// Inside a sandbox, box's own binary is the init (profile sandbox.init).
	if len(os.Args) > 2 && os.Args[1] == "--box-init" && os.Args[2] == "--" {
		os.Exit(sandbox.Init(os.Args[3:]))
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
