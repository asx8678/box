//go:build linux

package e2e

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/asx8678/box/internal/profile"
	"github.com/asx8678/box/internal/sandbox"
)

// session is box running on a pseudo-terminal, as it would in a terminal
// window: box's stdio is the pty, and it's the controlling terminal.
type session struct {
	t      *testing.T
	master *os.File
	cmd    *exec.Cmd
	mu     sync.Mutex
	out    bytes.Buffer
	done   chan struct{}
	code   int
}

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no ptys here: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.IoctlSetWinsize(int(m.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80})
	return m, s
}

func (e *env) terminal(args ...string) *session {
	e.t.Helper()
	master, slave := openPTY(e.t)
	cmd := exec.Command(boxBin, args...)
	cmd.Dir = e.proj
	cmd.Env = []string{"HOME=" + e.home, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=xterm", "LANG=C.UTF-8"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	slave.Close()
	s := &session{t: e.t, master: master, cmd: cmd, done: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			s.code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				s.code = 128 + int(ws.Signal())
			}
		}
		close(s.done)
	}()
	e.t.Cleanup(func() {
		cmd.Process.Kill()
		master.Close()
	})
	return s
}

func (s *session) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// waitFor waits until the output contains text; false after the timeout.
func (s *session) waitFor(text string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(s.output(), text) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (s *session) exited(timeout time.Duration) bool {
	select {
	case <-s.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *session) must(text string) {
	s.t.Helper()
	if !s.waitFor(text, 10*time.Second) {
		s.t.Fatalf("never saw %q; output:\n%s", text, s.output())
	}
}

func needPython(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		if os.Getenv("BOX_E2E_REQUIRED") != "" {
			t.Fatal("needs /usr/bin/python3")
		}
		t.Skip("needs /usr/bin/python3")
	}
}

func TestResizeReachesTheProgram(t *testing.T) {
	e := setup(t)
	s := e.terminal("sh", "-c", `trap 'echo got-WINCH' WINCH; echo ready; sleep 2; echo done`)
	s.must("ready")
	unix.IoctlSetWinsize(int(s.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120})
	s.must("got-WINCH")
}

// On a terminal box says what it starts and how, before the program's own
// output, and without holding a plain run up. Off a terminal it stays quiet.
func TestLaunchLineComesFirst(t *testing.T) {
	e := setup(t)
	s := e.terminal("sh", "-c", "echo program-ran")
	s.must("program-ran")
	out := s.output()
	line := "sh · profile default · network off · project read-write"
	if i := strings.Index(out, line); i < 0 || i > strings.Index(out, "program-ran") {
		t.Errorf("no launch line before the program's output:\n%s", out)
	}
	if strings.Contains(out, "launching") {
		t.Errorf("a run without the editor must not pause:\n%s", out)
	}
	if _, stderr, _ := e.run("sh", "-c", "true"); strings.Contains(stderr, "profile default") {
		t.Errorf("off a terminal box prints no launch line:\n%s", stderr)
	}
	s = e.terminal("--no-tui", "sh", "-c", "echo program-ran")
	s.must("program-ran")
	if strings.Contains(s.output(), "profile default") {
		t.Errorf("--no-tui asks for quiet:\n%s", s.output())
	}
}

func TestTerminalInjectionIsBlocked(t *testing.T) {
	needPython(t)
	e := setup(t)
	e.profile(profile.Default("python3"))
	s := e.terminal("python3", "-c", `import fcntl, termios
try:
    fcntl.ioctl(0, termios.TIOCSTI, b"x")
    print("injected")
except OSError as err:
    print("blocked", err.errno)`)
	s.must("blocked")
	if strings.Contains(s.output(), "injected") {
		t.Fatal("TIOCSTI worked inside the box")
	}
}

func TestDevTTYAndJobControl(t *testing.T) {
	e := setup(t)
	s := e.terminal("sh", "-c", `exec 3</dev/tty && echo tty-ok; sh -ic 'sleep 0.1 & wait; echo jobs-ok' 2>&1`)
	s.must("tty-ok")
	s.must("jobs-ok")
	if strings.Contains(s.output(), "no job control") {
		t.Errorf("job control is off:\n%s", s.output())
	}
}

// TestCtrlC codifies plan decision B3. Without the init, Ctrl+C in a
// cooked-mode program reaches bwrap's outer process too and the whole box
// dies; with sandbox.init the program alone gets it.
func TestCtrlC(t *testing.T) {
	script := `trap 'echo got-INT' INT; echo ready; sleep 3; echo survived`
	t.Run("init", func(t *testing.T) {
		e := setup(t)
		p := profile.Default("sh")
		p.Sandbox.Init = true
		e.profile(p)
		s := e.terminal("sh", "-c", script)
		s.must("ready")
		s.master.Write([]byte{3})
		s.must("got-INT")
		s.must("survived")
	})
	t.Run("plain", func(t *testing.T) {
		e := setup(t)
		s := e.terminal("sh", "-c", script)
		s.must("ready")
		s.master.Write([]byte{3})
		s.exited(10 * time.Second)
		if strings.Contains(s.output(), "survived") {
			t.Log("B3: without init the program survived Ctrl+C; init may not be needed here")
		} else {
			t.Logf("B3: without init Ctrl+C ended the whole box (exit %d); keep sandbox.init for such programs", s.code)
		}
	})
}

func TestStdinBlockingAfterStart(t *testing.T) {
	needPython(t)
	e := setup(t)
	e.profile(profile.Default("python3"))
	s := e.terminal("python3", "-c", `import fcntl, os; print("nonblock" if fcntl.fcntl(0, fcntl.F_GETFL) & os.O_NONBLOCK else "blocking")`)
	s.must("blocking")
}

// TestLandlock codifies plan decision S3: a scope-only Landlock ruleset
// must not stop bwrap from mounting, and must block abstract sockets.
func TestLandlock(t *testing.T) {
	if sandbox.LandlockABI() < 6 {
		t.Skip("needs Landlock ABI 6 (Linux 6.12+)")
	}
	needPython(t)
	name := fmt.Sprintf("box-e2e-%d", rand.Int())
	l, err := net.Listen("unix", "@"+name)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	connect := fmt.Sprintf(`import socket
s = socket.socket(socket.AF_UNIX)
try:
    s.connect("\0%s"); print("connected")
except OSError as err:
    print("refused", err.errno)`, name)
	for _, landlock := range []bool{false, true} {
		e := setup(t)
		p := profile.Default("python3")
		p.Network = profile.NetOn
		p.Sandbox.Landlock = landlock
		e.profile(p)
		out, stderr, code := e.run("--no-tui", "python3", "-c", connect)
		if code != 0 {
			t.Fatalf("landlock=%v: exit %d (does bwrap still mount?)\n%s", landlock, code, stderr)
		}
		if want := map[bool]string{false: "connected", true: "refused"}[landlock]; !strings.Contains(out, want) {
			t.Errorf("landlock=%v: got %q, want %s", landlock, out, want)
		}
	}
}

// TestVsock codifies plan item B9: on WSL, AF_VSOCK leads to the Windows host.
func TestVsock(t *testing.T) {
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err != nil {
		t.Skip("not WSL")
	}
	needPython(t)
	e := setup(t)
	out := e.sh(`python3 -c 'import socket
try:
    socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM); print("open")
except OSError as err:
    print("blocked", err.errno)'`)
	if !strings.Contains(out, "blocked 97") {
		t.Errorf("AF_VSOCK wasn't blocked: %q", out)
	}
}
