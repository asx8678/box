// Command box runs an installed program inside a bubblewrap sandbox,
// limited to the folder it is started in.
package main

import (
	"os"

	"github.com/asx8678/box/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
