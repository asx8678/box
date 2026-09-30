// Package cli parses box's command line and dispatches it.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
	"github.com/asx8678/box/internal/sandbox"
)

// ExitBox is box's own failure before anything runs, as with docker run.
const ExitBox = 125

const usage = `usage: box [flags] <program> [program args...]
       box -l | --list
       box -r [-y] <program>
       box --doctor | --version | -h

Runs <program> inside a bubblewrap sandbox limited to the current folder.
box's flags go before the program name; everything after it goes to the
program untouched.

  -p NAME      run with profile NAME
  -n NAME      create profile NAME from the preset, then run (needs --no-tui for now)
  --net        network on for this run only
  --no-net     network off for this run only
  --dry-run    print the bwrap command instead of running it
  --no-tui     never open the TUI; fail if a choice is needed
  -l, --list   list programs and their profiles
  -r           reset: delete the program's profiles and folder memory
  -y           with -r: don't ask for confirmation (keeps the private home)
  --doctor     check bubblewrap and this machine, then exit
  --version    print box's version

Exit codes: the program's own; 125 box error; 126 can't run; 127 not found.
`

type options struct {
	profile, newProfile   string
	net, noNet            bool
	dryRun, noTUI, doctor bool
	version, help         bool
	list, reset, yes      bool
}

func parse(args []string, stderr io.Writer) (options, []string, error) {
	var o options
	fs := flag.NewFlagSet("box", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.profile, "p", "", "")
	fs.StringVar(&o.newProfile, "n", "", "")
	fs.BoolVar(&o.net, "net", false, "")
	fs.BoolVar(&o.noNet, "no-net", false, "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.noTUI, "no-tui", false, "")
	fs.BoolVar(&o.doctor, "doctor", false, "")
	fs.BoolVar(&o.version, "version", false, "")
	fs.BoolVar(&o.help, "h", false, "")
	fs.BoolVar(&o.help, "help", false, "")
	fs.BoolVar(&o.list, "l", false, "")
	fs.BoolVar(&o.list, "list", false, "")
	fs.BoolVar(&o.reset, "r", false, "")
	fs.BoolVar(&o.yes, "y", false, "")
	for _, later := range []string{"e"} {
		fs.Bool(later, false, "")
	}
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	if fs.Lookup("e").Value.String() == "true" {
		return o, nil, errors.New("-e arrives with the TUI (milestone 4)")
	}
	if o.yes && !o.reset {
		return o, nil, errors.New("-y only goes with -r")
	}
	if o.reset && (o.profile != "" || o.newProfile != "" || o.dryRun) {
		return o, nil, errors.New("-r can't be combined with -p, -n or --dry-run")
	}
	if o.net && o.noNet {
		return o, nil, errors.New("--net and --no-net can't be used together")
	}
	if o.profile != "" && o.newProfile != "" {
		return o, nil, errors.New("-p and -n can't be used together")
	}
	return o, fs.Args(), nil
}

// Run executes box with args (without argv[0]) and returns the exit code.
// On success it doesn't return: box is replaced by bwrap.
func Run(args []string, stdout, stderr io.Writer) int {
	o, rest, err := parse(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "box: %v\n\n%s", err, usage)
		return ExitBox
	}
	switch {
	case o.help:
		fmt.Fprint(stdout, usage)
		return 0
	case o.version:
		fmt.Fprintln(stdout, "box", version())
		return 0
	}
	h := host.OS{}
	dirs, err := profile.DirsFor(h)
	if err != nil {
		return fail(stderr, err)
	}
	switch {
	case o.doctor:
		return doctor(dirs, stdout, stderr)
	case o.list:
		return list(dirs, stdout, stderr)
	case o.reset:
		if len(rest) != 1 {
			return fail(stderr, errors.New("usage: box -r [-y] <program>"))
		}
		return reset(dirs, rest[0], o.yes, stdout, stderr)
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitBox
	}
	code, err := run(h, dirs, o, rest[0], rest[1:], stdout)
	if err != nil {
		fmt.Fprintf(stderr, "box: %v\n", err)
	}
	return code
}

