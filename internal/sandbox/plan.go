// Package sandbox turns a profile into a bubblewrap invocation: the mount
// list, the environment and the command.
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
)

// Kind is a bubblewrap mount operation.
type Kind int

const (
	ROBind     Kind = iota // --ro-bind SRC DEST
	Bind                   // --bind SRC DEST
	ROBindTry              // --ro-bind-try SRC DEST
	Symlink                // --symlink TARGET DEST
	Proc                   // --proc DEST
	Dev                    // --dev DEST
	Tmpfs                  // --tmpfs DEST
	ROBindData             // --ro-bind-data FD DEST
)

// Mount is one filesystem operation inside the sandbox.
type Mount struct {
	Kind Kind
	Src  string // host path, or the symlink target
	Dest string
}

// DryRunInfoFD is the file descriptor the dry run uses for /run/box/profile.
const DryRunInfoFD = 3

// Input is everything the plan is built from. Program lookup and machine
// probing happen before this, so Build is pure given the host.
type Input struct {
	Profile     profile.Profile
	ProfileName string
	Dirs        profile.Dirs
	Protected   profile.Protected
	Workdir     string   // the folder box runs in
	Program     string   // the invoked program path
	ProgramDirs []string // host folders the program needs read-only
	Args        []string // passed to the program untouched
	ProgramEnv  []string // KEY=VALUE the program's kind needs, such as AppImage extraction
	Network     *bool    // --net / --no-net override for this run
	// LegacyTIOCSTI is /proc/sys/dev/tty/legacy_tiocsti: "0" means the
	// kernel already blocks terminal injection; "1" or "" (unknown) means
	// the seccomp filter blocks it.
	LegacyTIOCSTI string
	// Arch is the GOARCH the seccomp filter is built for.
	Arch string
	// Self is box's own executable, mounted at /run/box/box when the
	// profile runs the program under box's init.
	Self string
}

// InitPath is where box's own binary appears inside the sandbox.
const InitPath = "/run/box/box"

// DryRunSeccompFD is the fd the dry run shows for the seccomp filter.
const DryRunSeccompFD = 4

// Plan is a finished sandbox: bwrap's arguments plus the environment
// passed to it.
type Plan struct {
	Flags   []string
	Mounts  []Mount
	Chdir   string
	Env     []string // KEY=VALUE, sorted; the program's whole environment
	Command []string
	Info    []byte // contents of /run/box/profile
	InfoFD  int    // the fd that carries Info to bwrap; exec sets the real one
	// Create lists the program's own folders (and the private home) that
	// don't exist yet; they are created with mode 0700 before exec.
	Create []string
	// Filter is the seccomp filter passed to bwrap, if Filter.Any().
	Filter    Filter
	SeccompFD int // the fd carrying the filter; exec sets the real one
	// NewSession is set when box had to fall back to --new-session
	// because it can't build a seccomp filter for this machine.
	NewSession bool
	// Landlock asks exec to block abstract Unix sockets outside the box.
	Landlock bool
	// FlushInput asks exec to discard pending terminal input, such as
	// replies to the TUI's capability queries.
	FlushInput bool
}

// usrMerged are top-level folders that are symlinks into /usr on most
// distributions; they are recreated as symlinks rather than bound twice.
var usrMerged = []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32"}

// systemFiles are the /etc entries programs commonly need. bwrap follows
// their symlinks on the host, so /etc/resolv.conf pointing into /run or
// /mnt/wsl still works.
var systemFiles = []string{
	"/etc/alternatives",
	"/etc/ca-certificates",
	"/etc/crypto-policies",
	"/etc/gai.conf",
	"/etc/group",
	"/etc/host.conf",
	"/etc/hosts",
	"/etc/ld.so.cache",
	"/etc/ld.so.conf",
	"/etc/ld.so.conf.d",
	"/etc/localtime",
	"/etc/nsswitch.conf",
	"/etc/os-release",
	"/etc/passwd",
	"/etc/pki",
	"/etc/resolv.conf",
	"/etc/ssl",
	"/etc/terminfo",
	"/etc/timezone",
}

