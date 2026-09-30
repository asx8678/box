package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The screen is drawn as lines of segments. A segment with an ID is a
// widget: it takes keyboard focus in the order drawn, and becomes a Lip
// Gloss layer so a mouse click maps back to it through Compositor.Hit.

type seg struct {
	text string // styled
	id   string // "" for plain text
	ptr  bool   // the row's gutter: shows ❯ when the row holds the focus
}

type line []seg

// The look follows Claude Code: one warm accent for the brand and the
// selection, quiet grey for everything secondary, colour only where it
// carries meaning. There are two palettes; applyTheme picks the one that
// matches the terminal's background, so box looks at home on either.
var (
	styleAccent, styleBrand, styleSection, styleBold, styleDim, styleFaint lipgloss.Style
	styleError, styleWarn, stylePlain, styleFocus, styleHover              lipgloss.Style
	styleRW, styleRO, stylePrimary, styleCheck                             lipgloss.Style
)

func init() { applyTheme(true) }

// applyTheme sets every style for a dark or light terminal background.
func applyTheme(dark bool) {
	c := func(darkHex, lightHex string) lipgloss.Style {
		if dark {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(darkHex))
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(lightHex))
	}
	styleAccent = c("#D77757", "#C15F3C") // Claude orange
	styleBrand = styleAccent.Bold(true)
	styleSection = c("#E8A48A", "#A84F30").Bold(true)
	styleDim = c("#8A8A8A", "#6B6B6B")
	styleFaint = c("#5C5C5C", "#A8A8A8")
	styleError = c("#FF6B80", "#D12F4B")
	styleWarn = c("#E5B454", "#B7791F")
	styleRW = c("#4EBA65", "#2E8B4A")
	styleRO = c("#8FA6F9", "#3D5BD9")
	styleCheck = c("#4EBA65", "#2E8B4A").Bold(true)
	styleBold = lipgloss.NewStyle().Bold(true)
	stylePlain = lipgloss.NewStyle()
	stylePrimary = lipgloss.NewStyle().Bold(true)
	styleFocus = styleAccent.Bold(true)
	styleHover = lipgloss.NewStyle().Underline(true)
}

func txt(s string) seg                       { return seg{text: s} }
func styled(st lipgloss.Style, s string) seg { return seg{text: st.Render(s)} }

// gutter starts a row that can show the ❯ pointer.
func gutter() seg { return seg{text: "    ", ptr: true} }

// widget renders label as a focusable, clickable segment in style st, or
// in the accent colour when it has the focus.
func (e *editor) widget(id, label string, st lipgloss.Style) seg {
	switch {
	case e.focus == id && e.inputKind == "":
		st = styleFocus
	case e.hover == id:
		st = st.Inherit(styleHover)
	}
	return seg{text: st.Render(label), id: id}
}

// checkbox is a tick box with a green ✓, then its label as a widget; both
// parts answer to the same click.
func (e *editor) checkbox(id string, on bool, label string) []seg {
	box := styleFaint.Render("[") + " " + styleFaint.Render("]")
	st := styleDim
	if on {
		box, st = styleFaint.Render("[")+styleCheck.Render("✓")+styleFaint.Render("]"), stylePlain
	}
	return []seg{{text: box + " ", id: id}, e.widget(id, label, st)}
}

func radio(on bool) string {
	if on {
		return "●"
	}
	return "○"
}

// mode is the access label for a folder and its colour.
func mode(rw bool) (string, lipgloss.Style) {
	if rw {
		return "read-write ▾", styleRW
	}
	return "read-only  ▾", styleRO
}

func width(l line) int {
	w := 0
	for _, s := range l {
		w += lipgloss.Width(s.text)
	}
	return w
}

