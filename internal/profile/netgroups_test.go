package profile

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNetModeReadsOldBool(t *testing.T) {
	for in, want := range map[string]NetMode{
		"network = true":         NetOn,
		"network = false":        NetOff,
		`network = "restricted"`: NetRestricted,
		"":                       NetOff,
		`network = "on"`:         NetOn,
	} {
		p, err := Parse([]byte(in), "x")
		if err != nil || p.Network != want {
			t.Errorf("%q: network %q, err %v; want %q", in, p.Network, err, want)
		}
	}
	if _, err := Parse([]byte("network = 3"), "x"); err == nil {
		t.Error("a number must be refused")
	}
	p, _ := Parse([]byte(`network = "sometimes"`), "x")
	if err := p.Validate("x"); err == nil || !strings.Contains(err.Error(), "network must be") {
		t.Errorf("unknown mode: %v", err)
	}
}

func TestCheckHostNamesThePortWhenTheAddressIsFine(t *testing.T) {
	for _, s := range []string{"10.0.0.5:0", "10.0.0.5:99999", "[2001:db8::1]:0"} {
		if err := CheckHost(s); err == nil || !strings.Contains(err.Error(), "port must be") {
			t.Errorf("%q: %v", s, err)
		}
	}
	if err := CheckHost("999.1.1.1:80"); err == nil || !strings.Contains(err.Error(), "not an IP address") {
		t.Errorf("999.1.1.1:80: %v", err)
	}
}

func TestCheckHost(t *testing.T) {
	for _, ok := range []string{"docs.example.com", "*.example.com", "api.example.com:8443", "localhost",
		"10.0.0.5", "10.0.0.5:8443", "2001:db8::1", "[2001:db8::1]:443"} {
		if err := CheckHost(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "*", "*.com", "*.*.example.com", "Docs.Example.com", "https://x.com", "x.com/docs",
		"a b.com", "-x.com", "x.com:0", "x.com:99999", "x.com:", "exa*mple.com",
		"999.1.1.1", "1.2.3", "10.0.0.5:0", "10.0.0.5:99999", "10.0.0.0/24", "[2001:db8::1]", "2001:db8::zz", "fe80::1%eth0"} {
		if err := CheckHost(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// The embedded lists are data: every entry must be something the proxy can match.
func TestNetGroupsData(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range netGroups() {
		if !ValidName(g.ID) || seen[g.ID] {
			t.Errorf("group id %q is invalid or repeated", g.ID)
		}
		seen[g.ID] = true
		if g.Label == "" || len(g.Hosts) == 0 {
			t.Errorf("%s: needs a label and hosts", g.ID)
		}
		switch g.Kind {
		case "docs", "packages", "service":
			if len(g.Programs) > 0 {
				t.Errorf("%s: only program groups name programs", g.ID)
			}
		case "program":
			if len(g.Programs) == 0 {
				t.Errorf("%s: a program group must name its programs", g.ID)
			}
		default:
			t.Errorf("%s: unknown kind %q", g.ID, g.Kind)
		}
		for _, h := range g.Hosts {
			if err := CheckHost(h); err != nil {
				t.Errorf("%s: %v", g.ID, err)
			}
		}
		if g.Kind == "docs" {
			for _, h := range g.Hosts {
				if strings.HasPrefix(h, "*.") {
					t.Errorf("%s: %s: documentation groups name exact hosts, so strangers' subdomains stay out", g.ID, h)
				}
			}
		}
	}
	if slices.ContainsFunc(NetGroups(), func(g NetGroup) bool { return g.Kind == "program" }) {
		t.Error("NetGroups must leave out the programs' own servers")
	}
}

// The programs box is built for must keep working on a restricted network.
func TestOwnHostsKeepTheProgramWorking(t *testing.T) {
	for prog, host := range map[string]string{
		"kiro-cli": "runtime.us-east-1.kiro.dev",
		"kirocrew": "runtime.us-east-1.kiro.dev",
		"claude":   "api.anthropic.com",
	} {
		if !slices.Contains(OwnHosts(prog), host) {
			t.Errorf("%s: own servers %v lack %s", prog, OwnHosts(prog), host)
		}
	}
	if OwnHosts("mytool") != nil {
		t.Error("an unknown program has no servers of its own")
	}
}

func TestAllowedAndRoundTrip(t *testing.T) {
	p, err := Preset("kiro-cli")
	if err != nil {
		t.Fatal(err)
	}
	p.Network = NetRestricted
	p.Allow = Allow{Groups: []string{"docs-microsoft"}, Hosts: []string{"wiki.example.com"}}
	sets := p.Allowed()
	if len(sets) != 3 || !strings.Contains(sets[0].Name, "own servers") ||
		!slices.Contains(sets[1].Hosts, "learn.microsoft.com") || sets[2].Hosts[0] != "wiki.example.com" {
		t.Fatalf("allowed: %+v", sets)
	}
	path := filepath.Join(t.TempDir(), "kiro-cli", "default.toml")
	if err := Save(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "kiro-cli")
	if err != nil {
		t.Fatal(err)
	}
	if got.Network != NetRestricted || !slices.Equal(got.Allow.Groups, p.Allow.Groups) || !slices.Equal(got.Allow.Hosts, p.Allow.Hosts) {
		t.Errorf("round trip: %+v %+v", got.Network, got.Allow)
	}

	for _, bad := range []Allow{
		{Groups: []string{"no-such-group"}},
		{Groups: []string{"kiro"}}, // a program's own group can't be listed
		{Groups: []string{"docs-microsoft", "docs-microsoft"}},
		{Hosts: []string{"*.com"}},
		{Hosts: []string{"a.example.com", "a.example.com"}},
	} {
		p.Allow = bad
		if err := p.Validate("kiro-cli"); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
}
