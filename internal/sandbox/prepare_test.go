package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realDir is a temporary folder with symlinks resolved (macOS puts them
// under /var, a symlink to /private/var).
func realDir(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPrepareCreatesMountPoints(t *testing.T) {
	root := realDir(t)
	privHome := filepath.Join(root, "homes/tool/default")
	proj := filepath.Join(root, "real/code/proj")
	kiro := filepath.Join(root, "real/.kiro")
	gitconfig := filepath.Join(root, "real/.gitconfig")
	for _, d := range []string{proj + "/.git/hooks", kiro} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(proj+"/.git/config", nil, 0o644)
	os.WriteFile(gitconfig, nil, 0o644)

	home := "/home/u" // inside the sandbox
	plan := &Plan{
		Create: []string{privHome},
		Mounts: []Mount{
			{Tmpfs, "", "/run"},
			{Bind, privHome, home},
			{ROBindData, "", "/run/box/profile"},
			{ROBind, kiro, home + "/.kiro"},
			{ROBind, gitconfig, home + "/.gitconfig"},
			{Bind, proj, home + "/code/proj"},
			{ROBind, proj + "/.git/config", home + "/code/proj/.git/config"},
			{ROBind, proj + "/.git/hooks", home + "/code/proj/.git/hooks"},
		},
	}
	if err := Prepare(plan); err != nil {
		t.Fatal(err)
	}
	for path, dir := range map[string]bool{
		privHome + "/.kiro":      true,
		privHome + "/.gitconfig": false,
		privHome + "/code/proj":  true,
	} {
		fi, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("%s wasn't created: %v", path, err)
		}
		if fi.IsDir() != dir {
			t.Errorf("%s: dir=%v, want %v", path, fi.IsDir(), dir)
		}
	}
	if fi, _ := os.Stat(privHome); fi.Mode().Perm() != 0o700 {
		t.Errorf("private home mode %v, want 0700", fi.Mode().Perm())
	}
	// Running again is fine.
	if err := Prepare(plan); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

func TestPrepareRefusesPlantedSymlinks(t *testing.T) {
	for _, planted := range []string{"code", "code/proj", ".kiro"} {
		t.Run(planted, func(t *testing.T) {
			root := realDir(t)
			privHome := filepath.Join(root, "home")
			outside := filepath.Join(root, "outside")
			os.MkdirAll(outside, 0o755)
			os.MkdirAll(filepath.Join(privHome, filepath.Dir(planted)), 0o700)
			if err := os.Symlink(outside, filepath.Join(privHome, planted)); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(root, "src")
			os.MkdirAll(src, 0o755)
			plan := &Plan{Mounts: []Mount{
				{Bind, privHome, "/home/u"},
				{ROBind, src, "/home/u/.kiro"},
				{Bind, src, "/home/u/code/proj"},
			}}
			err := Prepare(plan)
			if err == nil || !strings.Contains(err.Error(), "is a symlink") {
				t.Fatalf("got %v, want a symlink refusal", err)
			}
			if entries, _ := os.ReadDir(outside); len(entries) > 0 {
				t.Errorf("something was created through the symlink: %v", entries)
			}
		})
	}
}

func TestPrepareRefusesSymlinkInProject(t *testing.T) {
	root := realDir(t)
	proj := filepath.Join(root, "proj")
	os.MkdirAll(proj, 0o755)
	os.MkdirAll(filepath.Join(root, "elsewhere"), 0o755)
	os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(proj, "secrets"))
	secrets := filepath.Join(root, "secrets-src")
	os.MkdirAll(secrets, 0o755)
	plan := &Plan{Mounts: []Mount{
		{Bind, proj, proj},
		{ROBind, secrets, proj + "/secrets"},
	}}
	if err := Prepare(plan); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("got %v", err)
	}
}

func TestCovering(t *testing.T) {
	mounts := []Mount{{Tmpfs, "", "/tmp"}, {Bind, "/h", "/home/u"}, {Bind, "/p", "/home/u/code/p"}}
	if m, ok := covering(mounts, "/home/u/code/p/.git/config"); !ok || m.Src != "/p" {
		t.Errorf("got %v %v", m, ok)
	}
	if _, ok := covering(mounts, "/home/u"); ok {
		t.Error("a mount doesn't cover itself")
	}
	if _, ok := covering(mounts, "/etc/hosts"); ok {
		t.Error("nothing covers /etc")
	}
}
