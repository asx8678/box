package sandbox

import (
	"errors"
	"strings"
)

// ErrNeedsLinux is returned where bubblewrap would be needed on another OS.
var ErrNeedsLinux = errors.New("box needs Linux: bubblewrap only runs there (on Windows, use WSL2)")

// Probe is what box learned about this machine before running anything.
type Probe struct {
	Bwrap          string `json:"bwrap"`          // trusted bwrap path
	Version        string `json:"version"`        // e.g. "0.9.0"
	Kernel         string `json:"kernel"`         // kernel release
	LegacyTIOCSTI  string `json:"legacy_tiocsti"` // "0", "1" or "" if the kernel has no switch
	WSL            bool   `json:"wsl"`
	AppArmorUserns string `json:"apparmor_userns"` // "1" if Ubuntu restricts user namespaces, "" if absent
	Nested         bool   `json:"-"`               // already inside box
	Cached         bool   `json:"-"`
}

// hint turns bwrap's error text from the namespace check into a fix.
func hint(stderr string, p Probe) string {
	switch {
	case strings.Contains(stderr, "setting up uid map: Permission denied"),
		strings.Contains(stderr, "Failed RTM_NEWADDR"):
		if p.AppArmorUserns == "1" {
			return "Ubuntu's AppArmor blocks user namespaces for bwrap. Either load the bwrap profile " +
				"(sudo apt install apparmor-profiles; sudo ln -s /usr/share/apparmor/extra-profiles/bwrap-userns-restrict " +
				"/etc/apparmor.d/ && sudo systemctl reload apparmor) or allow them for everyone: " +
				"sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0"
		}
		return "the kernel refused to map your user into a new namespace"
	case strings.Contains(stderr, "No permissions to create new namespace"),
		strings.Contains(stderr, "Creating new namespace failed"):
		return "unprivileged user namespaces are disabled; check sysctl user.max_user_namespaces " +
			"(and kernel.unprivileged_userns_clone on older Debian)"
	}
	return ""
}
