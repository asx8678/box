package profile

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asx8678/box/internal/host"
)

// Protected is the set of host paths the safety rules guard. All paths are
// canonical. Build it with NewProtected.
type Protected struct {
	Home string
	// NoWrite may not be mounted read-write, nor may anything that contains
	// them or lies inside them: writing there means code that runs outside
	// the box next time.
	NoWrite []string
	// NoMount may not be mounted at all, in any mode: sockets and folders
	// that hand the program a way out of the box.
	NoMount []string
	// Box is box's own config, data and state, which no box may see.
	Box []string
	// Secrets hold credentials. A profile may list them read-only, but the
	// folders box works out from a program (its shebang, a virtualenv's
	// pyvenv.cfg) may not reach them: a script the program could write
	// would otherwise choose them.
	Secrets []string
	// WSL turns on the rules for Windows paths under /mnt.
	WSL bool
}

// startupFiles are files in the home folder that run code, or decide what
// runs, the next time something starts outside the box: shells, editors,
// git, docker and npm. configFiles are the same under the config folder.
var (
	startupFiles = []string{
		".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".bash_aliases", ".profile",
		".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout", ".inputrc",
		".vimrc", ".vim", ".emacs", ".emacs.d", ".tmux.conf",
		".gitconfig", // aliases, core.fsmonitor, core.hooksPath
		".ssh", ".gnupg", ".aws", ".kube", ".docker", ".npmrc",
	}
	configFiles = []string{"git", "fish", "nvim", "pip", "Code", "systemd", "autostart", "environment.d"}
	secretFiles = []string{".ssh", ".gnupg", ".aws", ".kube", ".docker", ".netrc", ".git-credentials", ".password-store"}
)

// noMount are daemon sockets and Windows interop paths.
var noMount = []string{
	"/run/user",
	"/run/WSL",
	"/mnt/wslg",
	"/run/docker.sock",
	"/var/run/docker.sock",
	"/run/podman",
	"/run/containerd",
}

// NewProtected builds the protected set for this machine. boxBinary is the
// path of the running box executable ("" to skip it).
func NewProtected(h host.Host, d Dirs, boxBinary string, wsl bool) (Protected, error) {
	p := Protected{Home: d.Home, WSL: wsl}
	add := func(list *[]string, path string) error {
		c, err := Canonical(h, path)
		if err == nil && !slices.Contains(*list, c) {
			*list = append(*list, c)
		}
		return err
	}
	noWrite := []string{d.Config, d.Data}
	for _, f := range startupFiles {
		noWrite = append(noWrite, filepath.Join(d.Home, f))
	}
	// Both ~/.config and $XDG_CONFIG_HOME, whichever programs read.
	for _, base := range []string{filepath.Join(d.Home, ".config"), filepath.Dir(d.Config)} {
		for _, f := range configFiles {
			noWrite = append(noWrite, filepath.Join(base, f))
		}
	}
	secrets := []string{filepath.Join(filepath.Dir(d.Config), "gh")}
	for _, f := range secretFiles {
		secrets = append(secrets, filepath.Join(d.Home, f))
	}
	if boxBinary != "" {
		noWrite = append(noWrite, filepath.Dir(boxBinary))
	}
	// Every folder on PATH: a program that can write there can plant a
	// command the user will run outside the box.
	for _, dir := range filepath.SplitList(h.Getenv("PATH")) {
		if filepath.IsAbs(dir) && filepath.Clean(dir) != "/" {
			noWrite = append(noWrite, dir)
		}
	}
	for _, l := range []struct {
		list  *[]string
		paths []string
	}{{&p.NoWrite, noWrite}, {&p.Box, []string{d.Config, d.Data, d.State}}, {&p.Secrets, secrets}} {
		for _, path := range l.paths {
			if err := add(l.list, path); err != nil {
				return Protected{}, err
			}
		}
	}
	if rt := h.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(rt) {
		if err := add(&p.NoMount, rt); err != nil {
			return Protected{}, err
		}
	}
	for _, path := range noMount {
		if err := add(&p.NoMount, path); err != nil {
			return Protected{}, err
		}
	}
	return p, nil
}

