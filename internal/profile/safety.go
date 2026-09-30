package profile

import (
	"fmt"
	"io/fs"
	"path/filepath"
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
	// WSL turns on the rules for Windows paths under /mnt.
	WSL bool
}

// shellStartup are files a shell runs at startup.
var shellStartup = []string{
	".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".config/fish",
}

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
		if err != nil {
			return err
		}
		for _, have := range *list {
			if have == c {
				return nil
			}
		}
		*list = append(*list, c)
		return nil
	}
	noWrite := []string{
		d.Config,
		d.Data,
		filepath.Join(d.Home, ".ssh"),
		filepath.Join(d.Home, ".gnupg"),
		filepath.Join(d.Home, ".gitconfig"), // aliases, core.fsmonitor, core.hooksPath
		filepath.Join(d.Home, ".config/git"),
		filepath.Join(d.Home, ".config/systemd"),
		filepath.Join(d.Home, ".config/autostart"),
	}
	for _, f := range shellStartup {
		noWrite = append(noWrite, filepath.Join(d.Home, f))
	}
	if boxBinary != "" {
		noWrite = append(noWrite, filepath.Dir(boxBinary))
	}
	// Folders on PATH under $HOME: a program that can write there can plant
	// a command the user will run outside the box.
	for _, dir := range filepath.SplitList(h.Getenv("PATH")) {
		if filepath.IsAbs(dir) {
			if c, err := Canonical(h, dir); err == nil && Within(c, d.Home) && c != d.Home {
				noWrite = append(noWrite, c)
			}
		}
	}
	for _, path := range noWrite {
		if err := add(&p.NoWrite, path); err != nil {
			return Protected{}, err
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
	for _, q := range p.NoMount {
		if overlaps(path, q) {
			return fmt.Errorf("%s can't be mounted: it overlaps %s, which leads out of the box", path, q)
		}
	}
	if fi, err := h.Lstat(path); err == nil && fi.Mode()&fs.ModeSocket != 0 {
		return fmt.Errorf("%s can't be mounted: it's a socket, and a read-only mount doesn't stop connecting to it", path)
	}
	return nil
}

// windowsUserPath reports whether path is /mnt/<drive>, /mnt/<drive>/Users
// or /mnt/<drive>/Users/<name>: the equivalents of / and $HOME on Windows.
func windowsUserPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "mnt" || !isDrive(parts[1]) {
		return false
	}
	switch len(parts) {
	case 2:
		return true
	case 3, 4:
		return strings.EqualFold(parts[2], "Users")
	}
	return false
}

func windowsAppData(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	return len(parts) >= 5 && parts[0] == "mnt" && isDrive(parts[1]) &&
		strings.EqualFold(parts[2], "Users") && strings.EqualFold(parts[4], "AppData")
}

func isDrive(s string) bool {
	return len(s) == 1 && s[0] >= 'a' && s[0] <= 'z'
}
