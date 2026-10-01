package profile

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asx8678/box/internal/host"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles", "kiro-cli", "default.toml")
	p := Default("kiro-cli")
	p.Network = NetOn
	p.Home.RW = []string{"~/.local/share/kiro-cli"}
	p.Extra.RO = []string{"/opt/lib"}
	p.Env.Pass = []string{"AWS_PROFILE"}
	p.Env.Set["EDITOR"] = "vi"
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("profile mode %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v, want 0700", di.Mode().Perm())
	}
	got, err := Load(path, "kiro-cli")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("round trip changed the profile:\n got %+v\nwant %+v", got, p)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".tmp-*")); len(left) > 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func TestLoadRefusesWritableByOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.toml")
	if err := os.WriteFile(path, []byte("version = 1\nprogram = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "x"); err == nil || !strings.Contains(err.Error(), "writable by group") {
		t.Fatalf("got %v", err)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("version = 1\nnetwrk = true\n"), "x")
	if err == nil || !strings.Contains(err.Error(), "netwrk") {
		t.Fatalf("got %v", err)
	}
}

func TestParseKeepsDefaults(t *testing.T) {
	p, err := Parse([]byte("version = 1\n"), "x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Workdir.Mode != "rw" || p.Workdir.ProtectGit != GitFull || p.Network != NetOff {
		t.Errorf("defaults lost: %+v", p)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Profile)
		want   string
	}{
		{"version", func(p *Profile) { p.Version = 2 }, "version 2"},
		{"program mismatch", func(p *Profile) { p.Program = "other" }, "doesn't match"},
		{"bad mode", func(p *Profile) { p.Workdir.Mode = "rwx" }, "workdir.mode"},
		{"relative path", func(p *Profile) { p.Extra.RO = []string{"code/lib"} }, "must start with"},
		{"listed twice", func(p *Profile) { p.Home.RW = []string{"~/.x"}; p.Extra.RO = []string{"~/.x"} }, "already listed"},
		{"bad variable", func(p *Profile) { p.Env.Pass = []string{"A-B"} }, "not a variable name"},
		{"interop", func(p *Profile) { p.Env.Pass = []string{"WSL_INTEROP"} }, "way out"},
		{"ssh agent", func(p *Profile) { p.Env.Set = map[string]string{"SSH_AUTH_SOCK": "/x"} }, "way out"},
		{"reserved", func(p *Profile) { p.Env.Set = map[string]string{"HOME": "/x"} }, "set by box"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Default("x")
			tt.change(&p)
			if err := p.Validate("x"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
	if err := Default("kiro-cli").Validate("kiro-cli"); err != nil {
		t.Errorf("default profile invalid: %v", err)
	}
}

func TestValidName(t *testing.T) {
	for _, s := range []string{"kiro-cli", "my_agent", "a.b", "x1", "g++"} {
		if !ValidName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", ".hidden", "-x", "+x", "a/b", "..", "a b", strings.Repeat("a", 65)} {
		if ValidName(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestPresetsParse(t *testing.T) {
	entries, err := fs.ReadDir(presets, "presets")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".toml")
		if _, err := Preset(name); err != nil {
			t.Errorf("preset %s: %v", name, err)
		}
	}
	p, err := Preset("unknown-tool")
	if err != nil || !reflect.DeepEqual(p, Default("unknown-tool")) {
		t.Errorf("unknown program should get the generic default, got %+v, %v", p, err)
	}
}

func TestExpand(t *testing.T) {
	for in, want := range map[string]string{
		"~":        "/home/u",
		"~/.kiro":  "/home/u/.kiro",
		"/opt//x/": "/opt/x",
	} {
		if got, err := Expand(in, "/home/u"); err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Expand("~other/x", "/home/u"); err == nil {
		t.Error("~other should be refused")
	}
}

func TestCanonical(t *testing.T) {
	f := host.NewFake()
	f.Dir("/var/home/u").Symlink("/home", "var/home").Symlink("/var/home/u/dangling", "/nowhere")
	for in, want := range map[string]string{
		"/home/u":           "/var/home/u",
		"/home/u/new/child": "/var/home/u/new/child",
	} {
		if got, err := Canonical(f, in); err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Canonical(f, "/home/u/dangling/x"); err == nil {
		t.Error("a path through a broken symlink should be refused")
	}
}

func TestDirsFollowXDG(t *testing.T) {
	f := host.NewFake()
	f.Dir("/home/u").Dir("/xdg/config")
	f.Env["HOME"] = "/home/u"
	f.Env["XDG_CONFIG_HOME"] = "/xdg/config"
	d, err := DirsFor(f)
	if err != nil {
		t.Fatal(err)
	}
	want := Dirs{Home: "/home/u", Config: "/xdg/config/box", Data: "/home/u/.local/share/box", State: "/home/u/.local/state/box"}
	if d != want {
		t.Errorf("got %+v, want %+v", d, want)
	}
	f.Env["XDG_DATA_HOME"] = "relative"
	if _, err := DirsFor(f); err == nil {
		t.Error("a relative XDG_DATA_HOME should be refused")
	}
}

func TestWindowsPaths(t *testing.T) {
	for p, want := range map[string]bool{
		"/mnt/c":                  true,
		"/mnt/c/Users":            true,
		"/mnt/c/users/win":        true,
		"/mnt/c/Users/win/code":   false,
		"/mnt/wsl":                false,
		"/mnt/cd/Users/win":       false,
		"/home/u/mnt/c/Users/win": false,
	} {
		if got := windowsUserPath(p); got != want {
			t.Errorf("windowsUserPath(%q) = %v, want %v", p, got, want)
		}
	}
	if !windowsAppData("/mnt/d/Users/win/AppData/Roaming/x") || windowsAppData("/mnt/d/Users/win/code") {
		t.Error("windowsAppData misjudged a path")
	}
}

func TestFoldersRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box", "folders.toml")
	f, err := LoadFolders(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Set("/home/u/code/a b", "kiro-cli", "online")
	f.Set("/home/u/code/c", "bash", "default")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	g, err := LoadFolders(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := g.Get("/home/u/code/a b", "kiro-cli"); !ok || name != "online" {
		t.Errorf("got %q, %v", name, ok)
	}
	g.Forget("bash")
	if _, ok := g.Get("/home/u/code/c", "bash"); ok || len(g.Entries) != 1 {
		t.Errorf("Forget left %v", g.Entries)
	}
}

func TestList(t *testing.T) {
	d := Dirs{Config: t.TempDir()}
	for _, name := range []string{"online", "default"} {
		if err := Save(Path(d, "kiro-cli", name), Default("kiro-cli")); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(d.Config, "profiles", "kiro-cli", "notes.txt"), nil, 0o600)
	names, err := List(d, "kiro-cli")
	if err != nil || !reflect.DeepEqual(names, []string{"default", "online"}) {
		t.Errorf("got %v, %v", names, err)
	}
	if names, err := List(d, "none"); err != nil || names != nil {
		t.Errorf("missing program: %v, %v", names, err)
	}
}

func TestDamagedFoldersFileIsSetAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "folders.toml")
	os.WriteFile(path, []byte("[folders\nnot toml"), 0o600)
	f, err := LoadFolders(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Damaged == "" || len(f.Entries) != 0 {
		t.Errorf("damaged %q, entries %v", f.Damaged, f.Entries)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Errorf("not moved aside: %v", err)
	}
	f.Set("/p", "sh", "default")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	// Writable by others is still refused, not set aside.
	os.Chmod(path, 0o666)
	if _, err := LoadFolders(path); err == nil {
		t.Error("a folders file writable by others was accepted")
	}
}
