package sandbox

import (
	"strconv"
	"strings"
)

// Args is bwrap's argument list, without argv[0].
func (p *Plan) Args() []string {
	args := append([]string(nil), p.Flags...)
	for _, m := range p.Mounts {
		args = append(args, m.args()...)
	}
	args = append(args, "--chdir", p.Chdir, "--")
	return append(args, p.Command...)
}

func (m Mount) args() []string {
	switch m.Kind {
	case ROBind:
		return []string{"--ro-bind", m.Src, m.Dest}
	case Bind:
		return []string{"--bind", m.Src, m.Dest}
	case ROBindTry:
		return []string{"--ro-bind-try", m.Src, m.Dest}
	case Symlink:
		return []string{"--symlink", m.Src, m.Dest}
	case Proc:
		return []string{"--proc", m.Dest}
	case Dev:
		return []string{"--dev", m.Dest}
	case Tmpfs:
		return []string{"--tmpfs", m.Dest}
	case ROBindData:
		return []string{"--ro-bind-data", strconv.Itoa(InfoFD), m.Dest}
	}
	panic("sandbox: unknown mount kind")
}

// DryRun renders the plan as a shell command that can be pasted and run:
// env -i with the exact environment, then bwrap with one operation per line.
// The info file on fd 3 is supplied with a bash here-string.
func (p *Plan) DryRun(bwrap string) string {
	var b strings.Builder
	b.WriteString("env -i")
	for _, kv := range p.Env {
		b.WriteString(" \\\n  ")
		b.WriteString(Quote(kv))
	}
	b.WriteString(" \\\n")
	b.WriteString(Quote(bwrap))
	line := func(words ...string) {
		b.WriteString(" \\\n  ")
		for i, w := range words {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(Quote(w))
		}
	}
	line(p.Flags...)
	for _, m := range p.Mounts {
		line(m.args()...)
	}
	line("--chdir", p.Chdir)
	line(append([]string{"--"}, p.Command...)...)
	b.WriteString(" \\\n  3<<<")
	b.WriteString(Quote(strings.TrimSuffix(string(p.Info), "\n")))
	b.WriteByte('\n')
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
