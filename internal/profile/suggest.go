package profile

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/asx8678/box/internal/host"
)

// suggestBases are where programs usually keep their own state.
var suggestBases = []string{".", ".config/", ".local/share/", ".local/state/", ".cache/"}

// Suggest lists existing folders under home that are named after program,
// as "~/…" paths: ~/.<name>, ~/.config/<name>, ~/.local/share/<name>,
// ~/.local/state/<name> and ~/.cache/<name>, also trying the part of the
// name before the first "-" (kiro-cli → ~/.kiro). Paths the profile already
// mounts are left out. This works for any program; nothing here knows one.
func Suggest(h host.Host, home string, p Profile) []string {
	names := []string{p.Program}
	if i := strings.IndexByte(p.Program, '-'); i > 0 {
		names = append(names, p.Program[:i])
	}
	have := slices.Concat(p.Home.RW, p.Home.RO, p.Extra.RW, p.Extra.RO)
	var out []string
	for _, name := range names {
		for _, base := range suggestBases {
			rel := base + name
			if base == "." {
				rel = "." + name
			}
			fi, err := h.Stat(filepath.Join(home, rel))
			if err != nil || !fi.IsDir() {
				continue
			}
			path := "~/" + rel
			if !slices.Contains(have, path) && !slices.Contains(out, path) {
				out = append(out, path)
			}
		}
	}
	return out
}
