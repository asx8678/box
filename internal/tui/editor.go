// Package tui is box's profile editor: one screen, mouse and keyboard.
// It is the only package that imports the Charm libraries.
package tui

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/asx8678/box/internal/profile"
)

// Options configure the editor. The callbacks keep sandbox logic out of
// the TUI: box's CLI supplies them.
type Options struct {
	Profile   profile.Profile
	Name      string   // profile name to start with
	New       bool     // creating a profile: the name must not exist yet
	Workdir   string   // shown in the header
	Home      string   // for displaying and completing "~/" paths
	Suggested []string // program folders to offer, unticked ("~/…")
	EnvHints  []string // variable names to offer, unticked
	// Exists reports whether a profile with this name is already saved.
	Exists func(name string) bool
	// CheckPath checks a folder about to be ticked or added: the safety
	// rules allow it in this mode, and it exists when mustExist is set
	// (the program's own folders are created on first run; extra ones aren't).
	CheckPath func(path string, rw, mustExist bool) error
	// Plan validates the whole profile against the machine and returns
	// the dry-run command to preview.
	Plan func(p profile.Profile, name string) (string, error)
}

// Result is what the user chose.
type Result struct {
	Profile profile.Profile
	Name    string
}

// ErrCancelled is returned by Edit when the user leaves without saving.
var ErrCancelled = errors.New("cancelled")

// Edit runs the editor on the terminal until the user saves or cancels.
func Edit(opts Options) (Result, error) {
	e := newEditor(opts)
	final, err := tea.NewProgram(e).Run()
	if err != nil {
		return Result{}, err
	}
	if r := final.(*editor).result; r != nil {
		return *r, nil
	}
	return Result{}, ErrCancelled
}

type item struct {
	path    string // as stored in the profile: "~/…" or absolute
	rw      bool
	on      bool
	extra   bool // an extra folder (removable), not one of the program's own
	suggest bool // offered by box, not yet in the profile
}

type envItem struct {
	name string
	on   bool
}

type editor struct {
	page
	opts     Options
	p        profile.Profile // settings the screen doesn't list item by item
	baseline profile.Profile // what the screen started with, for "unsaved changes"
	items    []item
	env      []envItem
	focus    string

	name      textinput.Model
	input     textinput.Model // the add-folder or add-variable input
	inputKind string          // "", "folder" or "env"
	inputErr  string

	msg      string // why Save or Preview failed
	note     string // a passing confirmation or warning, cleared by the next key
	noteWarn bool
	armed    bool // Esc was pressed once with unsaved changes

	preview   bool
	pvSave    bool // the preview's focus is on Save & run, not Back
	view      viewport.Model
	top       int     // first body line on screen
	lastFocus string  // the focus at the last draw, to scroll it into view once
	result    *Result // set when saved
}

func newEditor(opts Options) *editor {
	e := &editor{opts: opts, p: opts.Profile, focus: "save"}
	for _, path := range opts.Profile.Home.RW {
		e.items = append(e.items, item{path: path, rw: true, on: true})
	}
	for _, path := range opts.Profile.Home.RO {
		e.items = append(e.items, item{path: path, on: true})
	}
	for _, path := range opts.Suggested {
		e.items = append(e.items, item{path: path, rw: true, suggest: true})
	}
	for _, path := range opts.Profile.Extra.RW {
		e.items = append(e.items, item{path: path, rw: true, on: true, extra: true})
	}
	for _, path := range opts.Profile.Extra.RO {
		e.items = append(e.items, item{path: path, on: true, extra: true})
	}
	for _, name := range opts.Profile.Env.Pass {
		e.env = append(e.env, envItem{name: name, on: true})
	}
	for _, name := range opts.EnvHints {
		if !slices.Contains(opts.Profile.Env.Pass, name) {
			e.env = append(e.env, envItem{name: name})
		}
	}
	e.name = textinput.New()
	e.name.Prompt = ""
	e.name.CharLimit = 64
	e.name.SetWidth(32)
	e.name.SetValue(opts.Name)
	e.input = textinput.New()
	e.input.Prompt = ""
	e.input.ShowSuggestions = true
	e.view = viewport.New()
	e.view.SoftWrap = true
	e.baseline = e.build()
	return e
}