// flow lays out segment groups left to right, wrapping at max columns;
// each group stays on one line.
func flow(groups [][]seg, max int) []line {
	var out []line
	var cur line
	for _, g := range groups {
		if len(cur) > 0 && width(cur)+width(line(g)) > max {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, g...)
	}
	if len(cur) > 0 {
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

// wrap breaks text into lines of at most w columns, at spaces, in style st.
func wrap(text string, st lipgloss.Style, w int) []line {
	var groups [][]seg
	for _, word := range strings.Fields(text) {
		groups = append(groups, []seg{styled(st, word+" ")})
	}
	return flow(groups, w)
}

// card draws body inside a rounded box w columns wide, like Claude Code's
// welcome box. border colours the frame.
func card(body []line, w int, border lipgloss.Style) []line {
	inner := w - 4
	out := []line{{styled(border, "╭"+strings.Repeat("─", w-2)+"╮")}}
	for _, l := range body {
		row := append(line{styled(border, "│ ")}, l...)
		if gap := inner - width(l); gap > 0 {
			row = append(row, txt(strings.Repeat(" ", gap)))
		}
		out = append(out, append(row, styled(border, " │")))
	}
	return append(out, line{styled(border, "╰"+strings.Repeat("─", w-2)+"╯")})
}

// section is a coloured title with an optional grey note, above its rows.
func section(title, note string) line {
	l := line{txt(" "), styled(styleSection, "◆ "+title)}
	if note != "" {
		l = append(l, styled(styleDim, "  "+note))
	}
	return l
}

// hints is the grey footer: pairs of key and what it does, wrapped to w.
func hints(w int, pairs ...string) []line {
	var groups [][]seg
	for i := 0; i+1 < len(pairs); i += 2 {
		g := []seg{styled(styleDim, pairs[i]+" "+pairs[i+1])}
		if i+2 < len(pairs) {
			g = append(g, styled(styleFaint, " · "))
		}
		groups = append(groups, g)
	}
	lines := flow(groups, w-2)
	for i := range lines {
		lines[i] = append(line{txt(" ")}, lines[i]...)
	}
	return lines
}

// pointAt puts the ❯ pointer in the gutter of the row that holds focus.
func pointAt(ls []line, focus string) {
	for _, l := range ls {
		for _, s := range l {
			if s.id != "" && s.id == focus {
				for i := range l {
					if l[i].ptr {
						l[i].text = "  " + styleAccent.Render("❯") + " "
					}
				}
			}
		}
	}
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

// screen wraps content as a full-screen view with mouse clicks enabled.
func screen(content, title string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion // motion too, for hover
	v.WindowTitle = title
	return v
}

// banner is the orange header card: the ✻ box mark with a title, and an
// optional second line of status.
func banner(title string, sub line, w int) []line {
	body := []line{{styled(styleBrand, "✻ box"), styled(styleFaint, "  │  "), txt(title)}}
	if len(sub) > 0 {
		body = append(body, append(line{txt("  ")}, sub...))
	}
	return card(body, w, styleAccent)
}

// chip is a coloured status word with a dot, for the banner.
func chip(on bool, onText, offText string, onSt lipgloss.Style) []seg {
	if on {
		return []seg{styled(onSt, "● "+onText)}
	}
	return []seg{styled(styleDim, "○ "+offText)}
}

// menu renders Claude Code-style numbered options: "❯ 1. label" for the
// selected one, in the accent colour, and "  2. label" for the rest.
func menu(labels, details []string, selected int, idPrefix string, w int) []line {
	var out []line
	digits := len(strconv.Itoa(len(labels)))
	for i, label := range labels {
		num := fmt.Sprintf("%*d. ", digits, i+1)
		ptr, st := "   ", stylePlain
		if i == selected {
			ptr, st = " "+styleAccent.Render("❯")+" ", styleFocus
		}
		row := line{txt(ptr), seg{text: st.Render(num + label), id: idPrefix + strconv.Itoa(i)}}
		if i < len(details) && details[i] != "" {
			room := w - width(row) - 4
			d := details[i]
			if r := []rune(d); room > 10 && len(r) > room {
				d = string(r[:room-1]) + "…"
			}
			row = append(row, styled(styleDim, "   "+d))
		}
		out = append(out, row)
	}
	return out
}
