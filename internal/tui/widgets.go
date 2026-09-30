package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The screen is drawn as lines of segments. A segment with an ID is a
// widget: it takes keyboard focus in the order drawn, and becomes a Lip
// Gloss layer so a mouse click or hover maps back to it through
// Compositor.Hit. Several segments may share an ID (a checkbox and its
// label); they then act as one widget.

type seg struct {
	text string // styled
	id   string // "" for plain text
	ptr  bool   // the row's gutter: shows ❯ when the row holds the focus
}

type line []seg

// page is what every screen shares: the terminal's size and theme, and
// the widget under the mouse, found through the compositor of the last draw.
type page struct {
	width, height int
	hover         string
	hits          *lipgloss.Compositor
}

// update handles the messages every screen answers the same way and
// reports whether msg was one of them.
func (p *page) update(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		applyTheme(msg.IsDark())
	case tea.MouseMotionMsg:
		p.hover = p.hitID(msg.Mouse())
	default:
		return false
	}
	return true
}

// hitID is the widget at a mouse position, or "" outside any.
func (p *page) hitID(m tea.Mouse) string {
	if p.hits == nil {
		return ""
	}
	return p.hits.Hit(m.X, m.Y).ID()
}

// clicked is the widget under a left click, or "".
func (p *page) clicked(msg tea.MouseClickMsg) string {
	if msg.Mouse().Button != tea.MouseLeft {
		return ""
	}
	return p.hitID(msg.Mouse())
}

// draw renders ls, records where every widget is, and returns it as a
// full-screen view with the mouse enabled, motion included, for hover.
func (p *page) draw(ls []line, title string) tea.View {
	var content string
	content, p.hits = render(ls)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion
	v.WindowTitle = title
	return v
}

// The look follows Claude Code: one warm accent for the brand, the focus
// and the primary action, quiet grey for everything secondary, colour only
// where it carries meaning. There are two palettes; applyTheme picks the
// one that matches the terminal's background.
var (
	styleAccent, styleBrand, styleSection, styleBold, styleDim, styleFaint lipgloss.Style
	styleError, styleWarn, styleOK, stylePlain, styleFocus, styleCheck     lipgloss.Style
	styleRW                                                                lipgloss.Style

	// Filled widgets: buttons, segmented toggles and pills.
	btnNormal, btnHover, btnFocus                lipgloss.Style
	btnPrimary, btnPrimaryHover, btnPrimaryFocus lipgloss.Style
	btnDanger, btnDangerHover, btnDangerFocus    lipgloss.Style
	segOn, segOff, segOffHover, pillRW, pillRO   lipgloss.Style
)

func init() { applyTheme(true) }

