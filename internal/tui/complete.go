package tui

import (
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirSuggestions completes the last component of raw ("~/co" or
// "/opt/x") to folders that exist, for the add-folder input. Hidden
// folders are offered only once the typed part starts with a dot.
func dirSuggestions(raw, home string) []string {
	i := strings.LastIndex(raw, "/")
	if i < 0 {
		return nil
	}
	dirRaw, prefix := raw[:i+1], raw[i+1:]
	dir := dirRaw
	if strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(home, dir[2:])
	}
	if !filepath.IsAbs(dir) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = fi.IsDir()
			}
		}
		if isDir {
			out = append(out, dirRaw+name+"/")
		}
		if len(out) == 100 {
			break
		}
	}
	sort.Strings(out)
	return out
}

// cleanHost tidies a typed or pasted host or IP address: lower case, of a
// pasted URL only the host (and port), and an address in its usual form.
func cleanHost(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s, _, _ = strings.Cut(rest, "/")
	}
	if bare, ok := strings.CutPrefix(s, "["); ok && strings.HasSuffix(bare, "]") {
		s = strings.TrimSuffix(bare, "]") // a URL's [IPv6] without a port
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.String()
	}
	return s
}

// cleanInput tidies a typed folder path: no trailing slash, "~" kept.
func cleanInput(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "~" || raw == "~/" {
		return "~"
	}
	if strings.HasPrefix(raw, "~/") {
		return "~/" + strings.TrimPrefix(filepath.Clean(raw[2:]), "/")
	}
	if raw == "" {
		return ""
	}
	return filepath.Clean(raw)
}
