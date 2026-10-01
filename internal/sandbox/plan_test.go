package sandbox

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// machine is a fake Ubuntu on WSL2: merged /usr, resolv.conf pointing into
// /mnt/wsl, a Docker socket, Windows drives and a user with a project.
func machine() *host.Fake {
	f := host.NewFake()
	f.Dir("/usr/bin").Dir("/usr/sbin").Dir("/usr/lib").Dir("/usr/lib64")
	f.Symlink("/bin", "usr/bin").Symlink("/sbin", "usr/sbin")
	f.Symlink("/lib", "usr/lib").Symlink("/lib64", "usr/lib64")
	f.File("/etc/passwd", "", 0o644)
	f.Symlink("/etc/resolv.conf", "/mnt/wsl/resolv.conf").File("/mnt/wsl/resolv.conf", "", 0o644)
	f.Dir("/run/user/1000").Symlink("/var/run", "/run").Socket("/run/docker.sock")
	f.Dir("/home/u/.ssh").Dir("/home/u/.kiro").Dir("/home/u/.local/share/kiro-cli")
	f.File("/home/u/.local/bin/kiro-cli", "", 0o755).File("/home/u/.local/bin/box", "", 0o755)
	f.File("/home/u/.bashrc", "", 0o644).Dir("/home/u/.config/box")
	f.Dir("/home/u/code/proj/.git/hooks").File("/home/u/code/proj/.git/config", "", 0o644)
	f.Dir("/home/u/code/proj/secrets").Dir("/home/u/code/shared-lib")
	f.Symlink("/home/u/code/link", "/home/u/.ssh")
	f.Socket("/home/u/code/agent.sock")
	f.Dir("/mnt/c/Users/win/AppData/Roaming").Dir("/mnt/c/Windows/System32")
	f.Env = map[string]string{
		"HOME":            "/home/u",
		"PATH":            "/home/u/.local/bin:/usr/local/bin:/usr/bin:/bin:/mnt/c/Windows/System32",
		"TERM":            "xterm-256color",
		"LANG":            "C.UTF-8",
		"LC_TIME":         "en_GB.UTF-8",
		"XDG_RUNTIME_DIR": "/run/user/1000",
		"WSL_INTEROP":     "/run/WSL/1_interop",
		"SSH_AUTH_SOCK":   "/run/user/1000/ssh.sock",
		"SECRET_TOKEN":    "do-not-leak",
		"KIRO_API_KEY":    "k-123",
	}
	f.Cwd = "/home/u/code/proj"
	return f
}

func input(t *testing.T, f *host.Fake, p profile.Profile) Input {
	t.Helper()
	dirs, err := profile.DirsFor(f)
	if err != nil {
		t.Fatal(err)
	}
	prot, err := profile.NewProtected(f, dirs, "/home/u/.local/bin/box", true)
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Profile:       p,
		ProfileName:   "default",
		Dirs:          dirs,
		Protected:     prot,
		Workdir:       f.Cwd,
		Program:       "/home/u/.local/bin/kiro-cli",
		ProgramDirs:   []string{"/home/u/.local/bin"},
		Args:          []string{"chat", "it's a test"},
		LegacyTIOCSTI: "0",
		Arch:          "amd64",
		Self:          "/home/u/.local/bin/box",
		EgressSocket:  "/run/user/1000/box/egress-1.sock",
	}
}

func preset(t *testing.T, program string) profile.Profile {
	t.Helper()
	p, err := profile.Preset(program)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"generic", "kiro-cli", "kirocrew", "bash", "restricted"} {
		t.Run(name, func(t *testing.T) {
			f := machine()
			f.Dir("/home/u/workplace")
			program := name
			switch name {
			case "generic":
				program = "mytool"
			case "restricted":
				program = "kiro-cli"
			}
			p := preset(t, program)
			if name == "restricted" {
				p.Network = profile.NetRestricted
				p.Allow = profile.Allow{Groups: []string{"docs-microsoft"}, Hosts: []string{"wiki.example.com"}}
			}
			plan, err := Build(f, input(t, f, p))
			if err != nil {
				t.Fatal(err)
			}
			got := plan.DryRun("/usr/bin/bwrap")
			path := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("dry run differs from %s (run go test ./internal/sandbox -update)\n%s", path, got)
			}
		})
	}
}