// Init asks the terminal for its background, to pick the light or dark palette.
func (e *editor) Init() tea.Cmd { return tea.RequestBackgroundColor }

// build turns the screen's state back into a profile.
func (e *editor) build() profile.Profile {
	p := e.p
	p.Home, p.Extra = profile.Mounts{}, profile.Mounts{}
	for _, it := range e.items {
		if !it.on {
			continue
		}
		m := &p.Home
		if it.extra {
			m = &p.Extra
		}
		if it.rw {
			m.RW = append(m.RW, it.path)
		} else {
			m.RO = append(m.RO, it.path)
		}
	}
	p.Env.Pass = nil
	for _, v := range e.env {
		if v.on {
			p.Env.Pass = append(p.Env.Pass, v.name)
		}
	}
	return p
}

// dirty reports unsaved changes.
func (e *editor) dirty() bool {
	return !reflect.DeepEqual(e.build(), e.baseline) || strings.TrimSpace(e.name.Value()) != e.opts.Name
}

// ids is the keyboard focus order: the widgets in the order they're drawn.
func (e *editor) ids() []string {
	ids := []string{"wd:rw", "wd:ro", "net:off", "net:on"}
	for pass := 0; pass < 2; pass++ {
		for i, it := range e.items {
			if it.extra != (pass == 1) {
				continue
			}
			ids = append(ids, fmt.Sprintf("f:%d:on", i), fmt.Sprintf("f:%d:mode", i))
			if it.extra {
				ids = append(ids, fmt.Sprintf("f:%d:rm", i))
			}
		}
		if pass == 1 {
			ids = append(ids, "add:folder")
		}
	}
	for i := range e.env {
		ids = append(ids, fmt.Sprintf("e:%d", i))
	}
	return append(ids, "add:env", "name", "preview", "cancel", "save")
}

func (e *editor) move(delta int) tea.Cmd {
	ids := e.ids()
	i := slices.Index(ids, e.focus)
	if i < 0 {
		i = len(ids) - 1
	}
	i = (i + delta + len(ids)) % len(ids)
	return e.setFocus(ids[i])
}

func (e *editor) setFocus(id string) tea.Cmd {
	e.focus = id
	if id == "name" {
		return e.name.Focus()
	}
	e.name.Blur()
	return nil
}

func (e *editor) setNote(warn bool, format string, a ...any) {
	e.note, e.noteWarn = fmt.Sprintf(format, a...), warn
}

// parseID splits "f:3:mode" into its kind, index and action.
func parseID(id string) (kind string, i int, action string) {
	parts := strings.Split(id, ":")
	kind = parts[0]
	if len(parts) > 1 {
		if n, err := strconv.Atoi(parts[1]); err == nil {
			i = n
		} else {
			action = parts[1]
		}
	}
	if len(parts) > 2 {
		action = parts[2]
	}
	return kind, i, action
}

func (e *editor) activate(id string) tea.Cmd {
	e.msg = ""
	kind, i, action := parseID(id)
	switch kind {
	case "wd":
		e.p.Workdir.Mode = action
	case "net":
		e.p.Network = action == "on"
	case "f":
		if i >= len(e.items) {
			return nil
		}
		it := &e.items[i]
		switch action {
		case "on":
			if !it.on {
				if err := e.opts.CheckPath(it.path, it.rw, it.extra); err != nil {
					e.msg = err.Error()
					return nil
				}
			}
			it.on = !it.on
		case "mode":
			if it.on && !it.rw {
				if err := e.opts.CheckPath(it.path, true, it.extra); err != nil {
					e.msg = err.Error()
					return nil
				}
			}
			it.rw = !it.rw
		case "rm":
			e.setNote(false, "Removed %s", it.path)
			e.items = slices.Delete(e.items, i, i+1)
			e.focus = "add:folder"
		}
	case "e":
		if i < len(e.env) {
			e.env[i].on = !e.env[i].on
		}
	case "add":
		return e.openInput(action)
	case "name":
		return e.setFocus("name")
	case "preview":
		e.openPreview()
	case "cancel":
		return e.cancel()
	case "save":
		return e.save()
	}
	return nil
}

