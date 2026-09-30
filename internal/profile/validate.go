package profile

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,63}$`)
	envRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
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
