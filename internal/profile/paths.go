package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/asx8678/box/internal/host"
)

// Dirs are box's folders on the host. Config and Data already end in "box".
type Dirs struct {
	Home   string // the real $HOME, canonical
	Config string // ~/.config/box: profiles and folder memory, never mounted
	Data   string // ~/.local/share/box: private homes
	State  string // ~/.local/state/box: probe cache
}

// DirsFor works out box's folders from $HOME and the XDG variables.
func DirsFor(h host.Host) (Dirs, error) {
	home := h.Getenv("HOME")
	if !filepath.IsAbs(home) {
		return Dirs{}, errors.New("HOME must be set to an absolute path")
	}
	home, err := Canonical(h, home)
	if err != nil {
		return Dirs{}, err
	}
	xdg := func(key, fallback string) (string, error) {
		v := h.Getenv(key)
		if v == "" {
			v = filepath.Join(home, fallback)
		} else if !filepath.IsAbs(v) {
			return "", fmt.Errorf("%s must be an absolute path", key)
		}
		c, err := Canonical(h, v)
		if err != nil {
			return "", err
		}
		return filepath.Join(c, "box"), nil
	}
	d := Dirs{Home: home}
	if d.Config, err = xdg("XDG_CONFIG_HOME", ".config"); err != nil {
		return Dirs{}, err
	}
	if d.Data, err = xdg("XDG_DATA_HOME", ".local/share"); err != nil {
		return Dirs{}, err
	}
	if d.State, err = xdg("XDG_STATE_HOME", ".local/state"); err != nil {
		return Dirs{}, err
	}
	return d, nil
}

// Expand turns "~" and "~/x" into paths under home and cleans absolute
// paths. Anything else is an error.
func Expand(p, home string) (string, error) {
	switch {
	case p == "~":
		return home, nil
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:]), nil
	case filepath.IsAbs(p):
		return filepath.Clean(p), nil
	}
	return "", fmt.Errorf("%q must start with / or ~/", p)
}

// Canonical resolves every symlink in p. If p doesn't exist yet, the part
// that exists is resolved and the rest appended, so safety rules still see
// where a new folder would really be created.
func Canonical(h host.Host, p string) (string, error) {
	p = filepath.Clean(p)
	real, err := h.EvalSymlinks(p)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	// A dangling symlink in the path must not be treated as "doesn't exist".
	if fi, lerr := h.Lstat(p); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a broken symlink", p)
	}
	parent := filepath.Dir(p)
	if parent == p {
		return "", err
	}
	realParent, err := Canonical(h, parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(realParent, filepath.Base(p)), nil
}

// Within reports whether child is parent or lies below it. Both paths must
// be clean and absolute.
func Within(child, parent string) bool {
	if parent == "/" {
		return true
	}
	return child == parent || strings.HasPrefix(child, parent+"/")
}

// overlaps reports whether a and b are the same path or one contains the other.
func overlaps(a, b string) bool {
	return Within(a, b) || Within(b, a)
}