// cancel leaves without saving; with unsaved changes it asks first, by
// wanting a second Esc or Cancel.
func (e *editor) cancel() tea.Cmd {
	if e.dirty() && !e.armed {
		e.armed = true
		e.setNote(true, "You have unsaved changes. Press esc or Cancel again to discard them, or ctrl+s to save.")
		return nil
	}
	return tea.Quit
}

func (e *editor) save() tea.Cmd {
	name := strings.TrimSpace(e.name.Value())
	if !profile.ValidName(name) {
		e.msg = fmt.Sprintf("Profile name %q: use letters, digits, '.', '_', '+' and '-'.", name)
		e.preview = false
		return e.setFocus("name")
	}
	if (e.opts.New || name != e.opts.Name) && e.opts.Exists(name) {
		e.msg = fmt.Sprintf("A profile called %q already exists; choose another name.", name)
		e.preview = false
		return e.setFocus("name")
	}
	p := e.build()
	if _, err := e.opts.Plan(p, name); err != nil {
		e.msg = err.Error()
		e.preview = false
		return nil
	}
	e.result = &Result{Profile: p, Name: name}
	return tea.Quit
}

func (e *editor) openPreview() {
	out, err := e.opts.Plan(e.build(), strings.TrimSpace(e.name.Value()))
	if err != nil {
		e.msg = err.Error()
		return
	}
	e.view.SetContent(out)
	e.view.GotoTop()
	e.preview, e.pvSave = true, false
}

func (e *editor) openInput(kind string) tea.Cmd {
	e.inputKind = kind
	e.inputErr = ""
	e.input.SetValue("")
	e.input.SetSuggestions(nil)
	if kind == "folder" {
		e.input.Placeholder = "~/path/to/folder"
		e.input.SetValue("~/")
		e.input.CursorEnd()
		e.refreshInput()
	} else {
		e.input.Placeholder = "VARIABLE_NAME"
	}
	e.input.SetWidth(layoutWidth(e.width) - 16)
	return e.input.Focus()
}

func (e *editor) closeInput() {
	e.inputKind = ""
	e.inputErr = ""
	e.input.Blur()
}

// refreshInput recomputes completions and checks the typed folder live.
func (e *editor) refreshInput() {
	if e.inputKind != "folder" {
		return
	}
	raw := e.input.Value()
	e.input.SetSuggestions(dirSuggestions(raw, e.opts.Home))
	e.inputErr = ""
	if path := cleanInput(raw); path != "" && path != "~" && !strings.HasSuffix(raw, "/") {
		if err := e.opts.CheckPath(path, false, true); err != nil {
			e.inputErr = err.Error()
		}
	}
}

func (e *editor) submitInput() tea.Cmd {
	switch e.inputKind {
	case "folder":
		path := cleanInput(e.input.Value())
		if err := e.opts.CheckPath(path, false, true); err != nil {
			e.inputErr = err.Error()
			return nil
		}
		for _, it := range e.items {
			if it.path == path {
				e.inputErr = path + " is already listed"
				return nil
			}
		}
		e.items = append(e.items, item{path: path, on: true, extra: true})
		e.setNote(false, "Added %s, read-only. Click read-only to make it writable.", path)
	case "env":
		name := strings.TrimSpace(e.input.Value())
		if err := profile.CheckEnvName(name); err != nil {
			e.inputErr = err.Error()
			return nil
		}
		if slices.ContainsFunc(e.env, func(v envItem) bool { return v.name == name }) {
			e.inputErr = name + " is already listed"
			return nil
		}
		e.env = append(e.env, envItem{name: name, on: true})
		e.setNote(false, "Added %s; its value is read when the program starts.", name)
	}
	kind := e.inputKind
	e.closeInput()
	e.focus = "add:" + kind
	return nil
}

