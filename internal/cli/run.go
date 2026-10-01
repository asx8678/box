// Package cli parses box's command line and dispatches it.
package cli

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
	"github.com/asx8678/box/internal/sandbox"
	"github.com/asx8678/box/internal/tui"
)

// ExitBox is box's own failure before anything runs, as with docker run.
const ExitBox = 125

const usage = `usage: box [flags] <program> [program args...]
       box -l | --list
       box -r [-y] <program>
       box --doctor | --net-log | --version | -h

Runs <program> inside a bubblewrap sandbox limited to the current folder.
box's flags go before the program name; everything after it goes to the
program untouched.

  -p NAME      run with profile NAME
  -n NAME      create profile NAME in the editor, then run (--no-tui: save the preset as-is)
  -e           edit the profile that would be used, then run
  --net        network on for this run only
  --no-net     network off for this run only
  --dry-run    print the bwrap command instead of running it
  --no-tui     never open the TUI; fail if a choice is needed
  -l, --list   list programs and their profiles
  -r           reset: delete the program's profiles and folder memory
  -y           with -r: don't ask for confirmation (keeps the private home)
  --doctor     check bubblewrap and this machine, then exit
  --net-log    show what restricted networks allowed and blocked lately
  --version    print box's version

With no profile yet, an editor opens (mouse or keyboard); with several,
a picker. Profiles live in ~/.config/box/profiles/<program>/.

Exit codes: the program's own; 125 box error (nothing ran); 126 can't run
or a Windows program; 127 not found.
`

type options struct {
	profile, newProfile   string
	net, noNet            bool
	dryRun, noTUI, doctor bool
	version, help, netLog bool
	list, reset, yes      bool
	edit                  bool
}

