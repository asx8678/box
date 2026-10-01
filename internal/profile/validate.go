package profile

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,63}$`)
	envRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// ipLikeRE matches what was meant as an IP address: only digits and
	// dots (with a port, perhaps), brackets, or more than one colon.
	ipLikeRE = regexp.MustCompile(`^[0-9.]+(:[0-9]*)?$|[\[\]]|:.*:`)
	hostRE   = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]*[a-z0-9])?(:[0-9]{1,5})?$`)
)

// deniedEnv are variables that hand the program a way out of the box:
// Windows interop, the SSH and GPG agents, the D-Bus session bus.
var deniedEnv = map[string]bool{
	"WSL_INTEROP":              true,
	"SSH_AUTH_SOCK":            true,
	"GPG_AGENT_INFO":           true,
	"DBUS_SESSION_BUS_ADDRESS": true,
}

// reservedEnv are set by box itself.
var reservedEnv = map[string]bool{
	"HOME":        true,
	"PWD":         true,
	"BOX_PROFILE": true,
}

// ValidName reports whether s can be a program or profile name: letters,
// digits, '.', '_', '+' and '-', not starting with a dot, plus or dash.
func ValidName(s string) bool {
	return nameRE.MatchString(s)
}

// CheckEnvName says why variable k can't be passed to or set for a
// program, or returns nil if it can.
func CheckEnvName(k string) error {
	switch {
	case !envRE.MatchString(k):
		return fmt.Errorf("%q is not a variable name", k)
	case deniedEnv[k]:
		return fmt.Errorf("%s would give the program a way out of the box", k)
	case reservedEnv[k]:
		return fmt.Errorf("%s is set by box", k)
	}
	return nil
}

// CheckHost says why s can't be an allowed host, or returns nil if it can:
// a lower-case host name, "*.suffix" for every name below suffix, or an IP
// address, each with an optional ":port" (an IPv6 address goes in brackets
// before a port). A wildcard needs a suffix of two labels or more, so
// "*.com" can't allow half the internet.
func CheckHost(s string) error {
	if strings.Contains(s, "/") {
		return fmt.Errorf("%q: give one host or one IP address; paths and ranges like 10.0.0.0/24 aren't supported", s)
	}
	addr, err := netip.ParseAddr(s)
	if ap, perr := netip.ParseAddrPort(s); err != nil && perr == nil && ap.Port() != 0 {
		addr, err = ap.Addr(), nil
	}
	switch {
	case err == nil && addr.Zone() != "":
		return fmt.Errorf("%q: an address with a zone (%%%s) only means something on one machine", s, addr.Zone())
	case err == nil:
		return nil
	case ipLikeRE.MatchString(s):
		// A good address with a port out of range gets the port's message.
		if i := strings.LastIndex(s, ":"); i > 0 {
			a, aerr := netip.ParseAddr(strings.Trim(s[:i], "[]"))
			if aerr == nil && (a.Is4() || s[0] == '[') {
				return fmt.Errorf("%q: the port must be between 1 and 65535", s)
			}
		}
		return fmt.Errorf("%q is not an IP address: write it like 10.0.0.5 or 2001:db8::1, and with a port like 10.0.0.5:8443 or [2001:db8::1]:8443", s)
	}
	if !hostRE.MatchString(s) {
		return fmt.Errorf("%q is not a host or IP address: use a name like docs.example.com or *.example.com, or an address like 10.0.0.5, optionally with :port", s)
	}
	name, port, hasPort := strings.Cut(s, ":")
	if n, _ := strconv.Atoi(port); hasPort && (n < 1 || n > 65535) {
		return fmt.Errorf("%q: the port must be between 1 and 65535", s)
	}
	if suffix, ok := strings.CutPrefix(name, "*."); ok && !strings.Contains(suffix, ".") {
		return fmt.Errorf("%q would allow every host under .%s; name the domain, such as *.example.%s", s, suffix, suffix)
	}
	return nil
}

// Validate checks the profile on its own, without looking at the machine.
// Path safety is checked later, on real paths, when the sandbox is planned.
func (p Profile) Validate(program string) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if p.Version != Version {
		add("version %d is not supported (want %d)", p.Version, Version)
	}
	if !ValidName(p.Program) {
		add("program %q is not a valid name", p.Program)
	} else if program != "" && p.Program != program {
		add("program %q doesn't match %q", p.Program, program)
	}
	if p.Workdir.Mode != "rw" && p.Workdir.Mode != "ro" {
		add("workdir.mode must be \"rw\" or \"ro\", not %q", p.Workdir.Mode)
	}
	if p.Network != NetOff && p.Network != NetRestricted && p.Network != NetOn {
		add("network must be \"off\", \"restricted\" or \"on\", not %q", p.Network)
	}
	listed := map[string]bool{}
	for _, id := range p.Allow.Groups {
		switch {
		case !slices.ContainsFunc(NetGroups(), func(g NetGroup) bool { return g.ID == id }):
			add("allow.groups: box has no group %q", id)
		case listed["group "+id]:
			add("allow.groups: %q is listed twice", id)
		}
		listed["group "+id] = true
	}
	for _, h := range p.Allow.Hosts {
		if err := CheckHost(h); err != nil {
			add("allow.hosts: %v", err)
		} else if listed[h] {
			add("allow.hosts: %q is listed twice", h)
		}
		listed[h] = true
	}

	for _, t := range p.Tools {
		if !ValidName(t) {
			add("tools: %q is not a command name", t)
		}
	}

	seen := map[string]string{}
	checkPaths := func(section string, paths []string) {
		for _, path := range paths {
			if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "/") {
				add("%s: %q must start with / or ~/", section, path)
				continue
			}
			if prev, ok := seen[path]; ok {
				add("%s: %q is already listed in %s", section, path, prev)
				continue
			}
			seen[path] = section
		}
	}
	checkPaths("home.rw", p.Home.RW)
	checkPaths("home.ro", p.Home.RO)
	checkPaths("extra.rw", p.Extra.RW)
	checkPaths("extra.ro", p.Extra.RO)
	checkPaths("system.extra_ro", p.System.ExtraRO)

	for _, k := range p.Env.Pass {
		if err := CheckEnvName(k); err != nil {
			add("env.pass: %v", err)
		}
	}
	for k := range p.Env.Set {
		if err := CheckEnvName(k); err != nil {
			add("env.set: %v", err)
		}
	}
	return errors.Join(errs...)
}