// A restricted network is never the whole network: the plan shares
// nothing, lists what the proxy will allow, and can't run until box can
// enforce it.
func TestRestrictedNetwork(t *testing.T) {
	f := machine()
	p := preset(t, "kiro-cli")
	p.Network = profile.NetRestricted
	p.Allow = profile.Allow{Groups: []string{"docs-microsoft"}}
	in := input(t, f, p)
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Args(), "--share-net") {
		t.Error("a restricted profile must not share the host's network")
	}
	if plan.Network != profile.NetRestricted {
		t.Errorf("network %q", plan.Network)
	}
	// The proxy socket is the only way out; the init runs the bridge to it,
	// and the program's proxy variables point at the bridge.
	if i := indexOf(t, plan, ROBind, EgressPath); plan.Mounts[i].Src != in.EgressSocket {
		t.Errorf("socket mounted from %s", plan.Mounts[i].Src)
	}
	if !slices.Equal(plan.Command[:3], []string{InitPath, "--box-init", "--"}) {
		t.Errorf("not under box's init: %v", plan.Command)
	}
	for _, kv := range []string{"HTTPS_PROXY=http://127.0.0.1:3128", "ALL_PROXY=socks5h://127.0.0.1:1080", "NODE_USE_ENV_PROXY=1"} {
		if !slices.Contains(plan.Env, kv) {
			t.Errorf("env lacks %s", kv)
		}
	}
	in.EgressSocket = ""
	if _, err := Build(f, in); err == nil {
		t.Error("a restricted plan built without the proxy socket")
	}
	in.EgressSocket = "/run/user/1000/box/egress-1.sock"
	dry := plan.DryRun("bwrap")
	for _, want := range []string{"# Network: restricted", "runtime.us-east-1.kiro.dev", "learn.microsoft.com", "network=restricted"} {
		if !strings.Contains(dry, want) {
			t.Errorf("dry run lacks %q:\n%s", want, dry)
		}
	}
	if strings.Contains(dry, "kubernetes.io") || strings.Contains(dry, "dev.azure.com") {
		t.Error("an unticked group leaked into the allowlist")
	}

	// --net and --no-net replace the profile's mode for one run.
	for _, on := range []bool{true, false} {
		in.Network = &on
		plan, err := Build(f, in)
		if err != nil {
			t.Fatal(err)
		}
		want := map[bool]profile.NetMode{true: profile.NetOn, false: profile.NetOff}[on]
		if plan.Network != want || slices.Contains(plan.Args(), "--share-net") != on || slices.Contains(plan.Env, "NODE_USE_ENV_PROXY=1") {
			t.Errorf("override %v: network %q, args %v", on, plan.Network, plan.Flags)
		}
		if strings.Contains(plan.DryRun("bwrap"), "Network: restricted") {
			t.Errorf("override %v still prints the allowlist", on)
		}
	}
}

