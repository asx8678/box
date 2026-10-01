package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asx8678/box/internal/profile"
)

func tempDirs(t *testing.T) profile.Dirs {
	root := t.TempDir()
	return profile.Dirs{Home: root, Config: filepath.Join(root, "config/box"), Data: filepath.Join(root, "data/box")}
}

func TestSummary(t *testing.T) {
	p := profile.Default("kiro-cli")
	p.Network = profile.NetOn
	p.Home.RW = []string{"~/.local/share/kiro-cli"}
	p.Home.RO = []string{"~/.kiro"}
	p.Tools = []string{"node"}
	p.Env.Pass = []string{"KIRO_API_KEY"}
	got := summary(p, 2)
	for _, want := range []string{"net on", "project rw", "rw: ~/.local/share/kiro-cli", "ro: ~/.kiro", "tools: node", "env: KIRO_API_KEY", "used in 2 folders"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
}

func TestLaunchSummary(t *testing.T) {
	p := profile.Default("kiro-cli")
	if got := launchSummary("kiro-cli", "default", p, profile.NetOn); got != "kiro-cli · profile default · network on · project read-write" {
		t.Errorf("%q", got)
	}
	p.Workdir.Mode = "ro"
	if got := launchSummary("sh", "offline", p, profile.NetOff); got != "sh · profile offline · network off · project read-only" {
		t.Errorf("%q", got)
	}
}

func TestSummaryRestricted(t *testing.T) {
	p := profile.Default("kiro-cli")
	p.Network = profile.NetRestricted
	if got := summary(p, 0); !strings.HasPrefix(got, "net restricted · ") {
		t.Errorf("summary %q", got)
	}
	p.Allow = profile.Allow{Groups: []string{"docs-microsoft", "aws"}, Hosts: []string{"wiki.example.com"}}
	if got := summary(p, 0); !strings.Contains(got, "net restricted (docs-microsoft, aws, 1 custom)") {
		t.Errorf("summary %q", got)
	}
}

func TestListAndReset(t *testing.T) {
	d := tempDirs(t)
	var out, errb bytes.Buffer
	if code := list(d, &out, &errb); code != 0 || !strings.Contains(out.String(), "no profiles yet") {
		t.Errorf("empty list: %d %q", code, out.String())
	}
	for _, name := range []string{"default", "online"} {
		if err := profile.Save(profile.Path(d, "kiro-cli", name), profile.Default("kiro-cli")); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := profile.LoadFolders(profile.FoldersPath(d))
	f.Set("/code/a", "kiro-cli", "online")
	f.Set("/code/a", "bash", "default")
	f.Save()
	home := profile.HomeDir(d, "kiro-cli", "default")
	os.MkdirAll(home, 0o700)

	out.Reset()
	if code := list(d, &out, &errb); code != 0 {
		t.Fatalf("list: exit %d %s", code, errb.String())
	}
	for _, want := range []string{"kiro-cli", "default", "online", "used in 1 folder"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := reset(d, "kiro-cli", true, &out, &errb); code != 0 {
		t.Fatalf("reset: exit %d %s", code, errb.String())
	}
	if names, _ := profile.List(d, "kiro-cli"); len(names) != 0 {
		t.Errorf("profiles left: %v", names)
	}
	f, _ = profile.LoadFolders(profile.FoldersPath(d))
	if _, ok := f.Get("/code/a", "kiro-cli"); ok {
		t.Error("folder memory not forgotten")
	}
	if _, ok := f.Get("/code/a", "bash"); !ok {
		t.Error("another program's memory was forgotten")
	}
	if _, err := os.Stat(home); err != nil {
		t.Error("-y must keep the private home")
	}
	if !strings.Contains(out.String(), "kept private home") {
		t.Errorf("reset didn't say it kept the home:\n%s", out.String())
	}
	if code := reset(d, ".hidden", true, &out, &errb); code != ExitBox {
		t.Errorf("a bad name should be refused, exit %d", code)
	}
}

func TestResetNeedsConfirmationWithoutTerminal(t *testing.T) {
	d := tempDirs(t)
	profile.Save(profile.Path(d, "bash", "default"), profile.Default("bash"))
	var out, errb bytes.Buffer
	if code := reset(d, "bash", false, &out, &errb); code != ExitBox || !strings.Contains(errb.String(), "-y") {
		t.Errorf("exit %d: %s", code, errb.String())
	}
	if names, _ := profile.List(d, "bash"); len(names) != 1 {
		t.Error("nothing may be deleted without confirmation")
	}
}

func TestRemoveAllHandlesReadOnlyFolders(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "home/go/pkg/mod/example.com/x@v1")
	os.MkdirAll(mod, 0o755)
	os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module x"), 0o444)
	os.Chmod(mod, 0o555) // as Go leaves its module cache
	if err := removeAll(filepath.Join(root, "home")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "home")); !os.IsNotExist(err) {
		t.Errorf("still there: %v", err)
	}
}

func TestListWithEmptyProgramFolder(t *testing.T) {
	d := tempDirs(t)
	os.MkdirAll(filepath.Join(d.Config, "profiles", "ghost"), 0o700)
	var out, errb bytes.Buffer
	if code := list(d, &out, &errb); code != 0 || !strings.Contains(out.String(), "no profiles yet") {
		t.Errorf("exit %d: %q", code, out.String())
	}
}