// applyTheme sets every style for a dark or light terminal background.
func applyTheme(dark bool) {
	pick := func(darkHex, lightHex string) color.Color {
		if dark {
			return lipgloss.Color(darkHex)
		}
		return lipgloss.Color(lightHex)
	}
	fg := func(d, l string) lipgloss.Style { return lipgloss.NewStyle().Foreground(pick(d, l)) }
	fill := func(bgD, bgL, fgD, fgL string) lipgloss.Style {
		return lipgloss.NewStyle().Background(pick(bgD, bgL)).Foreground(pick(fgD, fgL))
	}

	styleAccent = fg("#D77757", "#C15F3C") // Claude orange
	styleBrand = styleAccent.Bold(true)
	styleSection = fg("#E8A48A", "#A84F30").Bold(true)
	styleDim = fg("#8A8A8A", "#6B6B6B")
	styleFaint = fg("#5C5C5C", "#A8A8A8")
	styleError = fg("#FF6B80", "#D12F4B")
	styleWarn = fg("#E5B454", "#B7791F")
	styleOK = fg("#4EBA65", "#2E8B4A")
	styleRW = styleOK
	styleCheck = styleOK.Bold(true)
	styleBold = lipgloss.NewStyle().Bold(true)
	stylePlain = lipgloss.NewStyle()
	styleFocus = styleAccent.Bold(true)

	btnNormal = fill("#353535", "#E4E4E4", "#E6E6E6", "#1F1F1F")
	btnHover = fill("#4A4A4A", "#D2D2D2", "#FFFFFF", "#000000")
	btnFocus = fill("#D77757", "#C15F3C", "#1A1A1A", "#FFFFFF").Bold(true)
	btnPrimary = fill("#D77757", "#C15F3C", "#1A1A1A", "#FFFFFF").Bold(true)
	btnPrimaryHover = fill("#E48B6C", "#D47250", "#1A1A1A", "#FFFFFF").Bold(true)
	btnPrimaryFocus = fill("#F4AE92", "#9E4526", "#1A1A1A", "#FFFFFF").Bold(true).Underline(true)
	btnDanger = fill("#7A2A38", "#F6D5DB", "#FFD7DE", "#A0213A")
	btnDangerHover = fill("#94334A", "#EFC0C9", "#FFFFFF", "#7E1A2E")
	btnDangerFocus = fill("#FF6B80", "#D12F4B", "#1A1A1A", "#FFFFFF").Bold(true)

	segOn = fill("#D77757", "#C15F3C", "#1A1A1A", "#FFFFFF").Bold(true)
	segOff = fill("#2E2E2E", "#E9E9E9", "#8A8A8A", "#6B6B6B")
	segOffHover = fill("#404040", "#D8D8D8", "#E6E6E6", "#1F1F1F")
	pillRW = fill("#1E4A2A", "#D6F2DD", "#86E29A", "#1F6B37")
	pillRO = fill("#26335F", "#DDE4FF", "#B4C3FF", "#2F47B8")
}

func txt(s string) seg                       { return seg{text: s} }
func styled(st lipgloss.Style, s string) seg { return seg{text: st.Render(s)} }

// gutter starts a row that can show the ❯ pointer.
func gutter() seg { return seg{text: "    ", ptr: true} }

// kind is a button's role.
type kind int

const (
	secondary kind = iota
	primary
	danger
)

// btn renders a filled button: its role sets the colour, and focus and
// hover change it, the way native buttons do.
func btn(label string, k kind, focused, hovered bool) string {
	st := map[kind][3]lipgloss.Style{
		secondary: {btnNormal, btnHover, btnFocus},
		primary:   {btnPrimary, btnPrimaryHover, btnPrimaryFocus},
		danger:    {btnDanger, btnDangerHover, btnDangerFocus},
	}[k]
	switch {
	case focused:
		return st[2].Render("  " + label + "  ")
	case hovered:
		return st[1].Render("  " + label + "  ")
	}
	return st[0].Render("  " + label + "  ")
}

func (e *editor) focused(id string) bool { return e.focus == id && e.inputKind == "" }

// widget renders label as a focusable, clickable text widget in style st:
// in the accent colour when focused, underlined under the mouse.
func (e *editor) widget(id, label string, st lipgloss.Style) seg {
	switch {
	case e.focused(id):
		st = styleFocus
	case e.hover == id:
		st = st.Underline(true)
	}
	return seg{text: st.Render(label), id: id}
}

// button is a filled button widget.
func (e *editor) button(id, label string, k kind) seg {
	return seg{text: btn(label, k, e.focused(id), e.hover == id), id: id}
}

// segment is one half of a segmented toggle: filled when chosen, and
// underlined when it has the keyboard focus.
func (e *editor) segment(id, label string, on bool) seg {
	st := segOff
	switch {
	case on:
		st = segOn
	case e.hover == id:
		st = segOffHover
	}
	if e.focused(id) {
		st = st.Underline(true).Bold(true)
	}
	return seg{text: st.Render(" " + label + " "), id: id}
}

