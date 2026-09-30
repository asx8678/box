// Package host puts the operating system behind an interface, so that the
// profile and sandbox logic can be tested against a fake filesystem.
package host

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// Host is everything box reads from the machine it runs on.
type Host interface {
	Getenv(key string) string
	LookupEnv(key string) (string, bool)
	Environ() []string
	Getwd() (string, error)
	EvalSymlinks(path string) (string, error)
	Readlink(path string) (string, error)
	Lstat(path string) (fs.FileInfo, error)
	Stat(path string) (fs.FileInfo, error)
	LookPath(file string) (string, error)
	ReadFile(path string) ([]byte, error)
}

// OS is the real machine.
type OS struct{}

func (OS) Getenv(key string) string                 { return os.Getenv(key) }
func (OS) LookupEnv(key string) (string, bool)      { return os.LookupEnv(key) }
func (OS) Environ() []string                        { return os.Environ() }
func (OS) Getwd() (string, error)                   { return os.Getwd() }
func (OS) EvalSymlinks(path string) (string, error) { return filepath.EvalSymlinks(path) }
func (OS) Readlink(path string) (string, error)     { return os.Readlink(path) }
func (OS) Lstat(path string) (fs.FileInfo, error)   { return os.Lstat(path) }
func (OS) Stat(path string) (fs.FileInfo, error)    { return os.Stat(path) }
func (OS) LookPath(file string) (string, error)     { return exec.LookPath(file) }
func (OS) ReadFile(path string) ([]byte, error)     { return os.ReadFile(path) }
