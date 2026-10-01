package cli

import (
	"bufio"
	"bytes"
	"cmp"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asx8678/box/internal/egress"
	"github.com/asx8678/box/internal/host"
	"github.com/asx8678/box/internal/profile"
	"github.com/asx8678/box/internal/sandbox"
	"github.com/asx8678/box/internal/tui"
)

// egressSocket is where this run's proxy socket goes: a folder of box's own
// in the runtime folder, or in /tmp when there is none. It is known before
// the socket exists, so the dry run can show it.
func egressSocket(h host.Host) string {
	dir := filepath.Join(os.TempDir(), "box-"+strconv.Itoa(os.Getuid()))
	if rt := h.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(rt) {
		dir = filepath.Join(rt, "box")
	}
	return filepath.Join(dir, fmt.Sprintf("egress-%d.sock", os.Getpid()))
}

// proxied runs a restricted plan: box's proxy on the socket, bwrap as a
// child, then a word about anything the proxy blocked.
func (r *runner) proxied(plan *sandbox.Plan, name string, stderr io.Writer) (int, error) {
	dir := filepath.Dir(r.egress)
	// The folder must be box's alone: whoever controls it could put their
	// own socket, and so their own proxy, in front of the box.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ExitBox, err
	}
	if err := profile.CheckPrivateDir(dir); err != nil {
		return ExitBox, err
	}
	os.Remove(r.egress)
	l, err := net.Listen("unix", r.egress)
	if err != nil {
		return ExitBox, fmt.Errorf("the network proxy: %w", err)
	}
	defer os.Remove(r.egress)
	defer l.Close()

	var entries []egress.Entry
	for _, set := range plan.Allowed {
		for _, h := range set.Hosts {
			entries = append(entries, egress.Entry{Host: h, Trusted: set.Custom})
		}
	}
	policy, err := egress.NewPolicy(entries)
	if err != nil {
		return ExitBox, err
	}
	nl := openNetLog(r.dirs, r.prog.Name+"/"+name)
	defer nl.close()
	go (&egress.Server{Policy: policy, Log: nl.write}).Serve(l)

	code, err := sandbox.Spawn(r.probe.Bwrap, plan)
	if b := nl.blockedSummary(); b != "" {
		fmt.Fprintf(stderr, "box: the restricted network blocked %s\n"+
			"     to allow one: box -e %s (or add it under [allow] hosts in %s)\n",
			b, r.prog.Name, profile.Path(r.dirs, r.prog.Name, name))
	}
	return code, err
}

// netLog appends every connection the proxy decided to net.log in box's
// state folder, and counts what it blocked in this run.
type netLog struct {
	mu      sync.Mutex
	f       *os.File
	who     string
	blocked map[string]int
}

const netLogMax = 1 << 20 // then it moves to net.log.1, replacing the older one

func netLogPath(d profile.Dirs) string { return filepath.Join(d.State, "net.log") }

func openNetLog(d profile.Dirs, who string) *netLog {
	l := &netLog{who: who, blocked: map[string]int{}}
	path := netLogPath(d)
	if fi, err := os.Stat(path); err == nil && fi.Size() > netLogMax {
		os.Rename(path, path+".1")
	}
	if os.MkdirAll(d.State, 0o700) == nil {
		// A log: box runs without it if it can't be written.
		l.f, _ = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	}
	return l
}

func (l *netLog) write(e egress.Event) {
	// The name comes from the program: escape anything that could break a
	// line of the log or drive the terminal that shows it.
	verdict, target := "allowed", net.JoinHostPort(tui.Printable(e.Host), strconv.Itoa(int(e.Port)))
	if !e.Allowed {
		verdict = "blocked"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !e.Allowed {
		l.blocked[target]++
	}
	if l.f != nil {
		fmt.Fprintf(l.f, "%s\t%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), l.who, verdict, target, tui.Printable(e.Reason))
	}
}

// blockedSummary names what was blocked, the most often first: "3
// connections: a.example.com:443 (2), b.example.com:443".
func (l *netLog) blockedSummary() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.blocked) == 0 {
		return ""
	}
	var targets []string
	total := 0
	for t, n := range l.blocked {
		targets, total = append(targets, t), total+n
	}
	slices.SortFunc(targets, func(a, b string) int { return cmp.Or(l.blocked[b]-l.blocked[a], strings.Compare(a, b)) })
	var parts []string
	for i, t := range targets {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(targets)-5))
			break
		}
		if n := l.blocked[t]; n > 1 {
			t += fmt.Sprintf(" (%d)", n)
		}
		parts = append(parts, t)
	}
	noun := "connections"
	if total == 1 {
		noun = "connection"
	}
	return fmt.Sprintf("%d %s: %s", total, noun, strings.Join(parts, ", "))
}

func (l *netLog) close() {
	if l.f != nil {
		l.f.Close()
	}
}

// logLine is one line of net.log.
type logLine struct {
	time, who, verdict, target, reason string
}

func readNetLog(d profile.Dirs) []logLine {
	var lines []logLine
	for _, path := range []string{netLogPath(d) + ".1", netLogPath(d)} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			f := strings.SplitN(sc.Text(), "\t", 5)
			if len(f) == 5 {
				lines = append(lines, logLine{f[0], f[1], f[2], f[3], f[4]})
			}
		}
	}
	return lines
}

// recentlyBlocked lists, newest first, what program's runs had blocked and
// p still doesn't allow, as entries for allow.hosts: the port only when it
// isn't 80 or 443.
func recentlyBlocked(d profile.Dirs, program string, p profile.Profile) []string {
	var entries []egress.Entry
	for _, set := range p.Allowed() {
		for _, h := range set.Hosts {
			entries = append(entries, egress.Entry{Host: h})
		}
	}
	policy, err := egress.NewPolicy(entries)
	if err != nil {
		return nil
	}
	lines := readNetLog(d)
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < 8; i-- {
		l := lines[i]
		if l.verdict != "blocked" || !strings.HasPrefix(l.who, program+"/") {
			continue
		}
		host, portStr, err := net.SplitHostPort(l.target)
		port, perr := strconv.Atoi(portStr)
		if err != nil || perr != nil || policy.Allows(host, uint16(port)) {
			continue
		}
		entry := l.target
		if port == 80 || port == 443 {
			entry = host
		}
		if profile.CheckHost(entry) == nil && !slices.Contains(out, entry) {
			out = append(out, entry)
		}
	}
	return out
}

// showNetLog prints the end of net.log.
func showNetLog(d profile.Dirs, stdout io.Writer) int {
	lines := readNetLog(d)
	if len(lines) == 0 {
		fmt.Fprintln(stdout, "no network log yet: it fills while a profile with network = \"restricted\" runs")
		return 0
	}
	for _, l := range lines[max(0, len(lines)-50):] {
		fmt.Fprintf(stdout, "%s  %-24s %-8s %s  %s\n", l.time, l.who, l.verdict, l.target, l.reason)
	}
	return 0
}