// Build plans the sandbox. Every host path is canonicalized and checked
// against the safety rules; any failure means nothing may run.
func Build(h host.Host, in Input) (*Plan, error) {
	p := in.Profile
	if err := p.Validate(p.Program); err != nil {
		return nil, err
	}
	if !profile.ValidName(in.ProfileName) {
		return nil, fmt.Errorf("profile name %q is not valid", in.ProfileName)
	}
	if in.Program == "" {
		return nil, errors.New("no program to run")
	}
	home := in.Dirs.Home
	b := &builder{h: h, prot: in.Protected}
	if err := b.system(); err != nil {
		return nil, err
	}

	// Fresh, empty filesystems owned by the sandbox.
	b.add(Proc, "", "/proc")
	b.add(Dev, "", "/dev")
	b.add(Tmpfs, "", "/tmp")
	b.add(Tmpfs, "", "/run")
	b.add(ROBindData, "", "/run/box/profile")

	// The private home, then the folders the profile lists (the program's
	// own folders on top of the private home, created if missing).
	privateHome, err := profile.Canonical(h, profile.HomeDir(in.Dirs, p.Program, in.ProfileName))
	if err != nil {
		return nil, err
	}
	b.add(Bind, privateHome, home)
	if _, err := h.Stat(privateHome); err != nil {
		b.create = append(b.create, privateHome)
	}
	for _, l := range []struct {
		section   string
		paths     []string
		kind      Kind
		mustExist bool
	}{
		{"system.extra_ro", p.System.ExtraRO, ROBind, true},
		{"home.rw", p.Home.RW, Bind, false},
		{"home.ro", p.Home.RO, ROBind, false},
		{"extra.rw", p.Extra.RW, Bind, true},
		{"extra.ro", p.Extra.RO, ROBind, true},
	} {
		for _, raw := range l.paths {
			if err := b.hostPath(l.section, raw, home, l.kind, l.mustExist); err != nil {
				return nil, err
			}
		}
	}

	wd, err := b.workdir(in.Workdir, p.Workdir)
	if err != nil {
		return nil, err
	}
	if err := b.programDirs(in.ProgramDirs, wd, home); err != nil {
		return nil, err
	}
	command := append([]string{in.Program}, in.Args...)
	if p.Sandbox.Init {
		if in.Self == "" {
			return nil, errors.New("sandbox.init needs box's own path")
		}
		self, err := profile.Canonical(h, in.Self)
		if err != nil {
			return nil, err
		}
		b.add(ROBind, self, InitPath)
		command = append([]string{InitPath, "--box-init", "--"}, command...)
	}

	// Parents mount before children, so nothing is hidden by a later,
	// shallower mount. Ties keep the order above.
	sort.SliceStable(b.mounts, func(i, j int) bool {
		return depth(b.mounts[i].Dest) < depth(b.mounts[j].Dest)
	})
	seen := map[string]bool{}
	for _, m := range b.mounts {
		if seen[m.Dest] {
			return nil, fmt.Errorf("%s is mounted twice; list it only once", m.Dest)
		}
		seen[m.Dest] = true
	}

	network := p.Network
	if in.Network != nil {
		network = *in.Network
	}
	plan := &Plan{
		Mounts:   b.mounts,
		Chdir:    wd,
		Command:  command,
		InfoFD:   DryRunInfoFD,
		Create:   b.create,
		Landlock: p.Sandbox.Landlock && network,
	}
	plan.flags(in, network)
	plan.Env = buildEnv(h, p, in.ProfileName, home, wd, visiblePath(h, b.mounts, privateHome), in.ProgramEnv)
	plan.Info = fmt.Appendf(nil, "program=%s\nprofile=%s\nnetwork=%t\nworkdir=%s\n",
		p.Program, in.ProfileName, network, wd)
	return plan, nil
}