func run(h host.Host, dirs profile.Dirs, o options, name string, args []string, stdout io.Writer) (int, error) {
	wd, err := h.Getwd()
	if err != nil {
		return ExitBox, err
	}
	prog, err := sandbox.Lookup(h, name, wd)
	if err != nil {
		var le *sandbox.LookupError
		if errors.As(err, &le) {
			return le.Code, err
		}
		return ExitBox, err
	}

	probe, perr := sandbox.RunProbe(dirs.State, false)
	if perr != nil && !(o.dryRun && errors.Is(perr, sandbox.ErrNeedsLinux)) {
		return ExitBox, perr
	}

	realWd, err := profile.Canonical(h, wd)
	if err != nil {
		return ExitBox, err
	}
	folders, err := profile.LoadFolders(profile.FoldersPath(dirs))
	if err != nil {
		return ExitBox, err
	}
	name, p, err := choose(dirs, folders, o, prog.Name, realWd)
	if err != nil {
		return ExitBox, err
	}
	if o.newProfile != "" {
		if s := profile.Suggest(h, dirs.Home, p); len(s) > 0 {
			fmt.Fprintf(os.Stderr, "box: folders named after %s that the profile doesn't mount: %s\n"+
				"     add them under [home] in %s if it needs them\n",
				prog.Name, strings.Join(s, ", "), profile.Path(dirs, prog.Name, name))
		}
	}

	self, _ := os.Executable()
	prot, err := profile.NewProtected(h, dirs, self, probe.WSL)
	if err != nil {
		return ExitBox, err
	}
	in := sandbox.Input{
		Profile:       p,
		ProfileName:   name,
		Dirs:          dirs,
		Protected:     prot,
		Workdir:       realWd,
		Program:       prog.Path,
		ProgramDirs:   prog.Dirs,
		Args:          args,
		LegacyTIOCSTI: probe.LegacyTIOCSTI,
	}
	if o.net || o.noNet {
		in.Network = &o.net
	}
	plan, err := sandbox.Build(h, in)
	if err != nil {
		return ExitBox, err
	}
	if o.dryRun {
		bwrap := probe.Bwrap
		if bwrap == "" {
			bwrap = "bwrap"
		}
		fmt.Fprint(stdout, plan.DryRun(bwrap))
		return 0, nil
	}
	if plan.NewSession {
		fmt.Fprintln(os.Stderr, "box: this kernel allows terminal injection, so the program runs detached "+
			"from the terminal (no resize, no job control)")
	}
	if err := sandbox.Prepare(plan); err != nil {
		return ExitBox, err
	}
	folders.Set(realWd, prog.Name, name)
	if err := folders.Save(); err != nil {
		return ExitBox, err
	}
	return ExitBox, sandbox.Exec(probe.Bwrap, plan)
}

// choose picks the profile: -n creates it, -p names it, then folder
// memory, then the program's only profile. Choosing among several, or
// creating the first one interactively, needs the TUI (milestone 4).
func choose(dirs profile.Dirs, folders *profile.Folders, o options, program, wd string) (string, profile.Profile, error) {
	if o.newProfile != "" {
		if !profile.ValidName(o.newProfile) {
			return "", profile.Profile{}, fmt.Errorf("profile name %q is not valid", o.newProfile)
		}
		if !o.noTUI {
			return "", profile.Profile{}, errors.New("the profile editor arrives with the TUI; use -n NAME --no-tui to save the preset as-is")
		}
		path := profile.Path(dirs, program, o.newProfile)
		if _, err := os.Lstat(path); err == nil {
			return "", profile.Profile{}, fmt.Errorf("profile %q already exists for %s", o.newProfile, program)
		}
		p, err := profile.Preset(program)
		if err != nil {
			return "", profile.Profile{}, err
		}
		if !o.dryRun {
			if err := profile.Save(path, p); err != nil {
				return "", profile.Profile{}, err
			}
		}
		return o.newProfile, p, nil
	}
	names, err := profile.List(dirs, program)
	if err != nil {
		return "", profile.Profile{}, err
	}
	name := o.profile
	if name == "" {
		if remembered, ok := folders.Get(wd, program); ok && slices.Contains(names, remembered) {
			name = remembered
		}
	}
	if name == "" {
		switch len(names) {
		case 0:
			why := "the profile editor arrives with the TUI (milestone 4)"
			if o.noTUI || !interactive() {
				why = "there's no terminal to ask on"
			}
			return "", profile.Profile{}, fmt.Errorf("%s has no profile yet, and %s; create one from its preset with:\n  box -n default --no-tui %s", program, why, program)
		case 1:
			name = names[0]
		default:
			return "", profile.Profile{}, fmt.Errorf("%s has several profiles (%s); pick one with -p NAME", program, strings.Join(names, ", "))
		}
	}
	if !slices.Contains(names, name) {
		return "", profile.Profile{}, fmt.Errorf("%s has no profile %q", program, name)
	}
	p, err := profile.Load(profile.Path(dirs, program, name), program)
	return name, p, err
}

func doctor(dirs profile.Dirs, stdout, stderr io.Writer) int {
	p, err := sandbox.RunProbe(dirs.State, true)
	show := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Fprintf(stdout, "%-24s %s\n", k, v)
	}
	show("box", version())
	show("bwrap", strings.TrimSpace(p.Bwrap+" "+p.Version))
	show("kernel", p.Kernel)
	show("wsl", fmt.Sprint(p.WSL))
	tiocsti := "blocked by the kernel"
	if p.LegacyTIOCSTI != "0" {
		tiocsti = "allowed: box will detach programs from the terminal"
	}
	show("terminal injection", tiocsti)
	show("apparmor userns limit", p.AppArmorUserns)
	if p.Version != "" && older(p.Version, "0.12.0") {
		fmt.Fprintln(stdout, "note: bwrap before 0.12.0 has CVE-2026-87766 unless your distro patched it;"+
			" box guards against it, but a newer bwrap in /usr/local/bin is better")
	}
	if err != nil {
		fmt.Fprintf(stderr, "box: %v\n", err)
		return ExitBox
	}
	fmt.Fprintln(stdout, "ok: box can run sandboxes here")
	return 0
}

// older compares dotted version numbers.
func older(v, than string) bool {
	a, b := strings.Split(v, "."), strings.Split(than, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			fmt.Sscan(a[i], &x)
		}
		if i < len(b) {
			fmt.Sscan(b[i], &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "box: %v\n", err)
	return ExitBox
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
