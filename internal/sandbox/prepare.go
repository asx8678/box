package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Prepare runs just before exec. It creates the program's missing folders,
// then creates every mount point that lies inside a read-write mount itself,
// refusing symlinks on the way.
//
// Why: bwrap creates missing mount points, and before bubblewrap 0.12
// (CVE-2026-87766; Ubuntu's package is still affected) it follows symlinks
// while doing so. A program that planted a symlink in its private home or
// project on one run could make bwrap create files anywhere on the next.
func Prepare(plan *Plan) error {
	for _, dir := range plan.Create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	for i, m := range plan.Mounts {
		parent, ok := covering(plan.Mounts[:i], m.Dest)
		if !ok || parent.Kind != Bind {
			continue // on a fresh tmpfs or a read-only mount: nothing to plant
		}
		isDir := true
		if m.Kind == ROBind || m.Kind == Bind || m.Kind == ROBindTry {
			fi, err := os.Stat(m.Src)
			if err != nil {
				if m.Kind == ROBindTry {
					continue
				}
				return err
			}
			isDir = fi.IsDir()
		} else if m.Kind == ROBindData || m.Kind == Symlink {
			isDir = false
		}
		rel := strings.TrimPrefix(m.Dest, strings.TrimSuffix(parent.Dest, "/")+"/")
		if err := makePath(parent.Src, rel, isDir, m.Kind == Symlink); err != nil {
			return fmt.Errorf("preparing %s: %w", m.Dest, err)
		}
	}
	return nil
}

// makePath walks rel below base one component at a time without following
// symlinks, creating missing folders (0700) and, for a file mount point, an
// empty file (0600). For a symlink operation the last component must not
// exist or already be a symlink (bwrap replaces an identical one).
func makePath(base, rel string, isDir, symlink bool) error {
	const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(base, dirFlags, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", base, err)
	}
	defer func() { unix.Close(fd) }() // the folder reached so far
	parts := strings.Split(rel, "/")
	at := base
	for i, name := range parts {
		last := i == len(parts)-1
		at = filepath.Join(at, name)
		if last && symlink {
			var st unix.Stat_t
			if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT != unix.S_IFLNK {
				return fmt.Errorf("%s exists and isn't a symlink", at)
			}
			return nil
		}
		if last && !isDir {
			nfd, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if errors.Is(err, unix.ENOENT) {
				nfd, err = unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			}
			if err != nil {
				return pathErr(fd, name, at, err)
			}
			defer unix.Close(nfd)
			var st unix.Stat_t
			if err := unix.Fstat(nfd, &st); err != nil {
				return err
			}
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				return fmt.Errorf("%s is a folder, but a file is mounted there", at)
			}
			return nil
		}
		nfd, err := unix.Openat(fd, name, dirFlags, 0)
		if errors.Is(err, unix.ENOENT) {
			if err := unix.Mkdirat(fd, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				return pathErr(fd, name, at, err)
			}
			nfd, err = unix.Openat(fd, name, dirFlags, 0)
		}
		if err != nil {
			return pathErr(fd, name, at, err)
		}
		unix.Close(fd)
		fd = nfd
	}
	return nil
}

// pathErr explains a failed open of name in dir. Systems differ in the
// errno for a symlink opened with O_NOFOLLOW (ELOOP or ENOTDIR), so the
// entry's type is checked directly.
func pathErr(dir int, name, path string, err error) error {
	var st unix.Stat_t
	if unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			return fmt.Errorf("%s is a symlink; box won't let bwrap follow it (remove it and run again)", path)
		case unix.S_IFDIR:
		default:
			if errors.Is(err, unix.ENOTDIR) {
				return fmt.Errorf("%s is not a folder", path)
			}
		}
	}
	return fmt.Errorf("%s: %w", path, err)
}
