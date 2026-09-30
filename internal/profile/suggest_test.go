package profile

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asx8678/box/internal/host"
)

func TestSuggest(t *testing.T) {
	f := host.NewFake()
	f.Dir("/home/u/.kiro").Dir("/home/u/.local/share/kiro-cli").Dir("/home/u/.cache/kiro-cli")
	f.File("/home/u/.kiro-cli", "not a folder", 0o644).Dir("/home/u/.config/other")
	p := Default("kiro-cli")
	p.Home.RW = []string{"~/.local/share/kiro-cli"}
	got := Suggest(f, "/home/u", p)
	want := []string{"~/.cache/kiro-cli", "~/.kiro"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := Suggest(f, "/home/u", Default("nothing")); len(got) != 0 {
		t.Errorf("unknown program got %v", got)
	}
}

func TestCheckConfigDir(t *testing.T) {
	root := t.TempDir()
	d := Dirs{Config: filepath.Join(root, "box")}
	if err := CheckConfigDir(d); err != nil {
		t.Errorf("a missing folder is fine: %v", err)
	}
	os.Mkdir(d.Config, 0o700)
	if err := CheckConfigDir(d); err != nil {
		t.Errorf("0700 is fine: %v", err)
	}
	os.Chmod(d.Config, 0o755)
	if err := CheckConfigDir(d); err == nil || !strings.Contains(err.Error(), "chmod 700") {
		t.Errorf("0755 should be refused, got %v", err)
	}
}

func TestValidateTools(t *testing.T) {
	p := Default("x")
	p.Tools = []string{"node", "../evil"}
	if err := p.Validate("x"); err == nil || !strings.Contains(err.Error(), "tools") {
		t.Errorf("got %v", err)
	}
}