func TestRefuses(t *testing.T) {
	tests := []struct {
		name   string
		change func(*host.Fake, *Input)
		want   string
	}{
		{"workdir is /", func(f *host.Fake, in *Input) { in.Workdir = "/" }, "refusing to run in /"},
		{"workdir is home", func(f *host.Fake, in *Input) { in.Workdir = "/home/u" }, "whole home folder"},
		{"workdir is a parent of home", func(f *host.Fake, in *Input) { in.Workdir = "/home" }, "whole home folder"},
		{"workdir is the Windows home", func(f *host.Fake, in *Input) { in.Workdir = "/mnt/c/Users/win" }, "Windows user folder"},
		{"workdir inside ~/.ssh", func(f *host.Fake, in *Input) { in.Workdir = "/home/u/.ssh" }, "overlaps /home/u/.ssh"},
		{"rw home", func(f *host.Fake, in *Input) { in.Profile.Extra.RW = []string{"~"} }, "whole home folder"},
		{"rw symlink to ~/.ssh", func(f *host.Fake, in *Input) { in.Profile.Extra.RW = []string{"~/code/link"} }, "overlaps /home/u/.ssh"},
		{"rw ~/.config", func(f *host.Fake, in *Input) { in.Profile.Extra.RW = []string{"~/.config"} }, "overlaps /home/u/.config/box"},
		{"rw PATH folder", func(f *host.Fake, in *Input) { in.Profile.Extra.RW = []string{"~/.local/bin"} }, "overlaps /home/u/.local/bin"},
		{"rw shell startup file", func(f *host.Fake, in *Input) { in.Profile.Extra.RW = []string{"~/.bashrc"} }, "overlaps /home/u/.bashrc"},
		{"rw git config", func(f *host.Fake, in *Input) {
			f.File("/home/u/.gitconfig", "", 0o644)
			in.Profile.Extra.RW = []string{"~/.gitconfig"}
		}, "overlaps /home/u/.gitconfig"},
		{"rw private homes", func(f *host.Fake, in *Input) { in.Profile.Home.RW = []string{"~/.local/share"} }, "overlaps /home/u/.local/share/box"},
		{"rw Windows AppData", func(f *host.Fake, in *Input) {
			in.Profile.Extra.RW = []string{"/mnt/c/Users/win/AppData/Roaming"}
		}, "AppData"},
		{"docker socket read-only", func(f *host.Fake, in *Input) { in.Profile.Extra.RO = []string{"/var/run/docker.sock"} }, "overlaps /run/docker.sock"},
		{"folder containing sockets", func(f *host.Fake, in *Input) { in.Profile.Extra.RO = []string{"/run"} }, "leads out of the box"},
		{"runtime dir", func(f *host.Fake, in *Input) { in.Profile.Home.RO = []string{"/run/user/1000"} }, "leads out of the box"},
		{"any socket", func(f *host.Fake, in *Input) { in.Profile.Extra.RO = []string{"~/code/agent.sock"} }, "it's a socket"},
		{"missing extra", func(f *host.Fake, in *Input) { in.Profile.Extra.RO = []string{"~/nope"} }, "doesn't exist"},
		{"mounted twice", func(f *host.Fake, in *Input) { in.Profile.Extra.RO = []string{"~/code/proj"} }, "mounted twice"},
		{"ro home", func(f *host.Fake, in *Input) { in.Profile.Home.RO = []string{"~"} }, "whole home folder"},
		{"box's own config", func(f *host.Fake, in *Input) { in.Profile.Extra.RO = []string{"~/.config/box"} }, "box keeps its own files"},
		{"box's private homes", func(f *host.Fake, in *Input) {
			f.Dir("/home/u/.local/share/box/homes/other")
			in.Profile.Extra.RO = []string{"~/.local/share/box/homes/other"}
		}, "box keeps its own files"},
		{"rw PATH folder outside home", func(f *host.Fake, in *Input) {
			f.Dir("/opt/tool/bin")
			f.Env["PATH"] = "/opt/tool/bin:" + f.Env["PATH"]
			in.Protected, _ = profile.NewProtected(f, in.Dirs, "", true)
			in.Profile.Extra.RW = []string{"/opt/tool"}
		}, "overlaps /opt/tool/bin"},
		{"PATH with a broken symlink still protects the rest", func(f *host.Fake, in *Input) {
			f.Symlink("/opt/gone", "/nowhere").Dir("/opt/tool/bin")
			f.Env["PATH"] = "/opt/gone:/opt/tool/bin:" + f.Env["PATH"]
			var err error
			if in.Protected, err = profile.NewProtected(f, in.Dirs, "", true); err != nil {
				t.Fatalf("a broken PATH entry stopped box: %v", err)
			}
			in.Profile.Extra.RW = []string{"/opt/tool"}
		}, "overlaps /opt/tool/bin"},
		{"rw ~/.aws", func(f *host.Fake, in *Input) {
			f.Dir("/home/u/.aws")
			in.Profile.Extra.RW = []string{"~/.aws"}
		}, "overlaps /home/u/.aws"},
		{"rw git config under XDG_CONFIG_HOME", func(f *host.Fake, in *Input) {
			f.Dir("/home/u/xdg/git")
			f.Env["XDG_CONFIG_HOME"] = "/home/u/xdg"
			in.Dirs, _ = profile.DirsFor(f)
			in.Protected, _ = profile.NewProtected(f, in.Dirs, "", true)
			in.Profile.Extra.RW = []string{"~/xdg/git"}
		}, "overlaps /home/u/xdg/git"},
		{"interpreter line points at ~/.ssh", func(f *host.Fake, in *Input) {
			in.ProgramDirs = []string{"/home/u/.ssh"}
		}, "holds credentials"},
		{"denied variable", func(f *host.Fake, in *Input) { in.Profile.Env.Pass = []string{"WSL_INTEROP"} }, "way out of the box"},
		{"git symlink", func(f *host.Fake, in *Input) {
			f.Dir("/home/u/code/other").Symlink("/home/u/code/other/.git", "/home/u/.ssh")
			in.Workdir = "/home/u/code/other"
		}, "is a symlink"},
		{"bad profile name", func(f *host.Fake, in *Input) { in.ProfileName = "../x" }, "not valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := machine()
			in := input(t, f, profile.Default("mytool"))
			tt.change(f, &in)
			_, err := Build(f, in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got error %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func indexOf(t *testing.T, plan *Plan, k Kind, dest string) int {
	t.Helper()
	for i, m := range plan.Mounts {
		if m.Kind == k && m.Dest == dest {
			return i
		}
	}
	t.Fatalf("no %v mount at %s in %+v", k, dest, plan.Mounts)
	return -1
}

func TestNestedMountsComeAfterTheirParent(t *testing.T) {
	f := machine()
	p := profile.Default("mytool")
	p.Extra.RO = []string{"~/code/proj/secrets"}
	p.Workdir.ProtectGit = profile.GitHooks
	plan, err := Build(f, input(t, f, p))
	if err != nil {
		t.Fatal(err)
	}
	wd := indexOf(t, plan, Bind, "/home/u/code/proj")
	for _, dest := range []string{"/home/u/code/proj/secrets", "/home/u/code/proj/.git/config", "/home/u/code/proj/.git/hooks"} {
		if i := indexOf(t, plan, ROBind, dest); i < wd {
			t.Errorf("%s (#%d) is mounted before the project (#%d) and would be hidden", dest, i, wd)
		}
	}
	if home := indexOf(t, plan, Bind, "/home/u"); home > wd {
		t.Errorf("private home (#%d) mounted after the project (#%d)", home, wd)
	}
}

func TestEnvironment(t *testing.T) {
	f := machine()
	p := preset(t, "kiro-cli")
	plan, err := Build(f, input(t, f, p))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, kv := range plan.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, k := range []string{"SECRET_TOKEN", "WSL_INTEROP", "SSH_AUTH_SOCK", "XDG_RUNTIME_DIR"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s leaked into the sandbox", k)
		}
	}
	want := map[string]string{
		"HOME":         "/home/u",
		"PWD":          "/home/u/code/proj",
		"TMPDIR":       "/tmp",
		"TERM":         "xterm-256color",
		"LC_TIME":      "en_GB.UTF-8",
		"KIRO_API_KEY": "k-123",
		"BOX_PROFILE":  "kiro-cli/default",
		"PATH":         "/home/u/.local/bin:/usr/local/bin:/usr/bin:/bin",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestPathDropsFoldersThatAreNotMounted(t *testing.T) {
	f := machine()
	in := input(t, f, profile.Default("mytool"))
	in.Program = "/usr/bin/mytool"
	in.ProgramDirs = nil
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Env, "PATH=/usr/local/bin:/usr/bin:/bin") {
		t.Errorf("PATH should drop ~/.local/bin and /mnt/c: %v", plan.Env)
	}
}

func TestFlags(t *testing.T) {
	off := false
	tests := []struct {
		name       string
		change     func(*Input)
		want       []string
		wantFilter Filter
	}{
		{"kernel blocks TIOCSTI", func(in *Input) {}, []string{"--unshare-all", "--share-net", "--die-with-parent"}, Filter{Vsock: true}},
		{"legacy TIOCSTI", func(in *Input) { in.LegacyTIOCSTI = "1" }, []string{"--unshare-all", "--share-net", "--die-with-parent"}, Filter{TIOCSTI: true, Vsock: true}},
		{"unknown TIOCSTI", func(in *Input) { in.LegacyTIOCSTI = "" }, []string{"--unshare-all", "--share-net", "--die-with-parent"}, Filter{TIOCSTI: true, Vsock: true}},
		{"no filter possible", func(in *Input) { in.LegacyTIOCSTI = "1"; in.Arch = "riscv64" }, []string{"--unshare-all", "--share-net", "--die-with-parent", "--new-session"}, Filter{}},
		{"not WSL", func(in *Input) { in.Protected.WSL = false }, []string{"--unshare-all", "--share-net", "--die-with-parent"}, Filter{}},
		{"--no-net", func(in *Input) { in.Network = &off }, []string{"--unshare-all", "--die-with-parent"}, Filter{Vsock: true}},
		{"strict", func(in *Input) { in.Profile.Sandbox.Strict = true }, []string{"--unshare-all", "--share-net", "--die-with-parent", "--unshare-user", "--disable-userns"}, Filter{Vsock: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := machine()
			in := input(t, f, preset(t, "kiro-cli"))
			tt.change(&in)
			plan, err := Build(f, in)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(plan.Flags, tt.want) {
				t.Errorf("flags %v, want %v", plan.Flags, tt.want)
			}
			if plan.Filter != tt.wantFilter {
				t.Errorf("filter %+v, want %+v", plan.Filter, tt.wantFilter)
			}
			if hasSeccomp := slices.Contains(plan.Args(), "--seccomp"); hasSeccomp != tt.wantFilter.Any() {
				t.Errorf("--seccomp in args: %v, want %v", hasSeccomp, tt.wantFilter.Any())
			}
		})
	}
}

func TestProgramFoldersAreAlwaysReadOnly(t *testing.T) {
	f := machine()
	f.Dir("/home/u/.kiro/crew-venv/bin").Dir("/home/u/tools").Dir("/home/u/code/proj/scripts")
	p := profile.Default("kirocrew")
	p.Home.RW = []string{"~/.kiro"}
	p.Extra.RW = []string{"~/tools"}
	in := input(t, f, p)
	in.ProgramDirs = []string{"/home/u/.kiro/crew-venv", "/home/u/tools", "/home/u/code/proj/scripts"}
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	kiro := indexOf(t, plan, Bind, "/home/u/.kiro")
	if venv := indexOf(t, plan, ROBind, "/home/u/.kiro/crew-venv"); venv < kiro {
		t.Errorf("the read-only venv (#%d) must mount after ~/.kiro (#%d)", venv, kiro)
	}
	indexOf(t, plan, ROBind, "/home/u/tools") // a read-write folder that is a program folder
	for _, m := range plan.Mounts {
		if m.Dest == "/home/u/code/proj/scripts" {
			t.Errorf("a program folder inside the project keeps the project's mode, got %+v", m)
		}
	}
}

func TestInitAndLandlock(t *testing.T) {
	f := machine()
	p := preset(t, "kiro-cli")
	p.Sandbox.Init = true
	p.Sandbox.Landlock = true
	in := input(t, f, p)
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	if i := indexOf(t, plan, ROBind, InitPath); plan.Mounts[i].Src != "/home/u/.local/bin/box" {
		t.Errorf("init mounted from %s", plan.Mounts[i].Src)
	}
	want := []string{InitPath, "--box-init", "--", "/home/u/.local/bin/kiro-cli", "chat", "it's a test"}
	if !slices.Equal(plan.Command, want) {
		t.Errorf("command %q, want %q", plan.Command, want)
	}
	if !plan.Landlock {
		t.Error("landlock should be on with network on")
	}
	off := false
	in.Network = &off
	if plan, _ := Build(f, in); plan.Landlock {
		t.Error("landlock is only needed with network on")
	}
}

func TestProgramEnv(t *testing.T) {
	f := machine()
	in := input(t, f, profile.Default("mytool"))
	in.ProgramEnv = []string{"APPIMAGE_EXTRACT_AND_RUN=1"}
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.Env, "APPIMAGE_EXTRACT_AND_RUN=1") {
		t.Errorf("program env missing: %v", plan.Env)
	}
}

func TestArgsEndWithTheCommandUntouched(t *testing.T) {
	f := machine()
	plan, err := Build(f, input(t, f, profile.Default("mytool")))
	if err != nil {
		t.Fatal(err)
	}
	args := plan.Args()
	tail := []string{"--chdir", "/home/u/code/proj", "--", "/home/u/.local/bin/kiro-cli", "chat", "it's a test"}
	if !slices.Equal(args[len(args)-len(tail):], tail) {
		t.Errorf("args end with %q", args[len(args)-len(tail):])
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":             "''",
		"/usr/bin":     "/usr/bin",
		"it's":         `'it'\''s'`,
		"a b":          "'a b'",
		"$HOME":        "'$HOME'",
		"KEY=v:/x,y@z": "KEY=v:/x,y@z",
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestProgramDirectlyInHomeIsRefused(t *testing.T) {
	f := machine()
	for _, dir := range []string{"/home/u", "/home"} {
		in := input(t, f, profile.Default("mytool"))
		in.ProgramDirs = []string{dir}
		if _, err := Build(f, in); err == nil || !strings.Contains(err.Error(), "whole home folder") {
			t.Errorf("%s: got %v", dir, err)
		}
	}
}

func TestProtectedFoldersCantBeRenamedAside(t *testing.T) {
	f := machine()
	f.Dir("/home/u/.kiro/crew/venv")
	p := profile.Default("kirocrew")
	p.Home.RW = []string{"~/.kiro"}
	p.Workdir.ProtectGit = profile.GitHooks
	in := input(t, f, p)
	in.ProgramDirs = []string{"/home/u/.kiro/crew/venv"}
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	// .git and crew become mount points of their own, which Linux won't rename.
	for _, pin := range []string{"/home/u/code/proj/.git", "/home/u/.kiro/crew"} {
		i := indexOf(t, plan, Bind, pin)
		if plan.Mounts[i].Src != pin {
			t.Errorf("%s pinned from %s", pin, plan.Mounts[i].Src)
		}
	}
	if pin, cfg := indexOf(t, plan, Bind, "/home/u/code/proj/.git"), indexOf(t, plan, ROBind, "/home/u/code/proj/.git/config"); pin > cfg {
		t.Errorf("the pin (#%d) must mount before the protected file (#%d)", pin, cfg)
	}
}

func TestMissingGitHooksAreCreatedReadOnly(t *testing.T) {
	f := machine()
	f.Dir("/home/u/code/nohooks/.git").File("/home/u/code/nohooks/.git/config", "", 0o644)
	p := profile.Default("mytool")
	p.Workdir.ProtectGit = profile.GitHooks
	in := input(t, f, p)
	in.Workdir = "/home/u/code/nohooks"
	plan, err := Build(f, in)
	if err != nil {
		t.Fatal(err)
	}
	indexOf(t, plan, ROBind, "/home/u/code/nohooks/.git/hooks")
	if !slices.Contains(plan.Create, "/home/u/code/nohooks/.git/hooks") {
		t.Errorf("hooks folder not created: %v", plan.Create)
	}
}

func TestPlantedSymlinkInWritableFolderIsRefused(t *testing.T) {
	f := machine()
	f.Dir("/home/u/.aws").Symlink("/home/u/code/proj/vendor", "/home/u/.aws")
	f.Symlink("/home/u/code/lib-link", "/home/u/code/shared-lib")
	p := profile.Default("mytool")
	p.Extra.RO = []string{"~/code/proj/vendor"}
	if _, err := Build(f, input(t, f, p)); err == nil || !strings.Contains(err.Error(), "which the program can write") {
		t.Errorf("planted symlink: got %v", err)
	}
	// The user's own symlinks, outside anything writable, are fine.
	p.Extra.RO = []string{"~/code/lib-link"}
	if _, err := Build(f, input(t, f, p)); err != nil {
		t.Errorf("user symlink refused: %v", err)
	}
}

func TestProgramInFreshFolderIsRefused(t *testing.T) {
	f := machine()
	in := input(t, f, profile.Default("mytool"))
	in.ProgramDirs = []string{"/tmp"}
	f.Dir("/tmp")
	if _, err := Build(f, in); err == nil || !strings.Contains(err.Error(), "fresh, private") {
		t.Errorf("got %v", err)
	}
}

func TestEnvArgsCarryTheWholeEnvironment(t *testing.T) {
	f := machine()
	p := profile.Default("mytool")
	p.Env.Set = map[string]string{"LD_PRELOAD": "/x.so", "GREETING": "a b=c"}
	plan, err := Build(f, input(t, f, p))
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Split(strings.TrimSuffix(string(plan.EnvArgs()), "\x00"), "\x00")
	if len(words) != 3*len(plan.Env) {
		t.Fatalf("%d words for %d variables: %q", len(words), len(plan.Env), words)
	}
	for i, kv := range plan.Env {
		k, v, _ := strings.Cut(kv, "=")
		if got := words[3*i : 3*i+3]; !slices.Equal(got, []string{"--setenv", k, v}) {
			t.Errorf("variable %d: %q", i, got)
		}
	}
	// The environment travels only through --args, never on the command line.
	if slices.Contains(plan.Args(), "--setenv") || slices.Contains(plan.Args(), "/x.so") {
		t.Errorf("environment on the command line: %q", plan.Args())
	}
}

func TestDryRunHidesPassedValues(t *testing.T) {
	f := machine()
	f.Env["FOO_API_KEY"] = "s3cret-value"
	f.Env["SHOWN"] = "from-host"
	p := profile.Default("mytool")
	p.Env.Pass = []string{"FOO_API_KEY", "SHOWN"}
	p.Env.Set = map[string]string{"SHOWN": "from-profile"}
	plan, err := Build(f, input(t, f, p))
	if err != nil {
		t.Fatal(err)
	}
	dry := plan.DryRun("bwrap")
	if strings.Contains(dry, "s3cret") || !strings.Contains(dry, `--setenv FOO_API_KEY "$FOO_API_KEY"`) {
		t.Errorf("passed value printed, or its reference missing:\n%s", dry)
	}
	// A value the profile sets is the profile's own, and shown.
	if !strings.Contains(dry, "--setenv SHOWN from-profile") {
		t.Errorf("set value missing:\n%s", dry)
	}
	if !slices.Contains(plan.Env, "FOO_API_KEY=s3cret-value") {
		t.Errorf("the program must still get the value: %v", plan.Env)
	}
}

// A script in the project chooses its interpreter. Pointing it into ~/.ssh
// must not get ~/.ssh mounted on the next run.
func TestScriptCantSteerMountsToSecrets(t *testing.T) {
	f := machine()
	f.File("/home/u/.ssh/x", "", 0o755)
	f.File("/home/u/code/proj/run.sh", "#!/home/u/.ssh/x\n", 0o755)
	prog, err := Lookup(f, "./run.sh", f.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	in := input(t, f, profile.Default("run.sh"))
	in.Program, in.ProgramDirs = prog.Path, prog.Dirs
	if _, err := Build(f, in); err == nil || !strings.Contains(err.Error(), "holds credentials") {
		t.Fatalf("got %v, want a refusal naming credentials", err)
	}
}

func TestGitProtection(t *testing.T) {
	const proj = "/home/u/code/g"
	ro := func(paths ...string) []string { return paths }
	tests := []struct {
		name   string
		mode   profile.GitMode
		layout func(f *host.Fake)
		ro     []string // read-only binds of these paths, at their own place
		create []string
		notRO  []string
	}{
		{"full: plain repo", profile.GitFull, func(f *host.Fake) { f.Dir(proj + "/.git/hooks") },
			ro(proj + "/.git"), nil, ro(proj + "/.git/hooks")},
		{"full: no repo gets an empty read-only .git", profile.GitFull, func(f *host.Fake) { f.Dir(proj) },
			ro(proj + "/.git"), ro(proj + "/.git"), nil},
		{"full: nested repos", profile.GitFull, func(f *host.Fake) {
			f.Dir(proj + "/.git").Dir(proj + "/vendor/lib/.git").Dir(proj + "/a/b/c/d/.git")
		}, ro(proj+"/.git", proj+"/vendor/lib/.git", proj+"/a/b/c/d/.git"), nil, nil},
		{"full: too deep isn't searched", profile.GitFull, func(f *host.Fake) {
			f.Dir(proj + "/.git").Dir(proj + "/a/b/c/d/e/.git")
		}, ro(proj + "/.git"), nil, ro(proj + "/a/b/c/d/e/.git")},
		{"full: worktree .git file pointing outside", profile.GitFull, func(f *host.Fake) {
			f.Dir("/home/u/code/main/.git/worktrees/g")
			f.File(proj+"/.git", "gitdir: /home/u/code/main/.git/worktrees/g\n", 0o644)
		}, ro(proj + "/.git"), nil, ro("/home/u/code/main/.git/worktrees/g")},
		{"full: submodule .git file inside the project", profile.GitFull, func(f *host.Fake) {
			f.Dir(proj + "/.git/modules/sub")
			f.File(proj+"/sub/.git", "gitdir: ../.git/modules/sub\n", 0o644)
		}, ro(proj+"/.git", proj+"/sub/.git"), nil, nil},
		{"hooks: nested repo", profile.GitHooks, func(f *host.Fake) {
			f.Dir(proj+"/.git/hooks").File(proj+"/.git/config", "", 0o644).Dir(proj + "/lib/.git")
		}, ro(proj+"/.git/config", proj+"/.git/hooks", proj+"/lib/.git/hooks"), ro(proj + "/lib/.git/hooks"), ro(proj + "/.git")},
		{"hooks: no repo stays as it is", profile.GitHooks, func(f *host.Fake) { f.Dir(proj) }, nil, nil, ro(proj + "/.git")},
		{"off", profile.GitOff, func(f *host.Fake) { f.Dir(proj + "/.git/hooks") }, nil, nil, ro(proj+"/.git", proj+"/.git/hooks")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := machine()
			tt.layout(f)
			p := profile.Default("mytool")
			p.Workdir.ProtectGit = tt.mode
			in := input(t, f, p)
			in.Workdir = proj
			plan, err := Build(f, in)
			if err != nil {
				t.Fatal(err)
			}
			isRO := func(path string) bool {
				return slices.ContainsFunc(plan.Mounts, func(m Mount) bool { return m.Kind == ROBind && m.Src == path && m.Dest == path })
			}
			for _, path := range tt.ro {
				if !isRO(path) {
					t.Errorf("%s isn't read-only", path)
				}
			}
			for _, path := range tt.notRO {
				if isRO(path) {
					t.Errorf("%s is read-only", path)
				}
			}
			for _, path := range tt.create {
				if !slices.Contains(plan.Create, path) {
					t.Errorf("%s isn't created: %v", path, plan.Create)
				}
			}
		})
	}
}

func TestGitFileWithoutGitdirIsRefused(t *testing.T) {
	f := machine()
	f.File("/home/u/code/g/.git", "nonsense", 0o644)
	in := input(t, f, profile.Default("mytool"))
	in.Workdir = "/home/u/code/g"
	if _, err := Build(f, in); err == nil || !strings.Contains(err.Error(), "gitdir") {
		t.Fatalf("got %v", err)
	}
}