func (e *editor) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if e.page.update(msg) {
		if _, ok := msg.(tea.WindowSizeMsg); ok {
			e.view.SetWidth(layoutWidth(e.width) - 2)
			e.view.SetHeight(max(3, e.height-9))
			e.input.SetWidth(layoutWidth(e.width) - 16)
		}
		return e, nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return e, e.key(msg)
	case tea.MouseClickMsg:
		if msg.Mouse().Button == tea.MouseLeft {
			return e, e.click(e.hitID(msg.Mouse()))
		}
		return e, nil
	case tea.MouseWheelMsg:
		if e.preview {
			var cmd tea.Cmd
			e.view, cmd = e.view.Update(msg)
			return e, cmd
		}
		// The wheel scrolls the page; the focus stays where it is.
		if msg.Mouse().Button == tea.MouseWheelUp {
			e.top -= 3
		} else {
			e.top += 3
		}
		return e, nil
	}
	var cmd tea.Cmd
	switch {
	case e.inputKind != "":
		e.input, cmd = e.input.Update(msg)
	case e.focus == "name":
		e.name, cmd = e.name.Update(msg)
	}
	return e, cmd
}

func (e *editor) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		return tea.Quit
	}
	e.note = ""
	if k != "esc" {
		e.armed = false
	}
	if e.preview {
		switch k {
		case "esc", "q", "backspace":
			e.preview = false
		case "ctrl+s":
			return e.save()
		case "tab", "shift+tab", "left", "right":
			e.pvSave = !e.pvSave
		case "enter", "space":
			if e.pvSave {
				return e.save()
			}
			e.preview = false
		default:
			var cmd tea.Cmd
			e.view, cmd = e.view.Update(msg)
			return cmd
		}
		return nil
	}
	if e.inputKind != "" {
		switch k {
		case "esc":
			e.closeInput()
			return nil
		case "enter":
			return e.submitInput()
		}
		var cmd tea.Cmd
		e.input, cmd = e.input.Update(msg)
		e.refreshInput()
		return cmd
	}
	switch k {
	case "ctrl+s":
		return e.save()
	case "tab", "down":
		return e.move(1)
	case "shift+tab", "up":
		return e.move(-1)
	case "esc":
		return e.cancel()
	}
	if e.focus == "name" {
		if k == "enter" {
			return e.save()
		}
		var cmd tea.Cmd
		e.name, cmd = e.name.Update(msg)
		return cmd
	}
	switch k {
	case "right", "l":
		return e.move(1)
	case "left", "h":
		return e.move(-1)
	case "space", "enter":
		return e.activate(e.focus)
	case "p":
		e.openPreview()
	case "a":
		return e.openInput("folder")
	}
	return nil
}

// click acts on a left click on the widget id ("" for empty space).
func (e *editor) click(id string) tea.Cmd {
	e.note = ""
	if id != "cancel" {
		e.armed = false
	}
	if e.preview {
		switch id {
		case "pv:back":
			e.preview = false
		case "pv:save":
			return e.save()
		}
		return nil
	}
	switch {
	case id == "" || id == "input":
		return nil
	case strings.HasPrefix(id, "sug:"):
		// A completion from the list under the add-folder box.
		n, _ := strconv.Atoi(strings.TrimPrefix(id, "sug:"))
		if s := e.input.MatchedSuggestions(); n < len(s) {
			e.input.SetValue(s[n])
			e.input.CursorEnd()
			e.refreshInput()
		}
		return nil
	}
	if e.inputKind != "" {
		e.closeInput()
	}
	cmd := e.setFocus(id)
	if id == "name" {
		return cmd
	}
	return e.activate(id)
}

