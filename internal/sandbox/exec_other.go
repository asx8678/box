//go:build !linux

package sandbox

// RunProbe reports that bubblewrap can't run here. --dry-run still works.
func RunProbe(stateDir string, fresh bool) (Probe, error) {
	return Probe{}, ErrNeedsLinux
}

// Exec always fails outside Linux; box never runs a program unsandboxed.
func Exec(bwrap string, plan *Plan) error {
	return ErrNeedsLinux
}
