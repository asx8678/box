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

	"github.com/asx8678/box/internal/egress"
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
	// EgressSocket is the proxy's Unix socket on the host, for a restricted
	// network; it need not exist yet.
	EgressSocket string
}

// InitPath is where box's own binary appears inside the sandbox, and
// EgressPath where the proxy socket does.
const (
	InitPath   = "/run/box/box"
	EgressPath = "/run/box/egress.sock"
)

// DryRunSeccompFD is the fd the dry run shows for the seccomp filter.
const DryRunSeccompFD = 4

// Plan is a finished sandbox: bwrap's arguments plus the environment
// passed to it.
type Plan struct {
	Flags  []string
	Mounts []Mount
	Chdir  string
	Env    []string // KEY=VALUE, sorted; the program's whole environment
	// Passed names the variables whose values were copied from the host
	// because the profile passes them (API keys and the like). The dry run
	// shows them as "$NAME", never their values.
	Passed  []string
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
	// Network is what the sandbox gets, after --net or --no-net. When it
	// is restricted the sandbox has no network of its own, and Allowed
	// lists what box's proxy lets through.
	Network profile.NetMode
	Allowed []profile.AllowSet
	// FlushInput asks exec to discard pending terminal input, such as
	// replies to the TUI's capability queries.
	FlushInput bool
	// Notes are things the user should know about this sandbox.
	Notes []string
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
			if err := b.hostPath(raw, home, l.kind, l.mustExist); err != nil {
				return nil, fmt.Errorf("%s: %w", l.section, err)
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
	network := p.Network
	if in.Network != nil { // --net or --no-net, for this run
		network = profile.NetOff
		if *in.Network {
			network = profile.NetOn
		}
	}
	// A restricted box reaches the network only through box's proxy socket;
	// box's init runs the bridge to it, so it always runs under the init.
	proxied := network == profile.NetRestricted
	if proxied {
		if in.EgressSocket == "" {
			return nil, errors.New("a restricted network needs box's proxy socket")
		}
		b.add(ROBind, in.EgressSocket, EgressPath)
	}
	command := append([]string{in.Program}, in.Args...)
	if p.Sandbox.Init || proxied {
		if in.Self == "" {
			return nil, errors.New("box's init needs box's own path")
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

	plan := &Plan{
		Mounts:   b.mounts,
		Chdir:    wd,
		Command:  command,
		InfoFD:   DryRunInfoFD,
		Create:   b.create,
		Landlock: p.Sandbox.Landlock && network == profile.NetOn,
		Network:  network,
		Notes:    b.notes,
	}
	if network == profile.NetRestricted {
		plan.Allowed = p.Allowed()
	}
	plan.flags(in, network == profile.NetOn)
	programEnv := in.ProgramEnv
	if proxied {
		programEnv = append(slices.Clone(programEnv), proxyEnv...)
	}
	plan.Env, plan.Passed = buildEnv(h, p, in.ProfileName, home, wd, visiblePath(h, b.mounts, privateHome), programEnv)
	plan.Info = fmt.Appendf(nil, "program=%s\nprofile=%s\nnetwork=%s\nworkdir=%s\n",
		p.Program, in.ProfileName, network, wd)
	return plan, nil
}

// proxyEnv points programs at the bridge to box's proxy. socks5h sends host
// names, so the box needs no DNS of its own. Node.js reads the variables
// only with NODE_USE_ENV_PROXY. A profile can change any of them; that only
// breaks its own network.
var proxyEnv = []string{
	"ALL_PROXY=socks5h://" + egress.SocksAddr, "all_proxy=socks5h://" + egress.SocksAddr,
	"HTTP_PROXY=http://" + egress.HTTPAddr, "http_proxy=http://" + egress.HTTPAddr,
	"HTTPS_PROXY=http://" + egress.HTTPAddr, "https_proxy=http://" + egress.HTTPAddr,
	"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1",
	"NODE_USE_ENV_PROXY=1",
}

// flags sets bwrap's namespace and process options; shareNet gives the
// sandbox the host's network. Terminal injection is blocked by the kernel,
// or else by the seccomp filter, or as a last resort by detaching from the
// terminal; on WSL the filter also closes the VM's sockets to the Windows
// host.
func (plan *Plan) flags(in Input, shareNet bool) {
	plan.Flags = []string{"--unshare-all"}
	if shareNet {
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
		// bwrap only accepts --disable-userns with a user namespace it must
		// create, and --unshare-all only tries to.
		plan.Flags = append(plan.Flags, "--unshare-user", "--disable-userns")
	}
}

type builder struct {
	h       host.Host
	prot    profile.Protected
	mounts  []Mount
	create  []string
	rwRoots []string // host folders the program can write, same path inside
	notes   []string // things the user should know, printed before the run
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

// workdir mounts the project folder, keeping git's files read-only in a
// read-write project as far as the profile asks.
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
	if w.ProtectGit == profile.GitOff {
		return wd, nil
	}
	return wd, b.protectGit(wd, w.ProtectGit)
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
		if err := b.prot.CheckSecret(c); err != nil {
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
func (b *builder) hostPath(raw, home string, k Kind, mustExist bool) error {
	expanded, err := profile.Expand(raw, home)
	if err != nil {
		return err
	}
	if err := b.noPlantedSymlink(expanded); err != nil {
		return err
	}
	c, err := profile.Canonical(b.h, expanded)
	if err != nil {
		return err
	}
	if _, err := b.h.Stat(c); err != nil {
		if mustExist {
			return fmt.Errorf("%s doesn't exist", raw)
		}
		b.create = append(b.create, c)
	}
	if err := b.prot.CheckMount(b.h, c); err != nil {
		return err
	}
	if k == Bind {
		if err := b.prot.CheckRW(c); err != nil {
			return err
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

// Nested repositories are looked for this deep below the project, in at
// most this many folders, so a huge project doesn't slow every start.
const (
	repoDepth   = 4
	repoFolders = 500
)

// protectGit keeps git's files read-only in a read-write project: whoever
// can write them runs code the next time git runs outside. GitFull covers all
// of .git, and gives a project without one an empty read-only .git, so the
// program can't git init one with hooks; GitHooks covers config and hooks.
// Repositories nested in the project are protected the same way. One the
// program creates in a subfolder can't be: see the README.
func (b *builder) protectGit(wd string, mode profile.GitMode) error {
	top := filepath.Join(wd, ".git")
	if _, err := b.h.Lstat(top); err != nil && mode == profile.GitFull {
		b.create = append(b.create, top)
		b.add(ROBind, top, top)
	}
	for _, dot := range b.findRepos(wd) {
		if err := b.protectRepo(wd, dot, mode); err != nil {
			return err
		}
	}
	return nil
}

// findRepos returns every .git (folder or file) in the project, the
// project's own first, without following symlinks or entering .git folders.
func (b *builder) findRepos(wd string) []string {
	var found []string
	type dir struct {
		path  string
		depth int
	}
	queue, seen := []dir{{wd, 0}}, 0
	for len(queue) > 0 && seen < repoFolders {
		d := queue[0]
		queue = queue[1:]
		seen++
		entries, err := b.h.ReadDir(d.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(d.path, e.Name())
			switch {
			case e.Name() == ".git":
				found = append(found, p)
			case e.IsDir() && d.depth < repoDepth && e.Name() != "node_modules":
				queue = append(queue, dir{p, d.depth + 1})
			}
		}
	}
	if seen == repoFolders && len(queue) > 0 {
		b.notes = append(b.notes, fmt.Sprintf("the project is large: only %d of its folders were searched for nested "+
			"git repositories to protect", repoFolders))
	}
	return found
}

// protectRepo protects one .git: a folder, or a file naming the real one
// (a worktree or submodule), whose pointer is then kept read-only too.
func (b *builder) protectRepo(wd, dot string, mode profile.GitMode) error {
	fi, err := b.h.Lstat(dot)
	if err != nil {
		return nil
	}
	gitDir := dot
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink; refusing to guess what it protects", dot)
	case !fi.IsDir():
		data, err := b.h.ReadFile(dot)
		if err != nil {
			return err
		}
		target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return fmt.Errorf("%s is a file but doesn't name a git folder (gitdir: …)", dot)
		}
		if target = strings.TrimSpace(target); !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dot), target)
		}
		if gitDir, err = profile.Canonical(b.h, target); err != nil {
			return err
		}
		b.readOnly(dot)
		if !profile.Within(gitDir, wd) {
			return nil // outside the project: not in the box unless the profile lists it
		}
	}
	if mode == profile.GitFull {
		b.readOnly(gitDir)
		return nil
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
		b.readOnly(p)
	}
	return nil
}

// readOnly mounts path read-only at its own place, unless it already is.
func (b *builder) readOnly(path string) {
	if !b.readOnlyCovers(path, slices.IndexFunc(b.mounts, func(m Mount) bool { return m.Dest == path })) {
		b.add(ROBind, path, path)
	}
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

// buildEnv is the program's whole environment, and the names of the
// variables in it that the profile passes from the host. bwrap itself runs
// with an empty environment, so nothing else from the host leaks in.
func buildEnv(h host.Host, p profile.Profile, name, home, wd, path string, programEnv []string) ([]string, []string) {
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
	passed := map[string]bool{}
	for _, k := range p.Env.Pass {
		if v, ok := h.LookupEnv(k); ok {
			env[k], passed[k] = v, true
		}
	}
	for _, kv := range programEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k], passed[k] = v, false
		}
	}
	for k, v := range p.Env.Set {
		env[k], passed[k] = v, false
	}
	out := make([]string, 0, len(env))
	var names []string
	for k, v := range env {
		out = append(out, k+"="+v)
		if passed[k] {
			names = append(names, k)
		}
	}
	sort.Strings(out)
	sort.Strings(names)
	return out, names
}
