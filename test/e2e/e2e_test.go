//go:build linux

// Package e2e runs the real box binary with real bubblewrap. It needs Linux
// with working user namespaces (Ubuntu on WSL2 is the target) and skips
// itself when `box --doctor` says sandboxes can't run.
package e2e

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asx8678/box/internal/profile"
)

var boxBin string

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		fmt.Println("skipping the end-to-end tests: -short")
		return
	}
	dir, err := os.MkdirTemp("", "box-e2e-bin")
	if err != nil {
		panic(err)
	}
	boxBin = filepath.Join(dir, "box")
	out, err := exec.Command("go", "build", "-o", boxBin, "../..").CombinedOutput()
	if err != nil {
		panic("build: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t    *testing.T
	root string // canonical temp root
	home string // the fake $HOME
	proj string // $HOME/code/proj, where box runs
	dirs profile.Dirs
}

func setup(t *testing.T) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, root: root, home: filepath.Join(root, "home")}
	e.proj = filepath.Join(e.home, "code", "proj")
	for _, d := range []string{e.proj, e.home + "/code/sibling", e.home + "/.ssh", e.home + "/.config/box"} {
		os.MkdirAll(d, 0o700)
	}
	os.WriteFile(e.home+"/.ssh/id_ed25519", []byte("secret"), 0o600)
	e.dirs = profile.Dirs{Home: e.home, Config: e.home + "/.config/box",
		Data: e.home + "/.local/share/box", State: e.home + "/.local/state/box"}
	if _, stderr, code := e.run("--doctor"); code != 0 {
		// CI sets BOX_E2E_REQUIRED so a broken setup fails instead of
		// quietly skipping every test.
		if os.Getenv("BOX_E2E_REQUIRED") != "" {
			t.Fatalf("bubblewrap can't run sandboxes here:\n%s", stderr)
		}
		t.Skipf("bubblewrap can't run sandboxes here:\n%s", stderr)
	}
	e.profile(profile.Default("sh"))
	return e
}