// flags sets bwrap's namespace and process options. Terminal injection is
// blocked by the kernel, or else by the seccomp filter, or as a last resort
// by detaching from the terminal; on WSL the filter also closes the VM's
// sockets to the Windows host.
func (plan *Plan) flags(in Input, network bool) {
	plan.Flags = []string{"--unshare-all"}
	if network {
		plan.Flags = append(plan.Flags, "--share-net")
	}
	plan.Flags = append(plan.Flags, "--die-with-parent")
	filter := Filter{TIOCSTI: in.LegacyTIOCSTI != "0", Vsock: in.Protected.WSL}
	switch {
	case filter.Any() && SeccompSupported(in.Arch):
		plan.Filter = filter
		plan.SeccompFD = DryRunSeccompFD
	case filter.TIOCSTI:
		plan.Flags = append(plan.Flags, "--new-session")
		plan.NewSession = true
	}
	if in.Profile.Sandbox.Strict {
		plan.Flags = append(plan.Flags, "--disable-userns")
	}
}

type builder struct {
	h      host.Host
	prot   profile.Protected
	mounts []Mount
	create []string
}

func (b *builder) add(k Kind, src, dest string) {
	b.mounts = append(b.mounts, Mount{Kind: k, Src: src, Dest: dest})
}

// system mounts /usr and the system files read-only, as on the host.
func (b *builder) system() error {
	b.add(ROBind, "/usr", "/usr")
	for _, dir := range usrMerged {
		fi, err := b.h.Lstat(dir)
		switch {
		case err != nil:
			// Not on this machine.
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := b.h.Readlink(dir)
			if err != nil {
				return err
			}
			b.add(Symlink, target, dir)
		case fi.IsDir():
			b.add(ROBind, dir, dir)
		}
	}
	for _, f := range systemFiles {
		b.add(ROBindTry, f, f)
	}
	return nil
}

// workdir mounts the project folder, keeping .git/config and .git/hooks
// read-only in a read-write project when the profile asks.
func (b *builder) workdir(dir string, w profile.Workdir) (string, error) {
	wd, err := profile.Canonical(b.h, dir)
	if err != nil {
		return "", err
	}
	if err := b.prot.CheckWorkdir(wd); err != nil {
		return "", err
	}
	if err := b.prot.CheckMount(b.h, wd); err != nil {
		return "", err
	}
	if w.Mode != "rw" {
		b.add(ROBind, wd, wd)
		return wd, nil
	}
	if err := b.prot.CheckRW(wd); err != nil {
		return "", err
	}
	b.add(Bind, wd, wd)
	if w.ProtectGit {
		return wd, b.protectGit(wd)
	}
	return wd, nil
}

// programDirs mounts the program's own folders, and those of the tools it
// runs, always read-only: a program that can rewrite itself changes what
// runs outside the box next time. Folders inside the project keep the
// project's mode. A read-write folder that is exactly a program folder
// becomes read-only; one that contains it gets a read-only mount on top.
func (b *builder) programDirs(dirs []string, wd, home string) error {
	for _, dir := range dirs {
		c, err := profile.Canonical(b.h, dir)
		if err != nil {
			return err
		}
		if profile.Within(c, wd) || b.readOnlyCovers(c) {
			continue
		}
		if profile.Within(home, c) {
			return fmt.Errorf("the program's folder %s would expose your whole home folder; "+
				"move the program into a folder of its own, such as ~/.local/bin", c)
		}
		if err := b.prot.CheckMount(b.h, c); err != nil {
			return err
		}
		if i := slices.IndexFunc(b.mounts, func(m Mount) bool { return m.Dest == c }); i >= 0 {
			b.mounts[i].Kind = ROBind
			continue
		}
		b.add(ROBind, c, c)
	}
	return nil
}