// View draws the editor, or the preview over it. The footer (help line,
// buttons, key hints) stays on screen; the body above it scrolls.
func (e *editor) View() tea.View {
	w := layoutWidth(e.width)
	var all []line
	if e.preview {
		all = e.previewLines(w)
	} else {
		body, focusLine := e.body(w)
		footer := e.footer(w)
		avail := e.height - len(footer)
		if e.height <= 0 || len(body) <= avail {
			e.top = 0
		} else {
			if e.focus != e.lastFocus && focusLine >= 0 {
				if focusLine < e.top {
					e.top = focusLine
				} else if focusLine >= e.top+avail {
					e.top = focusLine - avail + 1
				}
			}
			e.top = max(0, min(e.top, len(body)-avail))
			more := ""
			switch {
			case e.top > 0 && e.top+avail < len(body):
				more = "↑↓ more"
			case e.top > 0:
				more = "↑ more above"
			case e.top+avail < len(body):
				more = "↓ more below"
			}
			body = body[e.top : e.top+avail]
			footer[0] = ruleNote(w, more)
		}
		e.lastFocus = e.focus
		// Keep the footer at the bottom of the screen, like a native dialog.
		for e.height > 0 && len(body)+len(footer) < e.height {
			body = append(body, line{})
		}
		all = append(body, footer...)
	}
	return e.draw(all, "box · "+e.opts.Profile.Program)
}

// ruleNote is the separator above the footer, with a scroll hint on it.
func ruleNote(w int, note string) line {
	if note == "" {
		return rule(w)
	}
	n := w - 6 - lipgloss.Width(note)
	return line{styled(styleFaint, " "+strings.Repeat("─", max(1, n))+" "), styled(styleDim, note), styled(styleFaint, " ──")}
}

// body lays out everything above the footer and says which line holds the
// focus (-1 if the focus is in the footer).
func (e *editor) body(w int) ([]line, int) {
	inner := w - 6 // after the gutter
	var ls []line
	blank := func() { ls = append(ls, line{}) }

	title := styleBold.Render(e.opts.Profile.Program) + styleDim.Render(" › ") + strings.TrimSpace(e.name.Value())
	switch {
	case e.opts.New:
		title += styleAccent.Render("  new")
	case e.dirty():
		title += styleAccent.Render("  ● edited")
	}
	sub := line(chip(e.p.Network, "network on", "network off", styleWarn))
	sub = append(sub, txt("   "))
	sub = append(sub, chip(e.p.Workdir.Mode == "rw", "project read-write", "project read-only", styleRW)...)
	sub = append(sub, txt("   "), styled(styleDim, e.opts.Workdir))
	ls = append(ls, banner(title, sub, w)...)
	blank()

	// Access: segmented toggles for the project folder and the network.
	rw := e.p.Workdir.Mode == "rw"
	label := func(s string) seg { return styled(styleDim, pad(s, 17)) }
	ls = append(ls, section("Access", ""),
		line{gutter(), label("Project folder"),
			e.segment("wd:rw", "read-write", rw), e.segment("wd:ro", "read-only", !rw)},
		line{gutter(), label("Network"),
			e.segment("net:off", "off", !e.p.Network), e.segment("net:on", "on", e.p.Network)})
	if e.p.Network {
		ls = append(ls, indent(wrap("⚠ reaches the internet, your LAN, the Windows host on WSL and local services",
			styleWarn, inner), 4)...)
	}
	blank()

	// Folders: one row each; the access pill lines up on the right.
	folder := func(i int, it item) line {
		right := []seg{e.pill(fmt.Sprintf("f:%d:mode", i), it.rw)}
		if it.extra {
			right = append(right, txt(" "), seg{text: btn("✕", secondary, e.focused(fmt.Sprintf("f:%d:rm", i)),
				e.hover == fmt.Sprintf("f:%d:rm", i)), id: fmt.Sprintf("f:%d:rm", i)})
		}
		note := ""
		if it.suggest && !it.on {
			note = styleFaint.Render("suggested  ")
		}
		room := inner - width(line(right)) - lipgloss.Width(note) - 6
		box := e.checkbox(fmt.Sprintf("f:%d:on", i), it.on, shorten(it.path, room))
		gap := inner - width(line(box)) - lipgloss.Width(note) - width(line(right))
		row := append(line{gutter()}, box...)
		return append(append(row, txt(strings.Repeat(" ", max(2, gap))), txt(note)), right...)
	}
	ls = append(ls, section("Program's own folders", "created if missing"))
	n := 0
	for i, it := range e.items {
		if !it.extra {
			ls = append(ls, folder(i, it))
			n++
		}
	}
	if n == 0 {
		ls = append(ls, line{txt("    "), styled(styleFaint, "none")})
	}
	blank()

	ls = append(ls, section("Extra folders", "must exist"))
	for i, it := range e.items {
		if it.extra {
			ls = append(ls, folder(i, it))
		}
	}
	ls = append(ls, line{gutter(), e.button("add:folder", "+ Add folder", secondary)})
	if e.inputKind == "folder" {
		ls = append(ls, e.inputBox("tab completes · ↑↓ choose · enter adds · esc closes", w)...)
	}
	blank()

	// Environment: ticked variables are copied in at run time.
	ls = append(ls, section("Environment", "TERM, LANG and the locale always pass"))
	var groups [][]seg
	for i, v := range e.env {
		groups = append(groups, append(e.checkbox(fmt.Sprintf("e:%d", i), v.on, v.name), txt("   ")))
	}
	groups = append(groups, []seg{e.button("add:env", "+ Add variable", secondary)})
	for _, l := range flow(groups, inner) {
		ls = append(ls, append(line{gutter()}, l...))
	}
	if e.inputKind == "env" {
		ls = append(ls, e.inputBox("enter adds · esc closes", w)...)
	}
	blank()

	// The profile name, as an input box.
	ls = append(ls, section("Profile name", ""))
	border := styleFaint
	if e.focus == "name" {
		border = styleAccent
	}
	ls = append(ls, indent(card([]line{{styled(styleDim, "> "), seg{text: e.name.View(), id: "name"}}},
		min(w-2, 46), border), 1)...)
	pointAt(ls, e.focus)

	focusLine := -1
	for y, l := range ls {
		for _, s := range l {
			if (s.id != "" && s.id == e.focus) || (s.id == "input" && e.inputKind != "") {
				focusLine = y
			}
		}
	}
	return ls, focusLine
}