// pill is a folder's access mode as a tinted, clickable chip.
func (e *editor) pill(id string, rw bool) seg {
	label, st := " read-only ▾  ", pillRO
	if rw {
		label, st = " read-write ▾ ", pillRW
	}
	switch {
	case e.focused(id):
		st = btnFocus
	case e.hover == id:
		st = st.Underline(true)
	}
	return seg{text: st.Render(label), id: id}
}

// checkbox is a tick box with a green ✓, then its label; both parts answer
// to the same click and hover.
func (e *editor) checkbox(id string, on bool, label string) []seg {
	box := styleFaint.Render("[") + " " + styleFaint.Render("]")
	st := styleDim
	if on {
		box, st = styleFaint.Render("[")+styleCheck.Render("✓")+styleFaint.Render("]"), stylePlain
	}
	return []seg{{text: box + " ", id: id}, e.widget(id, label, st)}
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

// shorten keeps the end of a path, which says the most, within n columns.
func shorten(s string, n int) string {
	r := []rune(s)
	if n < 4 || len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}

// wrap breaks text into lines of at most w columns, at spaces, in style st.
func wrap(text string, st lipgloss.Style, w int) []line {
	var groups [][]seg
	for _, word := range strings.Fields(text) {
		groups = append(groups, []seg{styled(st, word+" ")})
	}
	return flow(groups, w)
}

// indent prefixes every line with n spaces.
func indent(ls []line, n int) []line {
	for i := range ls {
		ls[i] = append(line{txt(strings.Repeat(" ", n))}, ls[i]...)
	}
	return ls
}

// rightAlign pads l on the left so it ends at column w.
func rightAlign(l line, w int) line {
	if gap := w - width(l); gap > 0 {
		return append(line{txt(strings.Repeat(" ", gap))}, l...)
	}
	return l
}

// card draws body inside a rounded box w columns wide, like Claude Code's
// welcome box. border colours the frame.
func card(body []line, w int, border lipgloss.Style) []line {
	inner := w - 4
	out := []line{{styled(border, "╭"+strings.Repeat("─", w-2)+"╮")}}
	for _, l := range body {
		// A line too wide for the box (a text input showing a long
		// completion) loses the end of its last segment, not the border.
		if over := width(l) - inner; over > 0 && len(l) > 0 {
			last := &l[len(l)-1]
			last.text = ansi.Truncate(last.text, max(0, lipgloss.Width(last.text)-over), "")
		}
		row := append(line{styled(border, "│ ")}, l...)
		if gap := inner - width(l); gap > 0 {
			row = append(row, txt(strings.Repeat(" ", gap)))
		}
		out = append(out, append(row, styled(border, " │")))
	}
	return append(out, line{styled(border, "╰"+strings.Repeat("─", w-2)+"╯")})
}

// rule is a faint full-width separator.
func rule(w int) line { return line{styled(styleFaint, " "+strings.Repeat("─", w-2))} }

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
		g := []seg{styled(styleBold, pairs[i]), styled(styleDim, " "+pairs[i+1])}
		if i+2 < len(pairs) {
			g = append(g, styled(styleFaint, " · "))
		}
		groups = append(groups, g)
	}
	return indent(flow(groups, w-2), 1)
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

// render draws lines and records every widget's position.
func render(lines []line) (string, *lipgloss.Compositor) {
	var b strings.Builder
	var layers []*lipgloss.Layer
	for y, l := range lines {
		x := 0
		for _, s := range l {
			if s.id != "" {
				layers = append(layers, lipgloss.NewLayer(s.text).X(x).Y(y).ID(s.id))
			}
			b.WriteString(s.text)
			x += lipgloss.Width(s.text)
		}
		if y < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String(), lipgloss.NewCompositor(layers...)
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

// layoutWidth is the width to lay out for: the terminal, capped so lines
// stay readable on very wide terminals.
func layoutWidth(termWidth int) int {
	if termWidth <= 0 {
		return 80
	}
	return min(max(termWidth, 50), 100)
}