// hostPath adds a profile-configured host path, mounted at the same place
// inside, after the safety rules. mustExist is false for the program's own
// folders, which are created before the first run.
func (b *builder) hostPath(section, raw, home string, k Kind, mustExist bool) error {
	expanded, err := profile.Expand(raw, home)
	if err != nil {
		return fmt.Errorf("%s: %w", section, err)
	}
	c, err := profile.Canonical(b.h, expanded)
	if err != nil {
		return fmt.Errorf("%s: %w", section, err)
	}
	if _, err := b.h.Stat(c); err != nil {
		if mustExist {
			return fmt.Errorf("%s: %s doesn't exist", section, raw)
		}
		b.create = append(b.create, c)
	}
	if err := b.prot.CheckMount(b.h, c); err != nil {
		return fmt.Errorf("%s: %w", section, err)
	}
	if k == Bind {
		if err := b.prot.CheckRW(c); err != nil {
			return fmt.Errorf("%s: %w", section, err)
		}
	}
	b.add(k, c, c)
	return nil
}

// readOnlyCovers reports whether the deepest same-path mount containing
// dir is read-only, so dir is already read-only inside.
func (b *builder) readOnlyCovers(dir string) bool {
	best := -1
	for i, m := range b.mounts {
		if (m.Kind == ROBind || m.Kind == Bind) && m.Src == m.Dest && profile.Within(dir, m.Dest) &&
			(best < 0 || depth(m.Dest) >= depth(b.mounts[best].Dest)) {
			best = i
		}
	}
	return best >= 0 && b.mounts[best].Kind == ROBind
}

// protectGit keeps .git/config and .git/hooks read-only in a read-write
// project: whoever can write them runs code the next time git runs outside.
func (b *builder) protectGit(wd string) error {
	gitDir := filepath.Join(wd, ".git")
	fi, err := b.h.Lstat(gitDir)
	if err != nil {
		return nil // not a git repository
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to guess what it protects", gitDir)
	}
	if !fi.IsDir() {
		return nil // a worktree's .git file
	}
	for _, name := range []string{"config", "hooks"} {
		p := filepath.Join(gitDir, name)
		fi, err := b.h.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to guess what it protects", p)
		}
		b.add(ROBind, p, p)
	}
	return nil
}

func depth(p string) int {
	if p == "/" {
		return 0
	}
	return strings.Count(filepath.Clean(p), "/")
}

// visiblePath keeps the host PATH entries that exist inside the sandbox:
// those under a same-path mount other than the private home. Windows
// folders under /mnt are never mounted, so they drop out here.
func visiblePath(h host.Host, mounts []Mount, privateHome string) string {
	var keep []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(h.Getenv("PATH")) {
		if !filepath.IsAbs(dir) || seen[dir] {
			continue
		}
		c, err := profile.Canonical(h, dir)
		if err != nil {
			continue
		}
		// Entries are compared by their real path, so /bin (→ /usr/bin)
		// counts as visible through the /usr mount.
		for _, m := range mounts {
			same := m.Src == m.Dest && (m.Kind == ROBind || m.Kind == Bind)
			if same && m.Src != privateHome && profile.Within(c, m.Dest) {
				keep = append(keep, dir)
				seen[dir] = true
				break
			}
		}
	}
	if len(keep) == 0 {
		return "/usr/local/bin:/usr/bin:/bin"
	}
	return strings.Join(keep, ":")
}

// baseEnv are passed whenever they are set: the terminal and the locale.
var baseEnv = []string{"TERM", "COLORTERM", "LANG", "LANGUAGE", "TZ", "NO_COLOR"}

// buildEnv is the program's whole environment. box passes it straight to
// execve, so nothing else from the host leaks in.
func buildEnv(h host.Host, p profile.Profile, name, home, wd, path string, programEnv []string) []string {
	env := map[string]string{
		"HOME":        home,
		"PATH":        path,
		"PWD":         wd,
		"TMPDIR":      "/tmp",
		"BOX_PROFILE": p.Program + "/" + name,
	}
	for _, k := range baseEnv {
		if v, ok := h.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for _, kv := range h.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "LC_") {
			env[k] = v
		}
	}
	for _, k := range p.Env.Pass {
		if v, ok := h.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for _, kv := range programEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range p.Env.Set {
		env[k] = v
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