// footer is the part that stays on screen: a separator, a help line about
// the focused item (or an error or a note), the buttons and the key hints.
func (e *editor) footer(w int) []line {
	ls := []line{rule(w)}
	var text []line
	switch {
	case e.msg != "":
		text = indent(wrap("✗ "+e.msg, styleError, w-4), 2)
	case e.note != "":
		st, mark := styleOK, "✓ "
		if e.noteWarn {
			st, mark = styleWarn, "⚠ "
		}
		text = indent(wrap(mark+e.note, st, w-4), 2)
	default:
		// The help line, marked by an accent bar, like a callout.
		for i, l := range wrap(e.describe(e.focus), styleDim, w-6) {
			mark := txt("    ")
			if i == 0 {
				mark = seg{text: "  " + styleAccent.Render("▎") + " "}
			}
			text = append(text, append(line{mark}, l...))
		}
	}
	// Two lines, always, so the page doesn't jump as the focus moves;
	// only an error may take more.
	for len(text) < 2 {
		text = append(text, line{})
	}
	if e.msg == "" && len(text) > 2 {
		text = text[:2]
	}
	ls = append(ls, text...)
	buttons := line{
		e.button("preview", "Preview", secondary), txt("  "),
		e.button("cancel", "Cancel", secondary), txt("  "),
		e.button("save", "▶ Save & run", primary), txt(" "),
	}
	ls = append(ls, line{}, rightAlign(buttons, w), line{})
	return append(ls, hints(w, "tab/↑↓", "move", "space", "toggle", "a", "add", "p", "preview",
		"ctrl+s", "save", "esc", "cancel")...)
}

