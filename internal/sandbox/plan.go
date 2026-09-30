// Package sandbox turns a profile into a bubblewrap invocation: the mount
// list, the environment and the command.
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
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

// InfoFD is the file descriptor that carries /run/box/profile to bwrap.
const InfoFD = 3

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
	Network     *bool    // --net / --no-net override for this run
	// LegacyTIOCSTI is /proc/sys/dev/tty/legacy_tiocsti: "0" means the
	// kernel already blocks terminal injection; "1" or "" (unknown) means
	// box falls back to --new-session.
	LegacyTIOCSTI string
}

// Plan is a finished sandbox: bwrap's arguments plus the environment
// passed to it.
type Plan struct {
	Flags   []string
	Mounts  []Mount
	Chdir   string
	Env     []string // KEY=VALUE, sorted; the program's whole environment
	Command []string
	Info    []byte // contents of /run/box/profile
	// Writable lists the host folders mounted read-write, for the
	// mount-point preparation done before exec.
	Writable []string
	// NewSession is set when box had to fall back to --new-session.
	NewSession bool
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

	// System: read-only, as on the host.
	b.add(ROBind, "/usr", "/usr")
	for _, dir := range usrMerged {
		fi, err := h.Lstat(dir)
		switch {
		case err != nil:
			// Not on this machine.
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := h.Readlink(dir)
			if err != nil {
				return nil, err
			}
			b.add(Symlink, target, dir)
		case fi.IsDir():
			b.add(ROBind, dir, dir)
		}
	}
	for _, f := range systemFiles {
		b.add(ROBindTry, f, f)
	}
	for _, raw := range p.System.ExtraRO {
		if err := b.hostPath("system.extra_ro", raw, home, ROBind, true); err != nil {
			return nil, err
		}
	}

	// Fresh, empty filesystems owned by the sandbox.
	b.add(Proc, "", "/proc")
	b.add(Dev, "", "/dev")
	b.add(Tmpfs, "", "/tmp")
	b.add(Tmpfs, "", "/run")
	b.add(ROBindData, "", "/run/box/profile")

	// The private home, then the program's own folders on top of it.
	privateHome, err := profile.Canonical(h, profile.HomeDir(in.Dirs, p.Program, in.ProfileName))
	if err != nil {
		return nil, err
	}
	b.add(Bind, privateHome, home)
	b.writable = append(b.writable, privateHome)
	for _, raw := range p.Home.RW {
		if err := b.hostPath("home.rw", raw, home, Bind, false); err != nil {
			return nil, err
		}
	}
	for _, raw := range p.Home.RO {
		if err := b.hostPath("home.ro", raw, home, ROBind, false); err != nil {
			return nil, err
		}
	}

	// The program's own folders, read-only, unless something already
	// covers them with the same mount.
	for _, dir := range in.ProgramDirs {
		c, err := profile.Canonical(h, dir)
		if err != nil {
			return nil, err
		}
		if b.covered(c) {
			continue
		}
		if err := b.prot.CheckMount(h, c); err != nil {
			return nil, err
		}
		b.add(ROBind, c, c)
	}

	for _, raw := range p.Extra.RW {
		if err := b.hostPath("extra.rw", raw, home, Bind, true); err != nil {
			return nil, err
		}
	}
	for _, raw := range p.Extra.RO {
		if err := b.hostPath("extra.ro", raw, home, ROBind, true); err != nil {
			return nil, err
		}
	}

	// The project folder.
	wd, err := profile.Canonical(h, in.Workdir)
	if err != nil {
		return nil, err
	}
	if err := b.prot.CheckWorkdir(wd); err != nil {
		return nil, err
	}
	if err := b.prot.CheckMount(h, wd); err != nil {
		return nil, err
	}
	if p.Workdir.Mode == "rw" {
		if err := b.prot.CheckRW(wd); err != nil {
			return nil, err
		}
		b.add(Bind, wd, wd)
		b.writable = append(b.writable, wd)
		if p.Workdir.ProtectGit {
			if err := b.protectGit(wd); err != nil {
				return nil, err
			}
		}
	} else {
		b.add(ROBind, wd, wd)
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
		Command:  append([]string{in.Program}, in.Args...),
		Writable: b.writable,
	}
	plan.Flags = []string{"--unshare-all"}
	if network {
		plan.Flags = append(plan.Flags, "--share-net")
	}
	plan.Flags = append(plan.Flags, "--die-with-parent")
	if in.LegacyTIOCSTI != "0" {
		plan.Flags = append(plan.Flags, "--new-session")
		plan.NewSession = true
	}
	if p.Sandbox.Strict {
		plan.Flags = append(plan.Flags, "--disable-userns")
	}
	plan.Env = buildEnv(h, p, in.ProfileName, home, wd, visiblePath(h, b.mounts, privateHome))
	plan.Info = fmt.Appendf(nil, "program=%s\nprofile=%s\nnetwork=%t\nworkdir=%s\n",
		p.Program, in.ProfileName, network, wd)
	return plan, nil
}

type builder struct {
	h        host.Host
	prot     profile.Protected
	mounts   []Mount
	writable []string
}

func (b *builder) add(k Kind, src, dest string) {
	b.mounts = append(b.mounts, Mount{Kind: k, Src: src, Dest: dest})
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
	if mustExist {
		if _, err := b.h.Stat(c); err != nil {
			return fmt.Errorf("%s: %s doesn't exist", section, raw)
		}
	}
	if err := b.prot.CheckMount(b.h, c); err != nil {
		return fmt.Errorf("%s: %w", section, err)
	}
	if k == Bind {
		if err := b.prot.CheckRW(c); err != nil {
			return fmt.Errorf("%s: %w", section, err)
		}
		b.writable = append(b.writable, c)
	}
	b.add(k, c, c)
	return nil
}

// covered reports whether dir is already mounted read-only at the same
// path, directly or through a parent, so a program folder adds nothing.
func (b *builder) covered(dir string) bool {
	for _, m := range b.mounts {
		if (m.Kind == ROBind || m.Kind == Bind) && m.Src == m.Dest && profile.Within(dir, m.Dest) {
			return true
		}
	}
	return false
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
func buildEnv(h host.Host, p profile.Profile, name, home, wd, path string) []string {
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