func (e *env) profile(p profile.Profile) {
	e.t.Helper()
	if err := profile.Save(profile.Path(e.dirs, p.Program, "default"), p); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) runIn(dir string, args ...string) (string, string, int) {
	cmd := exec.Command(boxBin, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + e.home, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb",
		"LANG=C.UTF-8", "SECRET_TOKEN=do-not-leak"}
	if v, ok := os.LookupEnv("WSL_INTEROP"); ok {
		cmd.Env = append(cmd.Env, "WSL_INTEROP="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func (e *env) run(args ...string) (string, string, int) {
	return e.runIn(e.proj, args...)
}

// sh runs a shell script inside the box and fails the test unless it exits 0.
func (e *env) sh(script string, args ...string) string {
	e.t.Helper()
	out, stderr, code := e.run(append([]string{"--no-tui", "sh", "-c", script, "sh"}, args...)...)
	if code != 0 {
		e.t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	return out
}

func TestSeesOnlyTheProject(t *testing.T) {
	e := setup(t)
	e.sh(`set -e
echo ok > "$PWD/written"
echo ok > "$HOME/in-private-home"
test ! -e "$HOME/.ssh"
test ! -e "$HOME/.config/box"
test ! -e "$1"
if touch /usr/box-test 2>/dev/null; then exit 1; fi
grep -q program=sh /run/box/profile`, e.home+"/code/sibling")
	if _, err := os.Stat(e.proj + "/written"); err != nil {
		t.Error("the project write didn't reach the host")
	}
	if _, err := os.Stat(e.dirs.Data + "/homes/sh/default/in-private-home"); err != nil {
		t.Error("the private home write didn't land in the profile's home")
	}
	if _, err := os.Stat(e.home + "/in-private-home"); err == nil {
		t.Error("the private home write reached the real home")
	}
}

func TestEnvironmentIsClean(t *testing.T) {
	e := setup(t)
	out := e.sh(`env`)
	for _, leak := range []string{"SECRET_TOKEN", "WSL_INTEROP"} {
		if strings.Contains(out, leak) {
			t.Errorf("%s leaked into the box:\n%s", leak, out)
		}
	}
	for _, want := range []string{"HOME=" + e.home, "BOX_PROFILE=sh/default", "TMPDIR=/tmp", "TERM=dumb"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
}

func TestNetworkSwitch(t *testing.T) {
	e := setup(t)
	off, _, code := e.run("--no-net", "sh", "-c", "tail -n +3 /proc/net/dev")
	if code != 0 || strings.Count(off, ":") != 1 || !strings.Contains(off, "lo:") {
		t.Errorf("--no-net should leave only loopback, got:\n%s", off)
	}
	host, _ := os.ReadFile("/proc/net/dev")
	on, _, _ := e.run("--net", "sh", "-c", "tail -n +3 /proc/net/dev")
	if strings.Count(on, ":") != strings.Count(string(host), ":") {
		t.Errorf("--net should share the host's interfaces, got:\n%s", on)
	}
}

// A restricted profile must never run with the whole network in place of
// the allowlist box can't enforce yet; --net and --no-net still work.
func TestRestrictedNetworkDoesNotRun(t *testing.T) {
	e := setup(t)
	p := profile.Default("sh")
	p.Network = profile.NetRestricted
	p.Allow = profile.Allow{Hosts: []string{"example.com"}}
	e.profile(p)
	out, stderr, code := e.run("--no-tui", "sh", "-c", "echo ran")
	if code != 125 || strings.Contains(out, "ran") || !strings.Contains(stderr, "can't enforce") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	off, _, code := e.run("--no-tui", "--no-net", "sh", "-c", "tail -n +3 /proc/net/dev")
	if code != 0 || strings.Count(off, ":") != 1 {
		t.Errorf("--no-net on a restricted profile: exit %d\n%s", code, off)
	}
}

func TestArgumentsArriveIntact(t *testing.T) {
	e := setup(t)
	out := e.sh(`printf '%s|' "$@"`, "a b", "c'd", "", "$HOME")
	if out != "a b|c'd||$HOME|" {
		t.Errorf("got %q", out)
	}
}

func TestExitCodes(t *testing.T) {
	e := setup(t)
	for _, c := range []struct {
		dir  string
		args []string
		want int
	}{
		{e.proj, []string{"sh", "-c", "exit 7"}, 7},
		{e.proj, []string{"sh", "-c", "kill -TERM $$"}, 128 + 15},
		{e.proj, []string{"no-such-program"}, 127},
		{e.proj, []string{"./not-executable"}, 126},
		{e.home, []string{"sh", "-c", "true"}, 125},
		{"/", []string{"sh", "-c", "true"}, 125},
	} {
		os.WriteFile(e.proj+"/not-executable", nil, 0o644)
		if _, stderr, code := e.runIn(c.dir, c.args...); code != c.want {
			t.Errorf("%v in %s: exit %d, want %d\n%s", c.args, c.dir, code, c.want, stderr)
		}
	}
}

func TestGitConfigAndHooksStayReadOnly(t *testing.T) {
	e := setup(t)
	os.MkdirAll(e.proj+"/.git/hooks", 0o755)
	os.WriteFile(e.proj+"/.git/config", []byte("[core]\n"), 0o644)
	e.sh(`set -e
if echo '[core] fsmonitor = evil' >> .git/config 2>/dev/null; then exit 1; fi
if touch .git/hooks/pre-commit 2>/dev/null; then exit 1; fi
touch .git/index-like-file`)
}

func TestReadOnlyFolderInsideProject(t *testing.T) {
	e := setup(t)
	os.MkdirAll(e.proj+"/secrets", 0o755)
	p := profile.Default("sh")
	p.Extra.RO = []string{e.proj + "/secrets"}
	e.profile(p)
	e.sh(`if touch secrets/x 2>/dev/null; then exit 1; fi; touch other`)
}

func TestRefusesPlantedSymlinkInPrivateHome(t *testing.T) {
	e := setup(t)
	outside := filepath.Join(e.root, "outside")
	os.MkdirAll(outside, 0o755)
	privHome := e.dirs.Data + "/homes/sh/default"
	os.MkdirAll(privHome, 0o700)
	// A program planted this on an earlier run; the project path runs through it.
	os.Symlink(outside, privHome+"/code")
	_, stderr, code := e.run("sh", "-c", "true")
	if code != 125 || !strings.Contains(stderr, "is a symlink") {
		t.Errorf("exit %d, want 125 with a symlink refusal:\n%s", code, stderr)
	}
	if entries, _ := os.ReadDir(outside); len(entries) > 0 {
		t.Errorf("created through the symlink: %v", entries)
	}
}

func TestRefusesSymlinkToSSH(t *testing.T) {
	e := setup(t)
	os.Symlink(e.home+"/.ssh", e.home+"/code/keys")
	p := profile.Default("sh")
	p.Extra.RW = []string{"~/code/keys"}
	e.profile(p)
	if _, stderr, code := e.run("sh", "-c", "true"); code != 125 || !strings.Contains(stderr, ".ssh") {
		t.Errorf("exit %d, want 125:\n%s", code, stderr)
	}
}

func TestStrictBlocksNestedSandboxes(t *testing.T) {
	e := setup(t)
	nested := "bwrap --ro-bind / / true"
	if _, _, code := e.run("sh", "-c", nested); code != 0 {
		t.Log("nested sandboxes don't work on this machine even without strict; checking strict only")
	}
	p := profile.Default("sh")
	p.Sandbox.Strict = true
	e.profile(p)
	if _, _, code := e.run("sh", "-c", nested); code == 0 {
		t.Error("strict should stop the program from creating user namespaces")
	}
}

func TestWSLInteropIsUnreachable(t *testing.T) {
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err != nil {
		t.Skip("not WSL")
	}
	e := setup(t)
	e.sh(`set -e; test -z "${WSL_INTEROP:-}"; test ! -e /run/WSL; test ! -e /mnt/c`)
	if _, err := os.Stat("/mnt/c/Windows/System32/cmd.exe"); err == nil {
		if _, _, code := e.run("/mnt/c/Windows/System32/cmd.exe"); code != 126 {
			t.Errorf("box cmd.exe: exit %d, want 126", code)
		}
		// A Windows program copied into the project must not start either.
		data, _ := os.ReadFile("/mnt/c/Windows/System32/whoami.exe")
		os.WriteFile(e.proj+"/w.exe", data, 0o755)
		if _, _, code := e.run("sh", "-c", "./w.exe"); code == 0 {
			t.Error("a Windows program started from inside the box")
		}
	}
}

// A variable the profile sets reaches the program, never bwrap itself:
// LD_PRELOAD in bwrap would run code before any sandbox exists.
func TestProfileEnvDoesNotReachBwrap(t *testing.T) {
	e := setup(t)
	lib := e.root + "/outside.so" // exists outside only; not an ELF file
	os.WriteFile(lib, []byte("not a library"), 0o644)
	p := profile.Default("sh")
	p.Env.Set = map[string]string{"LD_PRELOAD": lib}
	e.profile(p)
	out, stderr, code := e.run("--no-tui", "sh", "-c", `echo "$LD_PRELOAD"`)
	if code != 0 || !strings.Contains(out, lib) {
		t.Fatalf("exit %d, the program should see LD_PRELOAD\nstdout: %s\nstderr: %s", code, out, stderr)
	}
	// Inside the box the file doesn't exist; only bwrap could have read it.
	for _, l := range strings.Split(stderr, "\n") {
		if strings.Contains(l, "outside.so") && !strings.Contains(l, "cannot open") {
			t.Errorf("LD_PRELOAD reached bwrap: %s", l)
		}
	}
}

// Descriptors box inherited don't pass into the sandbox.
func TestInheritedFilesDontLeak(t *testing.T) {
	e := setup(t)
	marker, err := os.Create(e.root + "/inherited-marker")
	if err != nil {
		t.Fatal(err)
	}
	defer marker.Close()
	cmd := exec.Command(boxBin, "--no-tui", "sh", "-c", `for f in /proc/self/fd/*; do readlink "$f"; done`)
	cmd.Dir = e.proj
	cmd.Env = []string{"HOME=" + e.home, "PATH=/usr/local/bin:/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{marker} // fd 3 in box
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(string(out), "inherited-marker") {
		t.Errorf("an inherited file is open inside the box:\n%s", out)
	}
}

// --box-init runs a program only inside a box, never on the host.
func TestBoxInitOnlyInsideABox(t *testing.T) {
	out, err := exec.Command(boxBin, "--box-init", "--", "/bin/echo", "ran").CombinedOutput()
	if err == nil || strings.Contains(string(out), "ran\n") {
		t.Errorf("box --box-init ran a program outside a box:\n%s", out)
	}
}