// CheckWorkdir refuses working folders that would expose too much.
func (p Protected) CheckWorkdir(wd string) error {
	if wd == "/" {
		return fmt.Errorf("refusing to run in /")
	}
	if Within(p.Home, wd) {
		return fmt.Errorf("refusing to run in %s: it contains your whole home folder; cd into a project first", wd)
	}
	if p.WSL && windowsUserPath(wd) {
		return fmt.Errorf("refusing to run in %s: it contains a Windows user folder", wd)
	}
	return nil
}

// CheckRW refuses a read-write mount of a protected path.
func (p Protected) CheckRW(path string) error {
	if Within(p.Home, path) {
		return fmt.Errorf("%s can't be read-write: it contains your whole home folder", path)
	}
	for _, q := range p.NoWrite {
		if overlaps(path, q) {
			return fmt.Errorf("%s can't be read-write: it overlaps %s", path, q)
		}
	}
	if p.WSL {
		if windowsUserPath(path) {
			return fmt.Errorf("%s can't be read-write: it contains a Windows user folder", path)
		}
		if windowsAppData(path) {
			return fmt.Errorf("%s can't be read-write: Windows runs programs from AppData", path)
		}
	}
	return nil
}

// CheckMount refuses mounting daemon sockets, interop paths or anything
// containing them, in any mode.
func (p Protected) CheckMount(h host.Host, path string) error {
	if Within(p.Home, path) {
		return fmt.Errorf("%s can't be mounted: it contains your whole home folder; pick the folders inside it the program needs", path)
	}
	for _, q := range p.NoMount {
		if overlaps(path, q) {
			return fmt.Errorf("%s can't be mounted: it overlaps %s, which leads out of the box", path, q)
		}
	}
	for _, q := range p.Box {
		if overlaps(path, q) {
			return fmt.Errorf("%s can't be mounted: it overlaps %s, where box keeps its own files", path, q)
		}
	}
	if fi, err := h.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		return fmt.Errorf("%s can't be mounted: it's a socket, and a read-only mount doesn't stop connecting to it", path)
	}
	return nil
}

// CheckSecret refuses a folder box worked out from the program, its
// interpreter or a virtualenv when it overlaps a credentials folder.
func (p Protected) CheckSecret(path string) error {
	for _, q := range p.Secrets {
		if overlaps(path, q) {
			return fmt.Errorf("the program's folder %s overlaps %s, which holds credentials; "+
				"a script's interpreter line or pyvenv.cfg pointed there, so box won't mount it", path, q)
		}
	}
	return nil
}

// windowsUserPath reports whether path is /mnt/<drive>, /mnt/<drive>/Users
// or /mnt/<drive>/Users/<name>: the equivalents of / and $HOME on Windows.
func windowsUserPath(path string) bool {
	parts, ok := driveParts(path)
	switch {
	case !ok:
		return false
	case len(parts) == 0:
		return true
	case len(parts) <= 2:
		return strings.EqualFold(parts[0], "Users")
	}
	return false
}

func windowsAppData(path string) bool {
	parts, ok := driveParts(path)
	return ok && len(parts) >= 3 && strings.EqualFold(parts[0], "Users") && strings.EqualFold(parts[2], "AppData")
}

// WindowsDrive reports whether path is a Windows drive mounted by WSL,
// /mnt/<letter>, or lies below one.
func WindowsDrive(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) >= 2 && parts[0] == "mnt" && len(parts[1]) == 1 && parts[1][0] >= 'a' && parts[1][0] <= 'z'
}

// driveParts is the path below a Windows drive, split into its parts
// (none for the drive itself), and false for a path that isn't on one.
func driveParts(path string) ([]string, bool) {
	if !WindowsDrive(path) {
		return nil, false
	}
	return strings.Split(strings.Trim(path, "/"), "/")[2:], true
}
