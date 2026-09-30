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
	if err := b.writableRoots(in.Workdir, p, home); err != nil {
		return nil, err
	}
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
	b.pinParents()

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
	h       host.Host
	prot    profile.Protected
	mounts  []Mount
	create  []string
	rwRoots []string // host folders the program can write, same path inside
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
		i := slices.IndexFunc(b.mounts, func(m Mount) bool { return m.Dest == c })
		if profile.Within(c, wd) || b.readOnlyCovers(c, i) {
			continue
		}
		if profile.Within(home, c) {
			return fmt.Errorf("the program's folder %s would expose your whole home folder; "+
				"move the program into a folder of its own, such as ~/.local/bin", c)
		}
		if err := b.prot.CheckMount(b.h, c); err != nil {
			return err
		}
		if i >= 0 {
			if b.mounts[i].Kind != Bind || b.mounts[i].Src != c {
				return fmt.Errorf("the program's folder %s is a fresh, private folder inside the box; "+
					"move the program somewhere else", c)
			}
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
	if err := b.noPlantedSymlink(expanded); err != nil {
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

// writableRoots records the host folders the program will be able to
// write at their own path: the project (if read-write) and the profile's
// read-write folders. Symlinks inside them may have been planted by the
// program on an earlier run.
func (b *builder) writableRoots(workdir string, p profile.Profile, home string) error {
	if p.Workdir.Mode == "rw" {
		b.rwRoots = append(b.rwRoots, workdir)
	}
	for _, raw := range slices.Concat(p.Home.RW, p.Extra.RW) {
		if expanded, err := profile.Expand(raw, home); err == nil {
			b.rwRoots = append(b.rwRoots, expanded)
		}
	}
	for i, r := range b.rwRoots {
		c, err := profile.Canonical(b.h, r)
		if err != nil {
			return err
		}
		b.rwRoots[i] = c
	}
	return nil
}

// noPlantedSymlink refuses a configured path that runs through a symlink
// inside a folder the program can write: the program may have planted it
// to redirect the mount (plan B4/B6). Symlinks elsewhere are the user's own.
func (b *builder) noPlantedSymlink(path string) error {
	cur := "/"
	for _, name := range strings.Split(strings.Trim(path, "/"), "/") {
		dir, err := profile.Canonical(b.h, cur)
		if err != nil {
			return err
		}
		at := filepath.Join(dir, name)
		cur = filepath.Join(cur, name)
		fi, err := b.h.Lstat(at)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		for _, r := range b.rwRoots {
			if profile.Within(at, r) && at != r {
				return fmt.Errorf("%s is a symlink inside %s, which the program can write; refusing to follow it", at, r)
			}
		}
	}
	return nil
}

// pinParents turns every folder between a read-write mount and a read-only
// mount of the same host path inside it into a mount point of its own (a
// read-write bind onto itself). Linux refuses to rename or remove a mount
// point, but not a folder that merely contains one, so without this the
// program could move .git (or a tool's folder) aside and put a writable copy
// in its place on the host.
func (b *builder) pinParents() {
	for _, m := range slices.Clone(b.mounts) {
		if m.Kind != ROBind || m.Src != m.Dest {
			continue
		}
		root, ok := b.rwParent(m.Dest)
		if !ok {
			continue
		}
		for dir := filepath.Dir(m.Dest); dir != root && profile.Within(dir, root); dir = filepath.Dir(dir) {
			if !slices.ContainsFunc(b.mounts, func(x Mount) bool { return x.Dest == dir }) {
				b.add(Bind, dir, dir)
			}
		}
	}
}

// rwParent returns the mount containing dest if it is a read-write bind
// of the same host path, so a symlink or rename inside it reaches the host.
func (b *builder) rwParent(dest string) (string, bool) {
	m, ok := covering(b.mounts, dest)
	if !ok || m.Kind != Bind || m.Src != m.Dest {
		return "", false
	}
	return m.Dest, true
}

// readOnlyCovers reports whether dir is already read-only inside: the
// mount at dir (index at, or -1), else the mount containing it, is a
// read-only bind of the same host path.
func (b *builder) readOnlyCovers(dir string, at int) bool {
	m, ok := covering(b.mounts, dir)
	if at >= 0 {
		m, ok = b.mounts[at], true
	}
	return ok && m.Kind == ROBind && m.Src == m.Dest
}

// covering returns the deepest mount whose destination strictly contains
// dest: the one dest lives in inside the sandbox. The fixed mounts (/proc,
// /dev, /tmp, /run and the private home) are never contained by a bind of
// a host path, so the answer is the deepest same-path bind when there is one.
func covering(mounts []Mount, dest string) (Mount, bool) {
	best := -1
	for i, m := range mounts {
		if m.Dest != dest && profile.Within(dest, m.Dest) && (best < 0 || depth(m.Dest) >= depth(mounts[best].Dest)) {
			best = i
		}
	}
	if best < 0 {
		return Mount{}, false
	}
	return mounts[best], true
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
			if name == "hooks" {
				// Otherwise the program could create it and add a hook.
				b.create = append(b.create, p)
				b.add(ROBind, p, p)
			}
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
