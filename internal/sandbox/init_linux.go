//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/asx8678/box/internal/egress"
)

// Init runs inside the sandbox as box's init (profile sandbox.init, plan
// B3). bwrap's outer process stays in the terminal's foreground process
// group and dies on Ctrl+C, which SIGKILLs the whole box. Init moves the
// program into its own process group and gives that group the terminal,
// so Ctrl+C, Ctrl+\ and resizes reach only the program. Ctrl+Z isn't
// supported (as with docker run -it): a stopped program is resumed at once,
// because nothing outside could resume it.
func Init(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "box init: no program")
		return 125
	}
	// Only a sandbox has /run/box/profile (outside, /run is root's), so
	// --box-init can't be used to run a program with no sandbox.
	if _, err := os.Stat("/run/box/profile"); err != nil {
		fmt.Fprintln(os.Stderr, "box: --box-init only runs inside a box")
		return 125
	}
	// In a restricted box, the program's proxy settings point at loopback
	// ports; the bridge carries them to box's proxy outside. Without it the
	// program has no network, which is the safe way to fail.
	if _, err := os.Stat(EgressPath); err == nil {
		if err := egress.Bridge(EgressPath); err != nil {
			fmt.Fprintf(os.Stderr, "box init: the network bridge didn't start (%v); the program has no network\n", err)
		}
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	_, ttyErr := unix.IoctlGetTermios(0, unix.TCGETS)
	tty := ttyErr == nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: tty, Ctty: 0}

	// Signals sent to init (by bwrap's exit or a kill) go to the program.
	// Notify installs handlers, which exec resets to default in the child,
	// unlike ignoring them, which the child would inherit.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "box init: %v\n", err)
		var ee *exec.Error
		if errors.As(err, &ee) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	pid := cmd.Process.Pid
	// Init is in the background now. Ignoring SIGTTOU lets it hand the
	// terminal back to the program (a caught SIGTTOU would restart the
	// ioctl forever); the program has already exec'd, so it doesn't inherit this.
	signal.Ignore(unix.SIGTTOU, unix.SIGTTIN)
	go func() {
		for s := range sigs {
			unix.Kill(-pid, s.(syscall.Signal))
		}
	}()

	for {
		var ws unix.WaitStatus
		_, err := unix.Wait4(pid, &ws, unix.WUNTRACED, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "box init: %v\n", err)
			return 125
		}
		switch {
		case ws.Stopped():
			if tty {
				unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, pid)
			}
			unix.Kill(-pid, unix.SIGCONT)
		case ws.Exited():
			return ws.ExitStatus()
		case ws.Signaled():
			return 128 + int(ws.Signal())
		}
	}
}
