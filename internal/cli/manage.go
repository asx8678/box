package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/asx8678/box/internal/profile"
	"github.com/asx8678/box/internal/tui"
)

// isTerminal reports whether f is a terminal (a character device).
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// interactive reports whether box can ask the user something.
func interactive() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// ask shows the confirmation screen; only an explicit yes returns true.
func ask(title string, body []string, question string) bool {
	yes, err := tui.Confirm(title, body, question)
	return err == nil && yes
}

// list prints every program with its profiles and a one-line summary.
func list(dirs profile.Dirs, stdout, stderr io.Writer) int {
	entries, err := os.ReadDir(filepath.Join(dirs.Config, "profiles"))
	if err != nil && !os.IsNotExist(err) {
		return fail(stderr, err)
	}
	folders, err := profile.LoadFolders(profile.FoldersPath(dirs))
	if err != nil {
		return fail(stderr, err)
	}
	code, shown := 0, 0
	for _, e := range entries { // ReadDir sorts by name
		prog := e.Name()
		if !e.IsDir() || !profile.ValidName(prog) {
			continue
		}
		names, err := profile.List(dirs, prog)
		if err != nil || len(names) == 0 {
			continue
		}
		shown++
		used := folders.Uses(prog)
		fmt.Fprintln(stdout, prog)
		for _, name := range names {
			p, err := profile.Load(profile.Path(dirs, prog, name), prog)
			if err != nil {
				fmt.Fprintf(stdout, "  %-12s error: %v\n", name, err)
				code = ExitBox
				continue
			}
			fmt.Fprintf(stdout, "  %-12s %s\n", name, summary(p, used[name]))
		}
	}
	if shown == 0 {
		fmt.Fprintln(stdout, "no profiles yet; box <program> creates one")
	}
	return code
}

func summary(p profile.Profile, folders int) string {
	net := "net off"
	if p.Network {
		net = "net on"
	}
	parts := []string{net, "project " + p.Workdir.Mode}
	add := func(label string, paths ...[]string) {
		var all []string
		for _, ps := range paths {
			all = append(all, ps...)
		}
		if len(all) > 0 {
			parts = append(parts, label+" "+strings.Join(all, ", "))
		}
	}
	add("rw:", p.Home.RW, p.Extra.RW)
	add("ro:", p.Home.RO, p.Extra.RO, p.System.ExtraRO)
	if len(p.Tools) > 0 {
		parts = append(parts, "tools: "+strings.Join(p.Tools, ", "))
	}
	if len(p.Env.Pass) > 0 {
		parts = append(parts, "env: "+strings.Join(p.Env.Pass, ", "))
	}
	if p.Sandbox.Strict {
		parts = append(parts, "strict")
	}
	switch folders {
	case 0:
	case 1:
		parts = append(parts, "used in 1 folder")
	default:
		parts = append(parts, fmt.Sprintf("used in %d folders", folders))
	}
	return strings.Join(parts, " · ")
}

// reset deletes program's profiles and folder memory after confirmation.
// The private home (login state, unpacked apps) is only deleted after a
// second, separate yes; -y answers the first question only.
func reset(dirs profile.Dirs, program string, yes bool, stdout, stderr io.Writer) int {
	program = filepath.Base(program)
	if !profile.ValidName(program) {
		return fail(stderr, fmt.Errorf("%q is not a valid program name", program))
	}
	names, err := profile.List(dirs, program)
	if err != nil {
		return fail(stderr, err)
	}
	folders, err := profile.LoadFolders(profile.FoldersPath(dirs))
	if err != nil {
		return fail(stderr, err)
	}
	homes := filepath.Join(dirs.Data, "homes", program)
	_, homeErr := os.Stat(homes)
	remembered := 0
	for _, progs := range folders.Entries {
		if _, ok := progs[program]; ok {
			remembered++
		}
	}
	if len(names) == 0 && remembered == 0 && homeErr != nil {
		fmt.Fprintf(stdout, "%s has nothing to reset\n", program)
		return 0
	}

	title := "box · reset " + program
	if !yes {
		if !interactive() {
			return fail(stderr, fmt.Errorf("reset needs confirmation; run it in a terminal or add -y"))
		}
		var body []string
		if len(names) > 0 {
			body = append(body, fmt.Sprintf("Profiles to delete: %s", strings.Join(names, ", ")))
		}
		if remembered > 0 {
			body = append(body, fmt.Sprintf("Folders that will forget it: %d", remembered))
		}
		if homeErr == nil {
			body = append(body, "Private home folders are asked about separately.")
		}
		if !ask(title, body, fmt.Sprintf("Reset %s?", program)) {
			fmt.Fprintln(stdout, "nothing changed")
			return ExitBox
		}
	}
	if err := os.RemoveAll(filepath.Join(dirs.Config, "profiles", program)); err != nil {
		return fail(stderr, err)
	}
	folders.Forget(program)
	if err := folders.Save(); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "reset %s\n", program)

	if homeErr == nil {
		body := []string{homes, "They hold login state, caches and unpacked apps. This can't be undone."}
		if !yes && ask(title, body, "Also delete the private home folders?") {
			if err := removeAll(homes); err != nil {
				return fail(stderr, err)
			}
			fmt.Fprintln(stdout, "deleted", homes)
		} else {
			fmt.Fprintln(stdout, "kept private home folders in", homes)
		}
	}
	return 0
}

// removeAll deletes path even when it holds read-only folders, such as Go's
// module cache, whose contents os.RemoveAll alone can't delete.
func removeAll(path string) error {
	if os.RemoveAll(path) == nil {
		return nil
	}
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			os.Chmod(p, 0o700) // before WalkDir reads it
		}
		return nil
	})
	return os.RemoveAll(path)
}
