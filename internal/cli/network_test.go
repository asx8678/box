package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asx8678/box/internal/egress"
	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
)

func TestNetLogAndRecentlyBlocked(t *testing.T) {
	d := profile.Dirs{State: filepath.Join(t.TempDir(), "state")}
	l := openNetLog(d, "tool/default")
	l.write(egress.Event{Host: "ok.example.com", Port: 443, Allowed: true})
	for range 3 {
		l.write(egress.Event{Host: "a.example.com", Port: 443, Reason: "not in the allowed network"})
	}
	l.write(egress.Event{Host: "b.example.com", Port: 8443, Reason: "not in the allowed network"})
	l.write(egress.Event{Host: "evil\n\x1b[31m", Port: 443, Reason: "x"})
	l.write(egress.Event{Host: "lan.example.com", Port: 443, Reason: "resolves only to private addresses"})
	l.close()
	if got := l.blockedSummary(); !strings.HasPrefix(got, "6 connections: a.example.com:443 (3), ") {
		t.Errorf("summary %q", got)
	}
	data, _ := os.ReadFile(netLogPath(d))
	if n := strings.Count(string(data), "\n"); n != 7 || strings.Contains(string(data), "\x1b") {
		t.Errorf("a name from the program broke the log:\n%s", data)
	}
	// Another program's blocks aren't offered; an allowed host isn't either.
	other := openNetLog(d, "other/default")
	other.write(egress.Event{Host: "c.example.com", Port: 443})
	other.close()
	p := profile.Default("tool")
	p.Allow.Hosts = []string{"b.example.com:8443"}
	// Nor is one that resolved into the LAN: that takes typing it.
	if got := recentlyBlocked(d, "tool", p); len(got) != 1 || got[0].Host != "a.example.com" {
		t.Errorf("recently blocked %v", got)
	}
}

func TestEgressSocket(t *testing.T) {
	f := newFakeHome(t)
	f.Env["XDG_RUNTIME_DIR"] = "/run/user/1000"
	if s := egressSocket(f); !strings.HasPrefix(s, "/run/user/1000/box/egress-") || len(s) > 100 {
		t.Errorf("socket %s", s)
	}
	delete(f.Env, "XDG_RUNTIME_DIR")
	if s := egressSocket(f); !strings.HasPrefix(s, filepath.Join(os.TempDir(), "box-")) {
		t.Errorf("socket without a runtime folder: %s", s)
	}
}

func newFakeHome(t *testing.T) *host.Fake {
	f := host.NewFake().Dir("/home/u")
	f.Env["HOME"] = "/home/u"
	return f
}
