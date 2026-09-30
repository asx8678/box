package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// The screen is drawn as lines of segments. A segment with an ID is a
// widget: it takes keyboard focus in the order drawn, and becomes a Lip
// Gloss layer so a mouse click maps back to it through Compositor.Hit.

type seg struct {
	text string // styled
	id   string // "" for plain text
}

type line []seg

var (
	styleTitle   = lipgloss.NewStyle().Bold(true)
	styleSection = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleError   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleFocus   = lipgloss.NewStyle().Reverse(true)
	styleSave    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
)

func txt(s string) seg                       { return seg{text: s} }
func styled(st lipgloss.Style, s string) seg { return seg{text: st.Render(s)} }

// widget renders label as a focusable, clickable segment.
func (e *editor) widget(id, label string) seg {
	if e.focus == id && e.inputKind == "" {
		return seg{text: styleFocus.Render(label), id: id}
	}
	return seg{text: label, id: id}
}

func check(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}

func radio(on bool) string {
	if on {
		return "(•)"
	}
	return "( )"
}

func mode(rw bool) string {
	if rw {
		return "rw ▾"
	}
	return "ro ▾"
}

func width(l line) int {
	w := 0
	for _, s := range l {
		w += lipgloss.Width(s.text)
	}
	return w
}

// flow lays out segments left to right, wrapping at max columns; each
// group stays on one line. indent starts every line.
func flow(indent string, groups [][]seg, max int) []line {
	var out []line
	cur := line{txt(indent)}
	for _, g := range groups {
		gw := width(line(g))
		if width(cur) > len(indent) && width(cur)+gw > max {
			out = append(out, cur)
			cur = line{txt(indent)}
		}
		cur = append(cur, g...)
	}
	if width(cur) > len(indent) {
		out = append(out, cur)
	}
	return out
}

func pad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// render draws lines[top:top+height] and records every widget's position.
func render(lines []line, top, height int) (string, *lipgloss.Compositor) {
	end := len(lines)
	if height > 0 && top+height < end {
		end = top + height
	}
	var b strings.Builder
	var layers []*lipgloss.Layer
	for y, l := range lines[top:end] {
		x := 0
		for _, s := range l {
			if s.id != "" {
				layers = append(layers, lipgloss.NewLayer(s.text).X(x).Y(y).ID(s.id))
			}
			b.WriteString(s.text)
			x += lipgloss.Width(s.text)
		}
		if y < end-top-1 {
			b.WriteByte('\n')
		}
	}
	return b.String(), lipgloss.NewCompositor(layers...)
}
