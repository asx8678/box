package sandbox

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
)

// Exit codes for a program that can't be run, as with docker run.
const (
	ExitCannotRun = 126
	ExitNotFound  = 127
)

// Program is a looked-up program: what to run and what it needs mounted.
type Program struct {
	Name string   // profile key: the basename of what was typed
	Path string   // the invoked path, run as-is inside the sandbox
	Dirs []string // host folders to mount read-only, outside the system paths
	Env  []string // KEY=VALUE the program's kind needs inside the box
}

// LookupError carries the exit code for a program that can't be run.
type LookupError struct {
	Code int
	Err  error
}

func (e *LookupError) Error() string { return e.Err.Error() }
func (e *LookupError) Unwrap() error { return e.Err }

func lookupErr(code int, format string, a ...any) error {
	return &LookupError{Code: code, Err: fmt.Errorf(format, a...)}
}

// Lookup finds name the way a shell would (a path if it contains a slash,
// otherwise the host PATH) and works out which folders it needs: its own
// folder for helper binaries, and for scripts the interpreter's install,
// including a virtualenv's base interpreter.
func Lookup(h host.Host, name, wd string) (Program, error) {
	prog := Program{Name: filepath.Base(name)}
	if !profile.ValidName(prog.Name) {
		return Program{}, lookupErr(ExitCannotRun, "%q can't be used as a profile name", prog.Name)
	}
	path := name
	if strings.Contains(name, "/") {
		if !filepath.IsAbs(path) {
			path = filepath.Join(wd, path)
		}
	} else {
		found, err := h.LookPath(name)
		if err != nil {
			return Program{}, lookupErr(ExitNotFound, "%s: not found on PATH", name)
		}
		path = found
	}
	path = filepath.Clean(path)
	fi, err := h.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Program{}, lookupErr(ExitNotFound, "%s: no such file", path)
	}
	if err != nil {
		return Program{}, lookupErr(ExitCannotRun, "%s: %v", path, err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return Program{}, lookupErr(ExitCannotRun, "%s: not an executable file", path)
	}
	real, err := h.EvalSymlinks(path)
	if err != nil {
		return Program{}, lookupErr(ExitCannotRun, "%s: %v", path, err)
	}
	head, err := h.ReadPrefix(real, 256)
	if err != nil {
		return Program{}, lookupErr(ExitCannotRun, "%s: %v", path, err)
	}
	if profile.WindowsDrive(real) || bytes.HasPrefix(head, []byte("MZ")) {
		return Program{}, lookupErr(ExitCannotRun,
			"%s is a Windows program: it would run outside Linux, where box can't sandbox it", path)
	}
	if profile.Within(path, "/snap") || real == "/usr/bin/snap" {
		return Program{}, lookupErr(ExitCannotRun,
			"%s is a snap: snaps run in their own confinement and can't start inside box; install a non-snap build", path)
	}
	// An AppImage mounts itself with FUSE, which the sandbox doesn't have;
	// this makes it extract to /tmp instead.
	if len(head) >= 11 && bytes.HasPrefix(head, []byte("\x7fELF")) && bytes.Equal(head[8:11], []byte("AI\x02")) {
		prog.Env = append(prog.Env, "APPIMAGE_EXTRACT_AND_RUN=1")
	}
	// Run it by its path through canonical folders: only those are mounted.
	// The name stays as typed, for programs that look at argv[0].
	dir, err := profile.Canonical(h, filepath.Dir(path))
	if err != nil {
		return Program{}, lookupErr(ExitCannotRun, "%s: %v", path, err)
	}
	prog.Path = filepath.Join(dir, filepath.Base(path))

	dirs := &dirSet{h: h}
	dirs.add(filepath.Dir(path))
	dirs.add(filepath.Dir(real))
	if interp, ok := shebang(head); ok {
		if err := dirs.interpreter(interp); err != nil {
			return Program{}, lookupErr(ExitCannotRun, "%s: %v", path, err)
		}
	}
	prog.Dirs = dirs.list
	return prog, nil
}

// shebang returns the interpreter a script names, resolving "/usr/bin/env X"
// to X (the caller looks it up on PATH).
func shebang(head []byte) (string, bool) {
	line, ok := bytes.CutPrefix(head, []byte("#!"))
	if !ok {
		return "", false
	}
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return "", false
	}
	if filepath.Base(fields[0]) != "env" {
		return fields[0], true
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") || strings.Contains(f, "=") {
			continue // env options such as -S, or VAR=value assignments
		}
		return f, true
	}
	return "", false
}

type dirSet struct {
	h    host.Host
	list []string
}

// add records dir if it isn't under the system paths box always mounts.
func (d *dirSet) add(dir string) {
	c, err := profile.Canonical(d.h, dir)
	// "/" is the prefix of /bin/sh; the real /usr/bin/sh is covered.
	if err == nil && c != "/" && !systemPath(c) && !slices.Contains(d.list, c) {
		d.list = append(d.list, c)
	}
}

// interpreter adds the install folders of a script's interpreter: the
// prefix above its bin/ folder, and for a virtualenv the base interpreter's
// prefix from pyvenv.cfg.
func (d *dirSet) interpreter(name string) error {
	path := name
	if !filepath.IsAbs(name) {
		found, err := d.h.LookPath(name)
		if err != nil {
			return fmt.Errorf("interpreter %s not found on PATH", name)
		}
		path = found
	}
	if _, err := d.h.Stat(path); err != nil {
		return fmt.Errorf("interpreter %s: %v", path, err)
	}
	venv := prefix(path)
	d.add(venv)
	if cfg, err := d.h.ReadFile(filepath.Join(venv, "pyvenv.cfg")); err == nil {
		for _, line := range strings.Split(string(cfg), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(k) == "home" {
				d.add(prefix(filepath.Join(strings.TrimSpace(v), "python")))
			}
		}
	}
	if real, err := d.h.EvalSymlinks(path); err == nil {
		d.add(prefix(real))
	}
	return nil
}

// prefix is the install folder of an executable: the parent of its bin/
// folder, or the folder itself when it isn't called bin.
func prefix(exe string) string {
	dir := filepath.Dir(exe)
	if filepath.Base(dir) == "bin" {
		return filepath.Dir(dir)
	}
	return dir
}

// systemPath reports whether p is inside the system folders every sandbox
// gets read-only.
func systemPath(p string) bool {
	if profile.Within(p, "/usr") {
		return true
	}
	for _, dir := range usrMerged {
		if profile.Within(p, dir) {
			return true
		}
	}
	return false
}
