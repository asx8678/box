package tui

import (
	"io"
	"time"

	"charm.land/lipgloss/v2"
)

// After the editor or the picker, Launching holds the screen for
// LaunchSteps steps of LaunchStep each: long enough to read one line,
// short enough not to be in the way.
const (
	LaunchSteps = 3
	LaunchStep  = 600 * time.Millisecond
)

// Launching tells the user, on the normal screen, what is about to start
// in the sandbox. The summary line stays in the scrollback as a record of
// what ran and how. With a step above zero it then holds for LaunchSteps
// steps under a "launching" line: box's editor has just vanished, and
// without the hold the program would appear in its place with no word
// that it runs in a box. Only text is appended, with no cursor movement,
// so a narrow terminal can't garble it. Ctrl+C during the hold ends box
// before anything runs.
func Launching(w io.Writer, program, summary string, step time.Duration) {
	lipgloss.Fprintln(w, " "+styleBrand.Render("✻ box")+"  "+styleDim.Render(summary))
	if step <= 0 {
		return
	}
	lipgloss.Fprint(w, "        launching "+program+" in the sandbox")
	for range LaunchSteps {
		time.Sleep(step)
		lipgloss.Fprint(w, styleDim.Render(" ·"))
	}
	lipgloss.Fprintln(w)
}
