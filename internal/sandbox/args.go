package sandbox

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/asx8678/box/internal/profile"
)

// Args is bwrap's argument list, without argv[0] and without the
// environment: Exec passes that through --args, so the values (API keys)
// stay out of the command line every user on the machine can read.
func (p *Plan) Args() []string {
	return slices.Concat(p.ops(p.InfoFD, p.SeccompFD, false)...)
}

// EnvArgs are the --setenv operations that give the program its whole
// environment, as bwrap's --args reads them: each argument ends in a NUL.
func (p *Plan) EnvArgs() []byte {
	var b []byte
	for _, op := range p.envOps() {
		for _, w := range op {
			b = append(append(b, w...), 0)
		}
	}
	return b
}

func (p *Plan) envOps() [][]string {
	var ops [][]string
	for _, kv := range p.Env {
		k, v, _ := strings.Cut(kv, "=")
		ops = append(ops, []string{"--setenv", k, v})
	}
	return ops
}

// ops is bwrap's argument list as one operation per entry, with the
// descriptors that carry the info file and the seccomp filter, and the
// environment when withEnv is set.
func (p *Plan) ops(infoFD, seccompFD int, withEnv bool) [][]string {
	ops := [][]string{p.Flags}
	if withEnv {
		ops = append(ops, p.envOps()...)
	}
	if p.Filter.Any() {
		ops = append(ops, []string{"--seccomp", strconv.Itoa(seccompFD)})
	}
	for _, m := range p.Mounts {
		ops = append(ops, m.args(infoFD))
	}
	ops = append(ops, []string{"--chdir", p.Chdir})
	return append(ops, append([]string{"--"}, p.Command...))
}

// kindFlags are bwrap's option for each Kind, in Kind's order.
var kindFlags = [...]string{"--ro-bind", "--bind", "--ro-bind-try", "--symlink", "--proc", "--dev", "--tmpfs", "--ro-bind-data"}

func (m Mount) args(fd int) []string {
	flag := kindFlags[m.Kind] // panics on an unknown kind
	switch m.Kind {
	case Proc, Dev, Tmpfs:
		return []string{flag, m.Dest}
	case ROBindData:
		return []string{flag, strconv.Itoa(fd), m.Dest}
	}
	return []string{flag, m.Src, m.Dest}
}

// DryRun renders the plan as a shell command that can be pasted and run:
// bwrap with an empty environment, as box runs it, and one operation per
// line, the program's environment included. Variables passed from the host
// show as "$NAME", which the shell fills in, so no API key is printed. The
// info file on fd 3 is supplied with a bash here-string.
func (p *Plan) DryRun(bwrap string) string {
	var b strings.Builder
	b.WriteString("env -i " + Quote(bwrap))
	for _, op := range p.ops(DryRunInfoFD, DryRunSeccompFD, true) {
		b.WriteString(" \\\n  ")
		if op[0] == "--setenv" && slices.Contains(p.Passed, op[1]) {
			fmt.Fprintf(&b, `--setenv %s "$%s"`, op[1], op[1])
			continue
		}
		for i, w := range op {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(Quote(w))
		}
	}
	b.WriteString(" \\\n  " + strconv.Itoa(DryRunInfoFD) + "<<<")
	b.WriteString(Quote(strings.TrimSuffix(string(p.Info), "\n")))
	b.WriteByte('\n')
	if p.Filter.Any() {
		var blocks []string
		if p.Filter.TIOCSTI {
			blocks = append(blocks, "terminal injection (TIOCSTI, TIOCLINUX)")
		}
		if p.Filter.Vsock {
			blocks = append(blocks, "VM sockets to the Windows host (AF_VSOCK)")
		}
		fmt.Fprintf(&b, "# fd %d: box's seccomp filter, blocking %s; box passes it at run time,\n"+
			"# so pasting this command needs that fd or the --seccomp line removed.\n",
			DryRunSeccompFD, strings.Join(blocks, " and "))
	}
	for _, n := range p.Notes {
		b.WriteString("# Note: " + n + ".\n")
	}
	if p.Landlock {
		b.WriteString("# box also applies a Landlock scope blocking abstract Unix sockets outside the box.\n")
	}
	if p.Network == profile.NetRestricted {
		b.WriteString("# Network: restricted. The box has no network of its own. box runs a proxy on the\n" +
			"# socket above while the program runs, and only these hosts are reachable through it:\n")
		for _, set := range p.Allowed {
			fmt.Fprintf(&b, "#   %s: %s\n", set.Name, strings.Join(set.Hosts, ", "))
		}
		if len(p.Allowed) == 0 {
			b.WriteString("#   nothing: the profile allows no hosts\n")
		}
		b.WriteString("# So pasting this command gives a box with no network at all.\n")
	}
	return b.String()
}

// Quote makes s a single POSIX shell word.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("_@%+=:,./-", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
