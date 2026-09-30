package host

import (
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Fake is an in-memory machine for tests: a filesystem with directories,
// files, symlinks and sockets, an environment and a working folder.
type Fake struct {
	Env   map[string]string
	Cwd   string
	nodes map[string]*node
}

type kind int

const (
	kindDir kind = iota
	kindFile
	kindSymlink
	kindSocket
)

type node struct {
	kind   kind
	mode   fs.FileMode
	target string
	data   []byte
}

// NewFake returns a machine with only "/" in it.
func NewFake() *Fake {
	return &Fake{
		Env:   map[string]string{},
		Cwd:   "/",
		nodes: map[string]*node{"/": {kind: kindDir, mode: 0o755}},
	}
}

// Dir creates a directory and its parents.
func (f *Fake) Dir(p string) *Fake {
	p = filepath.Clean(p)
	if _, ok := f.nodes[p]; ok {
		return f
	}
	f.Dir(filepath.Dir(p))
	f.nodes[p] = &node{kind: kindDir, mode: 0o755}
	return f
}

// File creates a file with the given contents and permission bits.
func (f *Fake) File(p, data string, mode fs.FileMode) *Fake {
	p = filepath.Clean(p)
	f.Dir(filepath.Dir(p))
	f.nodes[p] = &node{kind: kindFile, mode: mode, data: []byte(data)}
	return f
}

// Symlink creates a symlink at p pointing to target.
func (f *Fake) Symlink(p, target string) *Fake {
	p = filepath.Clean(p)
	f.Dir(filepath.Dir(p))
	f.nodes[p] = &node{kind: kindSymlink, mode: 0o777, target: target}
	return f
}

// Socket creates a Unix socket file.
func (f *Fake) Socket(p string) *Fake {
	p = filepath.Clean(p)
	f.Dir(filepath.Dir(p))
	f.nodes[p] = &node{kind: kindSocket, mode: 0o660}
	return f
}

func split(p string) []string {
	p = strings.Trim(filepath.Clean(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// resolve walks p, following symlinks in every component and, when
// followLast is set, in the last one too.
func (f *Fake) resolve(op, p string, followLast bool) (string, *node, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(f.Cwd, p)
	}
	parts := split(p)
	cur := "/"
	hops := 0
	for len(parts) > 0 {
		name := parts[0]
		parts = parts[1:]
		next := filepath.Join(cur, name)
		n, ok := f.nodes[next]
		if !ok {
			return "", nil, &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
		}
		if n.kind == kindSymlink && (len(parts) > 0 || followLast) {
			if hops++; hops > 40 {
				return "", nil, &fs.PathError{Op: op, Path: p, Err: syscall.ELOOP}
			}
			t := n.target
			if !filepath.IsAbs(t) {
				t = filepath.Join(cur, t)
			}
			parts = append(split(t), parts...)
			cur = "/"
			continue
		}
		if len(parts) > 0 && n.kind != kindDir {
			return "", nil, &fs.PathError{Op: op, Path: p, Err: syscall.ENOTDIR}
		}
		cur = next
	}
	return cur, f.nodes[cur], nil
}

func (f *Fake) Getenv(key string) string { return f.Env[key] }

func (f *Fake) LookupEnv(key string) (string, bool) {
	v, ok := f.Env[key]
	return v, ok
}

func (f *Fake) Environ() []string {
	env := make([]string, 0, len(f.Env))
	for k, v := range f.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return env
}

func (f *Fake) Getwd() (string, error) { return f.Cwd, nil }

func (f *Fake) EvalSymlinks(p string) (string, error) {
	real, _, err := f.resolve("lstat", p, true)
	return real, err
}

func (f *Fake) Readlink(p string) (string, error) {
	_, n, err := f.resolve("readlink", p, false)
	if err != nil {
		return "", err
	}
	if n.kind != kindSymlink {
		return "", &fs.PathError{Op: "readlink", Path: p, Err: syscall.EINVAL}
	}
	return n.target, nil
}

func (f *Fake) Lstat(p string) (fs.FileInfo, error) {
	real, n, err := f.resolve("lstat", p, false)
	if err != nil {
		return nil, err
	}
	return info{name: filepath.Base(real), n: n}, nil
}

func (f *Fake) Stat(p string) (fs.FileInfo, error) {
	real, n, err := f.resolve("stat", p, true)
	if err != nil {
		return nil, err
	}
	return info{name: filepath.Base(real), n: n}, nil
}

func (f *Fake) LookPath(file string) (string, error) {
	isExec := func(p string) bool {
		fi, err := f.Stat(p)
		return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
	}
	if strings.Contains(file, "/") {
		if isExec(file) {
			return file, nil
		}
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}
	for _, dir := range filepath.SplitList(f.Env["PATH"]) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if p := filepath.Join(dir, file); isExec(p) {
			return p, nil
		}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

func (f *Fake) ReadFile(p string) ([]byte, error) {
	_, n, err := f.resolve("open", p, true)
	if err != nil {
		return nil, err
	}
	if n.kind != kindFile {
		return nil, &fs.PathError{Op: "read", Path: p, Err: errors.New("not a regular file")}
	}
	return append([]byte(nil), n.data...), nil
}

func (f *Fake) ReadPrefix(p string, n int) ([]byte, error) {
	data, err := f.ReadFile(p)
	if len(data) > n {
		data = data[:n]
	}
	return data, err
}

type info struct {
	name string
	n    *node
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return int64(len(i.n.data)) }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.n.kind == kindDir }
func (i info) Sys() any           { return nil }

func (i info) Mode() fs.FileMode {
	switch i.n.kind {
	case kindDir:
		return fs.ModeDir | i.n.mode
	case kindSymlink:
		return fs.ModeSymlink | i.n.mode
	case kindSocket:
		return fs.ModeSocket | i.n.mode
	}
	return i.n.mode
}
