// Package profile holds box's per-program profiles: the TOML schema, loading
// and saving them, validation, and the path safety rules.
package profile

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
)

// Version is the profile schema version box reads and writes.
const Version = 1

// Profile is one program's sandbox settings, stored as
// ~/.config/box/profiles/<program>/<name>.toml.
type Profile struct {
	Version int    `toml:"version"`
	Program string `toml:"program"`
	Network bool   `toml:"network"`
	// Tools are other commands the program runs (kiro-cli for Kiro Crew,
	// node or uvx for MCP servers). Each is looked up on the host PATH like
	// the program and its install folder mounted read-only; missing ones
	// are skipped.
	Tools   []string `toml:"tools"`
	Workdir Workdir  `toml:"workdir"`
	Home    Mounts   `toml:"home"`
	Extra   Mounts   `toml:"extra"`
	Env     Env      `toml:"env"`
	System  System   `toml:"system"`
	Sandbox Sandbox  `toml:"sandbox"`
}

// Workdir is how the folder box runs in is mounted.
type Workdir struct {
	Mode       string `toml:"mode"`        // "rw" or "ro"
	ProtectGit bool   `toml:"protect_git"` // keep .git/config and .git/hooks read-only
}

// Mounts lists host folders mounted at the same path inside the sandbox.
type Mounts struct {
	RW []string `toml:"rw"`
	RO []string `toml:"ro"`
}

// Env says which variables reach the program.
type Env struct {
	Pass []string          `toml:"pass"` // copied from the host at run time
	Set  map[string]string `toml:"set"`
}

// System adds read-only paths to box's built-in system list.
type System struct {
	ExtraRO []string `toml:"extra_ro"`
}

// Sandbox holds advanced switches that the TUI doesn't show.
type Sandbox struct {
	Strict bool `toml:"strict"` // adds --disable-userns: no nested sandboxes
	// Init runs the program under a small init from box inside the
	// sandbox, which gives it the terminal's foreground so Ctrl+C reaches
	// only the program, not the whole box (plan B3; decided in the M0 spike).
	Init bool `toml:"init"`
	// Landlock blocks abstract Unix sockets outside the box when network
	// is on (plan S3; needs Linux 6.12+, decided in the M0 spike).
	Landlock bool `toml:"landlock"`
}

// Default is the generic starting point for any program: the project folder
// read-write, network off, a private home and nothing else.
func Default(program string) Profile {
	return Profile{
		Version: Version,
		Program: program,
		Workdir: Workdir{Mode: "rw", ProtectGit: true},
		Env:     Env{Set: map[string]string{}},
	}
}

// Parse decodes a profile on top of the defaults, rejecting unknown keys.
func Parse(data []byte, program string) (Profile, error) {
	p := Default(program)
	md, err := toml.Decode(string(data), &p)
	if err != nil {
		return Profile{}, err
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return Profile{}, fmt.Errorf("unknown keys: %s", strings.Join(names, ", "))
	}
	if p.Env.Set == nil {
		p.Env.Set = map[string]string{}
	}
	return p, nil
}

// Load reads and validates the profile for program at path. The file must be
// owned by the user and not writable by anyone else.
func Load(path, program string) (Profile, error) {
	if err := checkOwner(path); err != nil {
		return Profile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	p, err := Parse(data, program)
	if err != nil {
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := p.Validate(program); err != nil {
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Save validates p and writes it atomically with mode 0600, creating its
// folder with mode 0700.
func Save(path string, p Profile) error {
	if err := p.Validate(p.Program); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(p); err != nil {
		return err
	}
	return WriteAtomic(path, buf.Bytes())
}

// WriteAtomic writes data with mode 0600 to a temporary file next to path
// and renames it into place, so readers never see a half-written file. The
// folder is created with mode 0700 if missing.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// checkOwner refuses files owned by someone else or writable by group or
// others: whoever can write a profile decides what the sandbox allows.
func checkOwner(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	return checkPrivate(path, fi, 0o022, "writable by group or others (chmod go-w "+path+")")
}

// checkPrivate refuses path if it isn't owned by the user or has any of
// the permission bits in mask set; problem says what the bits mean.
func checkPrivate(path string, fi fs.FileInfo, mask fs.FileMode, problem string) error {
	if fi.Mode().Perm()&mask != 0 {
		return fmt.Errorf("%s: %s", path, problem)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s: not owned by you", path)
	}
	return nil
}

// Path is where the profile name for program is stored.
func Path(d Dirs, program, name string) string {
	return filepath.Join(d.Config, "profiles", program, name+".toml")
}

// HomeDir is the private home folder for program's profile name.
func HomeDir(d Dirs, program, name string) string {
	return filepath.Join(d.Data, "homes", program, name)
}

// List returns the names of program's saved profiles, sorted.
func List(d Dirs, program string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.Config, "profiles", program))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".toml")
		if ok && e.Type().IsRegular() && ValidName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
