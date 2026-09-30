//go:build linux

package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// trustedBwrap are the only places box takes bwrap from: a user folder on
// PATH could hold a fake bwrap that runs the program unsandboxed.
var trustedBwrap = []string{"/usr/local/bin/bwrap", "/usr/bin/bwrap"}

func findBwrap() (string, os.FileInfo, error) {
	for _, p := range trustedBwrap {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return "", nil, fmt.Errorf("%s must be owned by root and not writable by others", p)
		}
		return p, fi, nil
	}
	return "", nil, errors.New("bubblewrap is not installed (sudo apt install bubblewrap)")
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type probeKey struct {
	Bwrap  string `json:"bwrap"`
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	Kernel string `json:"kernel"`
	Boot   string `json:"boot"`
}

type probeCache struct {
	Key   probeKey `json:"key"`
	Probe Probe    `json:"probe"`
}

// RunProbe checks that bwrap exists, is trusted and can create namespaces
// here, and reads the kernel switches box depends on. A successful result
// is cached in stateDir until bwrap, the kernel or the boot changes; fresh
// skips the cache.
func RunProbe(stateDir string, fresh bool) (Probe, error) {
	var p Probe
	if _, err := os.Stat("/run/box/profile"); err == nil {
		p.Nested = true
		return p, errors.New("already inside box: box can't run inside itself")
	}
	bwrap, fi, err := findBwrap()
	if err != nil {
		return p, err
	}
	p.Bwrap = bwrap
	p.Kernel = readTrim("/proc/sys/kernel/osrelease")
	p.LegacyTIOCSTI = readTrim("/proc/sys/dev/tty/legacy_tiocsti")
	p.AppArmorUserns = readTrim("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	_, werr := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	p.WSL = werr == nil || strings.Contains(strings.ToLower(p.Kernel), "microsoft")

	key := probeKey{Bwrap: bwrap, Size: fi.Size(), MTime: fi.ModTime().UnixNano(),
		Kernel: p.Kernel, Boot: readTrim("/proc/sys/kernel/random/boot_id")}
	cachePath := filepath.Join(stateDir, "probe.json")
	if !fresh {
		var c probeCache
		if b, err := os.ReadFile(cachePath); err == nil && json.Unmarshal(b, &c) == nil && c.Key == key {
			c.Probe.Cached = true
			return c.Probe, nil
		}
	}

	out, err := exec.Command(bwrap, "--version").Output()
	if err != nil {
		return p, fmt.Errorf("%s --version: %v", bwrap, err)
	}
	p.Version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "bubblewrap"))

	var stderr bytes.Buffer
	check := exec.Command(bwrap, "--unshare-all", "--die-with-parent", "--ro-bind", "/", "/", "--", "/bin/true")
	check.Env = []string{}
	check.Stderr = &stderr
	done := make(chan error, 1)
	if err := check.Start(); err != nil {
		return p, err
	}
	go func() { done <- check.Wait() }()
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		check.Process.Kill()
		err = errors.New("timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if h := hint(msg, p); h != "" {
			return p, fmt.Errorf("bwrap can't create a sandbox here: %s\n  fix: %s", msg, h)
		}
		return p, fmt.Errorf("bwrap can't create a sandbox here: %v %s", err, msg)
	}

	if b, err := json.Marshal(probeCache{Key: key, Probe: p}); err == nil {
		if os.MkdirAll(stateDir, 0o700) == nil {
			tmp := cachePath + ".tmp"
			if os.WriteFile(tmp, b, 0o600) == nil {
				os.Rename(tmp, cachePath)
			}
		}
	}
	return p, nil
}

// Exec replaces box with bwrap. It only returns on failure, and then
// nothing has run: box never falls back to running unsandboxed.
func Exec(bwrap string, plan *Plan) error {
	fd, err := unix.MemfdCreate("box-profile", 0) // no MFD_CLOEXEC: bwrap reads it
	if err != nil {
		return fmt.Errorf("memfd: %w", err)
	}
	if _, err := unix.Write(fd, plan.Info); err != nil {
		return fmt.Errorf("memfd: %w", err)
	}
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		return fmt.Errorf("memfd: %w", err)
	}
	plan.InfoFD = fd
	// The TUI may leave the terminal non-blocking; programs expect blocking stdio.
	for _, std := range []int{0, 1, 2} {
		unix.SetNonblock(std, false)
	}
	argv := append([]string{"bwrap"}, plan.Args()...)
	err = syscall.Exec(bwrap, argv, plan.Env)
	unix.Close(fd)
	return fmt.Errorf("exec %s: %w", bwrap, err)
}
