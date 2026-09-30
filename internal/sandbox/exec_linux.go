//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/asx8678/box/internal/profile"
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

// probeCache remembers the one slow result, a working namespace check, and
// bwrap's version. The kernel switches are cheap and read on every run, so
// a sysctl changed since the last check is never missed.
type probeCache struct {
	Key     probeKey `json:"key"`
	Version string   `json:"version"`
}

// RunProbe checks that bwrap exists, is trusted and can create namespaces
// here, and reads the kernel switches box depends on. A successful result
// is cached in stateDir until bwrap, the kernel or the boot changes; fresh
// skips the cache.
func RunProbe(stateDir string, fresh bool) (Probe, error) {
	var p Probe
	if _, err := os.Stat("/run/box/profile"); err == nil {
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
			p.Version = c.Version
			return p, nil
		}
	}

	out, err := exec.Command(bwrap, "--version").Output()
	if err != nil {
		return p, fmt.Errorf("%s --version: %v", bwrap, err)
	}
	p.Version = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "bubblewrap"))

	var stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	check := exec.CommandContext(ctx, bwrap, "--unshare-all", "--die-with-parent", "--ro-bind", "/", "/", "--", "/bin/true")
	check.Env = []string{}
	check.Stderr = &stderr
	check.WaitDelay = time.Second
	if err = check.Run(); ctx.Err() != nil {
		err = errors.New("timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if h := hint(msg, p); h != "" {
			return p, fmt.Errorf("bwrap can't create a sandbox here: %s\n  fix: %s", msg, h)
		}
		return p, fmt.Errorf("bwrap can't create a sandbox here: %v %s", err, msg)
	}

	if b, err := json.Marshal(probeCache{Key: key, Version: p.Version}); err == nil {
		profile.WriteAtomic(cachePath, b) // a cache: failing to write it only costs time
	}
	return p, nil
}

// hint turns bwrap's error text from the namespace check into a fix.
func hint(stderr string, p Probe) string {
	switch {
	case strings.Contains(stderr, "setting up uid map: Permission denied"),
		strings.Contains(stderr, "Failed RTM_NEWADDR"):
		if p.AppArmorUserns == "1" {
			return "Ubuntu's AppArmor blocks user namespaces for bwrap. Either load the bwrap profile " +
				"(sudo apt install apparmor-profiles; sudo ln -s /usr/share/apparmor/extra-profiles/bwrap-userns-restrict " +
				"/etc/apparmor.d/ && sudo systemctl reload apparmor) or allow them for everyone: " +
				"sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0"
		}
		return "the kernel refused to map your user into a new namespace"
	case strings.Contains(stderr, "No permissions to create new namespace"),
		strings.Contains(stderr, "Creating new namespace failed"):
		return "unprivileged user namespaces are disabled; check sysctl user.max_user_namespaces " +
			"(and kernel.unprivileged_userns_clone on older Debian)"
	}
	return ""
}

// memfd returns an inheritable in-memory file holding data, at offset 0.
func memfd(name string, data []byte) (int, error) {
	fd, err := unix.MemfdCreate(name, 0) // no MFD_CLOEXEC: bwrap reads it
	if err != nil {
		return -1, fmt.Errorf("memfd: %w", err)
	}
	if _, err := unix.Write(fd, data); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("memfd: %w", err)
	}
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("memfd: %w", err)
	}
	return fd, nil
}

// Exec replaces box with bwrap. It only returns on failure, and then
// nothing has run: box never falls back to running unsandboxed.
func Exec(bwrap string, plan *Plan) error {
	fd, err := memfd("box-profile", plan.Info)
	if err != nil {
		return err
	}
	plan.InfoFD = fd
	if plan.Filter.Any() {
		prog, err := SeccompProgram(runtime.GOARCH, plan.Filter)
		if err != nil {
			return err
		}
		if plan.SeccompFD, err = memfd("box-seccomp", prog); err != nil {
			return err
		}
	}
	// The TUI may leave the terminal non-blocking; programs expect blocking stdio.
	for _, std := range []int{0, 1, 2} {
		unix.SetNonblock(std, false)
	}
	if plan.FlushInput {
		// Replies to the TUI's terminal queries may still be queued; the
		// program would read them as typed input.
		unix.IoctlSetInt(0, unix.TCFLSH, unix.TCIFLUSH)
	}
	// Landlock and no_new_privs apply to the calling thread, so the thread
	// that sets them must be the one that calls execve.
	runtime.LockOSThread()
	if plan.Landlock {
		if err := landlockScope(); err != nil {
			return err
		}
	}
	argv := append([]string{"bwrap"}, plan.Args()...)
	err = syscall.Exec(bwrap, argv, plan.Env)
	return fmt.Errorf("exec %s: %w", bwrap, err)
}

// LandlockABI returns the kernel's Landlock ABI version, or 0 without it.
func LandlockABI() int {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0
	}
	return int(v)
}

// landlockScope stops this thread, and everything it execs, from
// connecting to abstract Unix sockets created outside it (such as an X11
// server's). It has no filesystem rules, so bwrap can still mount.
func landlockScope() error {
	if abi := LandlockABI(); abi < 6 {
		return fmt.Errorf("sandbox.landlock needs Landlock ABI 6 (Linux 6.12+); this kernel has %d", abi)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	attr := unix.LandlockRulesetAttr{Scoped: unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer unix.Close(int(fd))
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}