func parse(args []string) (options, []string, error) {
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
	fs.BoolVar(&o.netLog, "net-log", false, "")
	fs.BoolVar(&o.help, "h", false, "")
	fs.BoolVar(&o.help, "help", false, "")
	fs.BoolVar(&o.list, "l", false, "")
	fs.BoolVar(&o.list, "list", false, "")
	fs.BoolVar(&o.reset, "r", false, "")
	fs.BoolVar(&o.yes, "y", false, "")
	fs.BoolVar(&o.edit, "e", false, "")
	if err := fs.Parse(args); err != nil {
		return o, nil, err
	}
	if o.edit && (o.newProfile != "" || o.noTUI) {
		return o, nil, errors.New("-e can't be combined with -n or --no-tui")
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
	o, rest, err := parse(args)
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
	// As root the program would keep every capability inside the box.
	if os.Getuid() == 0 {
		return fail(stderr, errors.New("box doesn't run as root: run it as your own user, without sudo"))
	}
	h := host.OS{}
	dirs, err := profile.DirsFor(h)
	if err != nil {
		return fail(stderr, err)
	}
	if err := profile.CheckConfigDir(dirs); err != nil {
		return fail(stderr, err)
	}
	switch {
	case o.doctor:
		return doctor(dirs, stdout, stderr)
	case o.list:
		return list(dirs, stdout, stderr)
	case o.netLog:
		return showNetLog(dirs, stdout)
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
	code, err := run(h, dirs, o, rest[0], rest[1:], stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "box: %v\n", err)
	}
	return code
}

// runner holds what one run of box knows about the program and machine.
type runner struct {
	h       host.Host
	dirs    profile.Dirs
	prog    sandbox.Program
	args    []string
	wd      string // canonical working folder
	probe   sandbox.Probe
	prot    profile.Protected
	self    string
	bwrap   string
	network *bool
	egress  string // the proxy socket, for a restricted network
}

func run(h host.Host, dirs profile.Dirs, o options, name string, args []string, stdout, stderr io.Writer) (int, error) {
	r := &runner{h: h, dirs: dirs, args: args, egress: egressSocket(h)}
	wd, err := h.Getwd()
	if err != nil {
		return ExitBox, err
	}
	if r.wd, err = profile.Canonical(h, wd); err != nil {
		return ExitBox, err
	}
	if r.prog, err = sandbox.Lookup(h, name, r.wd); err != nil {
		var le *sandbox.LookupError
		if errors.As(err, &le) {
			return le.Code, err
		}
		return ExitBox, err
	}
	// A dry run only prints the command, so a machine that can't run
	// sandboxes (no bwrap, blocked namespaces, not Linux) still gets one.
	var perr error
	if r.probe, perr = sandbox.RunProbe(dirs.State, false); perr != nil {
		if !o.dryRun {
			return ExitBox, perr
		}
		if !errors.Is(perr, sandbox.ErrNeedsLinux) {
			fmt.Fprintf(stderr, "box: warning: %v\n     (printing the command anyway)\n", perr)
		}
	}
	folders, err := loadFolders(dirs, stderr)
	if err != nil {
		return ExitBox, err
	}
	r.self, _ = os.Executable()
	if r.prot, err = profile.NewProtected(h, dirs, r.self, r.probe.WSL); err != nil {
		return ExitBox, err
	}
	// Refuse a folder box can't run in before the editor opens, not after
	// the user has made their choices in it.
	if err := r.prot.CheckWorkdir(r.wd); err != nil {
		return ExitBox, err
	}
	if err := r.prot.CheckMount(h, r.wd); err != nil {
		return ExitBox, err
	}
	if o.net || o.noNet {
		r.network = &o.net
	}
	r.bwrap = cmp.Or(r.probe.Bwrap, "bwrap")

	sel, err := choose(dirs, folders, o, r.prog.Name, r.wd)
	if err != nil {
		return ExitBox, err
	}
	name, p := sel.name, sel.profile
	if sel.edit {
		if name, p, err = r.edit(sel); err != nil {
			return ExitBox, err
		}
	} else if o.newProfile != "" {
		if s := profile.Suggest(h, dirs.Home, p); len(s) > 0 {
			fmt.Fprintf(stderr, "box: folders named after %s that the profile doesn't mount: %s\n"+
				"     add them under [home] in %s if it needs them\n",
				r.prog.Name, strings.Join(s, ", "), profile.Path(dirs, r.prog.Name, name))
		}
	}

	plan, err := r.plan(p, name)
	if err != nil {
		return ExitBox, err
	}
	// A new or edited profile is saved only once its sandbox is known to be
	// valid. One saved in the editor is saved under --dry-run too: the user
	// pressed Save.
	if sel.edit || (sel.isNew && !o.dryRun) {
		if err := profile.Save(profile.Path(dirs, r.prog.Name, name), p); err != nil {
			return ExitBox, err
		}
		if o.dryRun {
			fmt.Fprintf(stderr, "box: saved profile %s; nothing ran (--dry-run)\n", name)
		}
	}
	plan.FlushInput = sel.edit || sel.picked
	if o.dryRun {
		fmt.Fprint(stdout, plan.DryRun(r.bwrap))
		return 0, nil
	}
	for _, n := range plan.Notes {
		fmt.Fprintf(stderr, "box: note: %s\n", n)
	}
	if plan.NewSession {
		fmt.Fprintln(stderr, "box: this kernel allows terminal injection, so the program runs detached "+
			"from the terminal (no resize, no job control)")
	}
	if err := sandbox.Prepare(plan); err != nil {
		return ExitBox, err
	}
	folders.Set(r.wd, r.prog.Name, name)
	if err := folders.Save(); err != nil {
		return ExitBox, err
	}
	// Say what starts and how, when a person is watching: on a terminal,
	// unless --no-tui asked for quiet. After the editor or the picker the
	// line is held for a moment, so the hand-over to the program is seen.
	if f, ok := stderr.(*os.File); ok && isTerminal(f) && !o.noTUI {
		step := time.Duration(0)
		if sel.edit || sel.picked {
			step = tui.LaunchStep
		}
		tui.Launching(stderr, r.prog.Name, launchSummary(r.prog.Name, name, p, plan.Network), step)
	}
	if plan.Network == profile.NetRestricted {
		return r.proxied(plan, name, stderr)
	}
	return ExitBox, sandbox.Exec(r.probe.Bwrap, plan)
}

// launchSummary is the launch line: the program, its profile, and the two
// settings that matter most. net is the network this run really gets,
// after --net or --no-net.
func launchSummary(program, name string, p profile.Profile, net profile.NetMode) string {
	project := "read-write"
	if p.Workdir.Mode != "rw" {
		project = "read-only"
	}
	return fmt.Sprintf("%s · profile %s · network %s · project %s", program, name, net, project)
}

// plan builds the sandbox for profile p. The program's folders are mounted
// with those of the tools it runs; tools that aren't installed are skipped.
func (r *runner) plan(p profile.Profile, name string) (*sandbox.Plan, error) {
	progDirs := slices.Clone(r.prog.Dirs)
	for _, tool := range p.Tools {
		if t, err := sandbox.Lookup(r.h, tool, r.wd); err == nil {
			progDirs = append(progDirs, t.Dirs...)
		}
	}
	return sandbox.Build(r.h, sandbox.Input{
		Profile:       p,
		ProfileName:   name,
		Dirs:          r.dirs,
		Protected:     r.prot,
		Workdir:       r.wd,
		Program:       r.prog.Path,
		ProgramDirs:   progDirs,
		ProgramEnv:    r.prog.Env,
		Args:          r.args,
		Network:       r.network,
		LegacyTIOCSTI: r.probe.LegacyTIOCSTI,
		Arch:          runtime.GOARCH,
		Self:          r.self,
		EgressSocket:  r.egress,
	})
}

// edit opens the profile editor on the selection and returns what was saved.
func (r *runner) edit(sel selection) (string, profile.Profile, error) {
	res, err := tui.Edit(tui.Options{
		Profile:   sel.profile,
		Name:      sel.name,
		New:       sel.isNew,
		Workdir:   tilde(r.wd, r.dirs.Home),
		Home:      r.dirs.Home,
		Suggested: profile.Suggest(r.h, r.dirs.Home, sel.profile),
		EnvHints:  envHints(r.h),
		Blocked:   recentlyBlocked(r.dirs, r.prog.Name, sel.profile),
		Exists: func(n string) bool {
			_, err := os.Lstat(profile.Path(r.dirs, r.prog.Name, n))
			return err == nil
		},
		CheckPath: func(path string, rw, mustExist bool) error {
			return checkPath(r.h, r.dirs.Home, r.prot, path, rw, mustExist)
		},
		Plan: func(p profile.Profile, n string) (string, error) {
			plan, err := r.plan(p, n)
			if err != nil {
				return "", err
			}
			return plan.DryRun(r.bwrap), nil
		},
	})
	if errors.Is(err, tui.ErrCancelled) {
		return "", profile.Profile{}, errors.New("cancelled; nothing was saved or run")
	}
	return res.Name, res.Profile, err
}

// selection is the profile to run and whether the editor opens first.
type selection struct {
	name    string
	profile profile.Profile
	edit    bool // open the editor before running
	isNew   bool // a new profile: saved once its plan is built
	picked  bool // the picker was shown
}

// choose picks the profile: -n creates it, -p names it, then folder
// memory, then the program's only profile. With no profile yet, the editor
// opens on the preset. Choosing among several needs the picker (milestone 5).
func choose(dirs profile.Dirs, folders *profile.Folders, o options, program, wd string) (selection, error) {
	canEdit := !o.noTUI && interactive()
	why := "there's no terminal to show the editor on"
	if o.noTUI {
		why = "--no-tui is set"
	}
	if o.newProfile != "" {
		if !profile.ValidName(o.newProfile) {
			return selection{}, fmt.Errorf("profile name %q is not valid", o.newProfile)
		}
		path := profile.Path(dirs, program, o.newProfile)
		if _, err := os.Lstat(path); err == nil {
			return selection{}, fmt.Errorf("profile %q already exists for %s", o.newProfile, program)
		}
		p, err := profile.Preset(program)
		if err != nil {
			return selection{}, err
		}
		if o.noTUI {
			return selection{name: o.newProfile, profile: p, isNew: true}, nil
		}
		if !canEdit {
			return selection{}, fmt.Errorf("-n needs the profile editor, but %s", why)
		}
		// Pre-fill from the program's default profile when there is one.
		if d, err := profile.Load(profile.Path(dirs, program, "default"), program); err == nil {
			p = d
		}
		return selection{name: o.newProfile, profile: p, edit: true, isNew: true}, nil
	}
	names, err := profile.List(dirs, program)
	if err != nil {
		return selection{}, err
	}
	name := o.profile
	pickedShown := false
	if name == "" {
		if remembered, ok := folders.Get(wd, program); ok && slices.Contains(names, remembered) {
			name = remembered
		}
	}
	if name == "" {
		switch len(names) {
		case 0:
			if !canEdit {
				return selection{}, fmt.Errorf("%s has no profile yet, and %s; create one from its preset with:\n  box -n default --no-tui %s",
					program, why, program)
			}
			p, err := profile.Preset(program)
			if err != nil {
				return selection{}, err
			}
			return selection{name: "default", profile: p, edit: true, isNew: true}, nil
		case 1:
			name = names[0]
		default:
			if !canEdit {
				return selection{}, fmt.Errorf("%s has several profiles (%s) and none is remembered for this folder; pick one with -p NAME",
					program, strings.Join(names, ", "))
			}
			picked, err := pick(dirs, folders, program, names)
			if err != nil {
				return selection{}, err
			}
			name = picked
			pickedShown = true
		}
	}
	if !slices.Contains(names, name) {
		return selection{}, fmt.Errorf("%s has no profile %q", program, name)
	}
	p, err := profile.Load(profile.Path(dirs, program, name), program)
	if err != nil {
		return selection{}, err
	}
	if o.edit && !canEdit {
		return selection{}, fmt.Errorf("-e needs the profile editor, but %s", why)
	}
	return selection{name: name, profile: p, edit: o.edit, picked: pickedShown}, nil
}

// pick shows the profile picker with a one-line summary of each profile.
func pick(dirs profile.Dirs, folders *profile.Folders, program string, names []string) (string, error) {
	used := folders.Uses(program)
	var choices []tui.Choice
	for _, n := range names {
		detail := "error: can't read it"
		if p, err := profile.Load(profile.Path(dirs, program, n), program); err == nil {
			detail = summary(p, used[n])
		}
		choices = append(choices, tui.Choice{Name: n, Detail: detail})
	}
	name, err := tui.Pick(fmt.Sprintf("box · %s · which profile?", program), choices)
	if errors.Is(err, tui.ErrCancelled) {
		return "", errors.New("cancelled; nothing was run")
	}
	return name, err
}

// checkPath is the editor's live check for a folder being ticked, added or
// switched to read-write: it must pass the safety rules, and exist when
// mustExist is set.
func checkPath(h host.Host, home string, prot profile.Protected, raw string, rw, mustExist bool) error {
	expanded, err := profile.Expand(raw, home)
	if err != nil {
		return err
	}
	c, err := profile.Canonical(h, expanded)
	if err != nil {
		return err
	}
	if _, err := h.Stat(c); err != nil && mustExist {
		return fmt.Errorf("%s doesn't exist", raw)
	}
	if err := prot.CheckMount(h, c); err != nil {
		return err
	}
	if rw {
		return prot.CheckRW(c)
	}
	return nil
}

// envHints offers variables set on the host that programs commonly need.
// They start unticked; their values are read at run time, never stored.
func envHints(h host.Host) []string {
	var out []string
	for _, kv := range h.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasSuffix(k, "_API_KEY") || strings.HasSuffix(k, "_TOKEN") ||
			k == "AWS_PROFILE" || k == "AWS_REGION" || k == "EDITOR" {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// tilde shortens paths under home to "~/…" for display.
func tilde(path, home string) string {
	if profile.Within(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
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
		tiocsti = "allowed by the kernel: box's seccomp filter blocks it"
		if !sandbox.SeccompSupported(runtime.GOARCH) {
			tiocsti = "allowed: box will detach programs from the terminal"
		}
	}
	show("terminal injection", tiocsti)
	show("apparmor userns limit", p.AppArmorUserns)
	landlock := "not available"
	if abi := sandbox.LandlockABI(); abi >= 6 {
		landlock = fmt.Sprintf("ABI %d: sandbox.landlock can block abstract sockets", abi)
	} else if abi > 0 {
		landlock = fmt.Sprintf("ABI %d: too old for sandbox.landlock (needs 6, Linux 6.12+)", abi)
	}
	show("landlock", landlock)
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

// loadFolders reads folder memory, warning when a damaged file was set aside.
func loadFolders(dirs profile.Dirs, stderr io.Writer) (*profile.Folders, error) {
	f, err := profile.LoadFolders(profile.FoldersPath(dirs))
	if err == nil && f.Damaged != "" {
		fmt.Fprintf(stderr, "box: warning: %s\n", f.Damaged)
	}
	return f, err
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
