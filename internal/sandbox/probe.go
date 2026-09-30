package sandbox

import "errors"

// ErrNeedsLinux is returned where bubblewrap would be needed on another OS.
var ErrNeedsLinux = errors.New("box needs Linux: bubblewrap only runs there (on Windows, use WSL2)")

// Probe is what box learned about this machine before running anything.
type Probe struct {
	Bwrap          string // trusted bwrap path
	Version        string // e.g. "0.9.0"
	Kernel         string // kernel release
	LegacyTIOCSTI  string // "0", "1" or "" if the kernel has no switch
	WSL            bool
	AppArmorUserns string // "1" if Ubuntu restricts user namespaces, "" if absent
}