// describe explains the focused widget in one sentence.
func (e *editor) describe(id string) string {
	kind, i, action := parseID(id)
	prog := e.opts.Profile.Program
	switch kind {
	case "wd":
		if action == "rw" {
			return "The program can create, change and delete files in this project. .git/config and .git/hooks stay read-only."
		}
		return "The program can read this project but not change anything in it."
	case "net":
		if action == "on" {
			return "Full network access: the internet, your LAN, the Windows host on WSL and local services."
		}
		return "No network: only a loopback interface inside the box. Safest when the program works offline."
	case "f":
		if i >= len(e.items) {
			return ""
		}
		it := e.items[i]
		switch action {
		case "on":
			if it.extra {
				return "Mount " + it.path + " into the box at the same path. It must already exist."
			}
			return "Mount " + it.path + ", where " + prog + " keeps its own files. Created if it doesn't exist yet."
		case "mode":
			return "Read-write lets the program change files there; read-only lets it only read them. Space switches."
		case "rm":
			return "Remove " + it.path + " from this profile."
		}
	case "e":
		if i < len(e.env) {
			return "Copy " + e.env[i].name + " from your environment into the box when it starts. The value is never saved."
		}
	case "add":
		if action == "folder" {
			return "Add a folder that already exists on this machine. Tab completes the path."
		}
		return "Pass another environment variable by name."
	case "name":
		return "The profile's name. Saving under a new name keeps the old profile as it was."
	case "preview":
		return "Show the exact bwrap command box will run. Nothing runs."
	case "cancel":
		return "Leave without saving or running anything."
	case "save":
		return "Save this profile and run " + prog + " in the box."
	}
	return ""
}

// inputBox is the add-folder or add-variable input: a rounded box with a
// > prompt, the matching folders below it, then the live error if there is
// one, else what the keys do.
func (e *editor) inputBox(keys string, w int) []line {
	out := indent(card([]line{{styled(styleAccent, "> "), seg{text: e.input.View(), id: "input"}}}, w-4, styleAccent), 3)
	if e.inputKind == "folder" {
		matches := e.input.MatchedSuggestions()
		cur := e.input.CurrentSuggestionIndex()
		start := max(0, min(cur-2, len(matches)-6))
		for n := start; n < len(matches) && n < start+6; n++ {
			ptr, st := "  ", styleDim
			if n == cur {
				ptr, st = styleAccent.Render("❯ "), stylePlain
			}
			if e.hover == "sug:"+strconv.Itoa(n) {
				st = st.Underline(true)
			}
			out = append(out, line{txt("     "), txt(ptr), seg{text: st.Render(shorten(matches[n], w-12)), id: "sug:" + strconv.Itoa(n)}})
		}
		if len(matches) > 6 {
			out = append(out, line{txt("       "), styled(styleFaint, fmt.Sprintf("%d folders match", len(matches)))})
		}
	}
	if e.inputErr != "" {
		return append(out, indent(wrap("✗ "+e.inputErr, styleError, w-8), 5)...)
	}
	return append(out, line{txt("     "), styled(styleDim, keys)})
}

// previewLines is the preview screen: the command in a scrolling pane,
// with Back and Save & run buttons.
func (e *editor) previewLines(w int) []line {
	ls := banner("Preview", line{styled(styleDim, "the exact command box will run · nothing has run yet")}, w)
	for _, l := range strings.Split(e.view.View(), "\n") {
		ls = append(ls, line{txt(" "), txt(l)})
	}
	pct := fmt.Sprintf("%3.0f%%", e.view.ScrollPercent()*100)
	buttons := line{
		seg{text: btn("Back", secondary, !e.pvSave, e.hover == "pv:back"), id: "pv:back"}, txt("  "),
		seg{text: btn("▶ Save & run", primary, e.pvSave, e.hover == "pv:save"), id: "pv:save"}, txt(" "),
	}
	footer := []line{ruleNote(w, pct), line{}, rightAlign(buttons, w), line{}}
	footer = append(footer, hints(w, "↑↓ pgup pgdn", "scroll", "tab", "switch button", "esc", "back", "ctrl+s", "save & run")...)
	for e.height > 0 && len(ls)+len(footer) < e.height {
		ls = append(ls, line{})
	}
	return append(ls, footer...)
}
