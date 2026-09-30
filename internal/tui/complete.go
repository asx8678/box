package tui

import (
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
