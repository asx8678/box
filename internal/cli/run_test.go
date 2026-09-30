package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	o, rest, err := parse([]string{"-p", "online", "--no-net", "kiro-cli", "-p", "x", "--net"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.profile != "online" || !o.noNet || o.net {
		t.Errorf("options %+v", o)
	}
	if strings.Join(rest, " ") != "kiro-cli -p x --net" {
		t.Errorf("program args changed: %q", rest)
	}
	for _, bad := range [][]string{
		{"--net", "--no-net", "x"},
		{"-p", "a", "-n", "b", "x"},
		{"--bogus", "x"},
		{"-e", "x"},
		{"-y", "x"},
		{"-r", "-p", "a", "x"},
	} {
		if _, _, err := parse(bad, io.Discard); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"--bogus"}, &out, &errb); code != ExitBox {
		t.Errorf("bad flag: exit %d", code)
	}
	if code := Run([]string{"--version"}, &out, &errb); code != 0 {
		t.Errorf("--version: exit %d", code)
	}
	if code := Run(nil, &out, &errb); code != ExitBox {
		t.Errorf("no program: exit %d", code)
	}
}

func TestOlder(t *testing.T) {
	for _, c := range []struct {
		v, than string
		want    bool
	}{{"0.9.0", "0.12.0", true}, {"0.12.0", "0.12.0", false}, {"0.13.1", "0.12.0", false}, {"0.11", "0.12.0", true}} {
		if got := older(c.v, c.than); got != c.want {
			t.Errorf("older(%s, %s) = %v", c.v, c.than, got)
		}
	}
}

// TestDryRunEndToEnd builds box and runs it for real in a throwaway home,
// on any OS: --dry-run doesn't need bubblewrap.
func TestDryRunEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds box")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(root, "bin", "box")
	build := exec.Command("go", "build", "-o", bin, "../..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(root, "home")
	proj := filepath.Join(home, "code", "proj")
	os.MkdirAll(proj, 0o755)
	boxRun := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = proj
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=dumb", "SECRET=x"}
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}

	out, code := boxRun("--dry-run", "sh", "-c", "echo hi")
	if code != ExitBox || !strings.Contains(out, "no profile yet") {
		t.Fatalf("without a profile: exit %d\n%s", code, out)
	}
	out, code = boxRun("--dry-run", "-n", "default", "--no-tui", "sh", "-c", "echo 'hi there'")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"env -i", "--unshare-all", "--bind " + proj + " " + proj, "'echo '\\''hi there'\\'''"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("host variable leaked into the dry run")
	}
	if _, err := os.Stat(filepath.Join(home, ".config/box/profiles/sh/default.toml")); err == nil {
		t.Error("--dry-run saved a profile")
	}
	if _, code = boxRun("--dry-run", "no-such-program"); code != 127 {
		t.Errorf("missing program: exit %d, want 127", code)
	}
	if runtime.GOOS != "linux" {
		if out, code = boxRun("-n", "default", "--no-tui", "sh"); code != ExitBox || !strings.Contains(out, "needs Linux") {
			t.Errorf("running off Linux must fail closed: exit %d\n%s", code, out)
		}
	}
}
