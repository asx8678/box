package sandbox

import (
	"errors"
	"slices"
	"testing"
)

func TestLookup(t *testing.T) {
	f := machine()
	// A Kiro-style binary with helpers next to it.
	f.File("/home/u/.local/bin/kiro-cli-chat", "\x7fELF", 0o755)
	// A pipx/uv-style Python app: launcher symlink → venv script → venv python → base python.
	f.File("/home/u/.local/share/uv/python/cpython-3.12/bin/python3.12", "\x7fELF", 0o755)
	f.Symlink("/home/u/.kiro/crew-venv/bin/python", "/home/u/.local/share/uv/python/cpython-3.12/bin/python3.12")
	f.File("/home/u/.kiro/crew-venv/pyvenv.cfg", "home = /home/u/.local/share/uv/python/cpython-3.12/bin\nversion = 3.12\n", 0o644)
	f.File("/home/u/.kiro/crew-venv/bin/kirocrew", "#!/home/u/.kiro/crew-venv/bin/python\nimport sys\n", 0o755)
	f.Symlink("/home/u/.local/bin/kirocrew", "../../.kiro/crew-venv/bin/kirocrew")
	// A node script through env, with node from nvm.
	f.File("/home/u/.nvm/versions/node/v22/bin/node", "\x7fELF", 0o755)
	f.File("/home/u/code/proj/tool.js", "#!/usr/bin/env -S node --no-warnings\n", 0o755)
	f.Env["PATH"] += ":/home/u/.nvm/versions/node/v22/bin"
	// System programs and Windows ones.
	f.File("/usr/bin/dash", "\x7fELF", 0o755).Symlink("/usr/bin/sh", "dash")
	f.File("/mnt/c/Windows/System32/cmd.exe", "MZ", 0o755)
	f.File("/home/u/code/proj/setup.exe", "MZ\x90", 0o755)
	f.File("/home/u/code/proj/notes.txt", "hi", 0o644)

	tests := []struct {
		name     string
		wantPath string
		wantDirs []string
	}{
		{"kiro-cli", "/home/u/.local/bin/kiro-cli", []string{"/home/u/.local/bin"}},
		{"sh", "/usr/bin/sh", nil},
		{"kirocrew", "/home/u/.local/bin/kirocrew", []string{
			"/home/u/.local/bin", "/home/u/.kiro/crew-venv/bin",
			"/home/u/.kiro/crew-venv", "/home/u/.local/share/uv/python/cpython-3.12",
		}},
		{"./tool.js", "/home/u/code/proj/tool.js", []string{"/home/u/code/proj", "/home/u/.nvm/versions/node/v22"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Lookup(f, tt.name, "/home/u/code/proj")
			if err != nil {
				t.Fatal(err)
			}
			if p.Path != tt.wantPath || !slices.Equal(p.Dirs, tt.wantDirs) {
				t.Errorf("got %s %q, want %s %q", p.Path, p.Dirs, tt.wantPath, tt.wantDirs)
			}
		})
	}

	appImage := "\x7fELF\x02\x01\x01\x00AI\x02\x00\x00\x00\x00\x00"
	f.File("/home/u/Apps/tool.AppImage", appImage, 0o755)
	f.File("/usr/bin/snap", "\x7fELF", 0o755).Symlink("/snap/bin/lxd", "/usr/bin/snap")
	if p, err := Lookup(f, "/home/u/Apps/tool.AppImage", "/home/u/code/proj"); err != nil ||
		!slices.Equal(p.Env, []string{"APPIMAGE_EXTRACT_AND_RUN=1"}) {
		t.Errorf("AppImage: %+v, %v", p, err)
	}
	if p, _ := Lookup(f, "kiro-cli", "/home/u/code/proj"); len(p.Env) != 0 {
		t.Errorf("a plain ELF binary got env %v", p.Env)
	}

	fails := []struct {
		name string
		code int
	}{
		{"nope", ExitNotFound},
		{"./missing", ExitNotFound},
		{"./notes.txt", ExitCannotRun},
		{"/mnt/c/Windows/System32/cmd.exe", ExitCannotRun},
		{"./setup.exe", ExitCannotRun},
		{"./.hidden", ExitCannotRun},
		{"/snap/bin/lxd", ExitCannotRun},
	}
	for _, tt := range fails {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Lookup(f, tt.name, "/home/u/code/proj")
			var le *LookupError
			if !errors.As(err, &le) || le.Code != tt.code {
				t.Fatalf("got %v, want exit %d", err, tt.code)
			}
		})
	}
}

func TestShebang(t *testing.T) {
	for in, want := range map[string]string{
		"#!/usr/bin/python3 -u\n":      "/usr/bin/python3",
		"#!/usr/bin/env python3\n":     "python3",
		"#! /usr/bin/env -S node -x\n": "node",
		"#!/usr/bin/env FOO=1 ruby\n":  "ruby",
		"\x7fELF":                      "",
		"#!\n":                         "",
	} {
		got, _ := shebang([]byte(in))
		if got != want {
			t.Errorf("shebang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupShellScriptsAndSymlinkedFolders(t *testing.T) {
	f := machine()
	f.File("/usr/bin/dash", "\x7fELF", 0o755).Symlink("/usr/bin/sh", "dash")
	f.File("/home/u/code/proj/run.sh", "#!/bin/sh\necho hi\n", 0o755)
	p, err := Lookup(f, "./run.sh", "/home/u/code/proj")
	if err != nil || !slices.Equal(p.Dirs, []string{"/home/u/code/proj"}) {
		t.Errorf("#!/bin/sh script: %+v, %v", p, err)
	}
	f.File("/data/proj/run", "\x7fELF", 0o755).Symlink("/home/u/code/p2", "/data/proj")
	p, err = Lookup(f, "./run", "/home/u/code/p2")
	if err != nil || p.Path != "/data/proj/run" {
		t.Errorf("symlinked folder: path %q, %v", p.Path, err)
	}
}
