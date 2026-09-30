// Package tui is box's profile editor: one screen, mouse and keyboard.
// It is the only package that imports the Charm libraries.
package tui

import (
	"errors"
	"fmt"
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
	Saved   bool // false when cancelled
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
	r := final.(*editor).result
	if !r.Saved {
		return r, ErrCancelled
	}
	return r, nil
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
	opts  Options
	p     profile.Profile // settings the screen doesn't list item by item
	items []item
	env   []envItem
	focus string

	name      textinput.Model
	input     textinput.Model // the add-folder or add-variable input
	inputKind string          // "", "folder" or "env"
	inputErr  string
	msg       string // why Save or Preview failed

	preview bool
	view    viewport.Model

	width, height int
	top           int
	hits          *lipgloss.Compositor
	result        Result
}

func newEditor(opts Options) *editor {
	e := &editor{opts: opts, p: opts.Profile, focus: "save", width: 80}
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
	e.name.SetWidth(16)
	e.name.SetValue(opts.Name)
	e.input = textinput.New()
	e.input.Prompt = ""
	e.input.ShowSuggestions = true
	e.view = viewport.New()
	return e
}

func (e *editor) Init() tea.Cmd { return nil }

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
		switch action {
		case "on":
			if !e.items[i].on {
				if err := e.opts.CheckPath(e.items[i].path, e.items[i].rw, e.items[i].extra); err != nil {
					e.msg = err.Error()
					return nil
				}
			}
			e.items[i].on = !e.items[i].on
		case "mode":
			if e.items[i].on && !e.items[i].rw {
				if err := e.opts.CheckPath(e.items[i].path, true, e.items[i].extra); err != nil {
					e.msg = err.Error()
					return nil
				}
			}
			e.items[i].rw = !e.items[i].rw
		case "rm":
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
		out, err := e.opts.Plan(e.build(), strings.TrimSpace(e.name.Value()))
		if err != nil {
			e.msg = err.Error()
			return nil
		}
		e.view.SetContent(out)
		e.view.GotoTop()
		e.preview = true
	case "cancel":
		return tea.Quit
	case "save":
		return e.save()
	}
	return nil
}

func (e *editor) save() tea.Cmd {
	name := strings.TrimSpace(e.name.Value())
	if !profile.ValidName(name) {
		e.msg = fmt.Sprintf("profile name %q: use letters, digits, '.', '_' and '-'", name)
		return e.setFocus("name")
	}
	if (e.opts.New || name != e.opts.Name) && e.opts.Exists(name) {
		e.msg = fmt.Sprintf("a profile called %q already exists", name)
		return e.setFocus("name")
	}
	p := e.build()
	if _, err := e.opts.Plan(p, name); err != nil {
		e.msg = err.Error()
		return nil
	}
	e.result = Result{Profile: p, Name: name, Saved: true}
	return tea.Quit
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
	e.input.SetWidth(max(20, e.width-14))
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
	}
	kind := e.inputKind
	e.closeInput()
	e.focus = "add:" + kind
	return nil
}

func (e *editor) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		e.width, e.height = msg.Width, msg.Height
		e.view.SetWidth(msg.Width)
		e.view.SetHeight(max(1, msg.Height-2))
		return e, nil
	case tea.KeyPressMsg:
		return e, e.key(msg)
	case tea.MouseClickMsg:
		return e, e.click(msg.Mouse())
	case tea.MouseWheelMsg:
		if e.preview {
			var cmd tea.Cmd
			e.view, cmd = e.view.Update(msg)
			return e, cmd
		}
		if msg.Mouse().Button == tea.MouseWheelUp {
			return e, e.move(-1)
		}
		return e, e.move(1)
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
	if e.preview {
		switch k {
		case "esc", "q", "enter", "space":
			e.preview = false
			return nil
		}
		var cmd tea.Cmd
		e.view, cmd = e.view.Update(msg)
		return cmd
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
	case "tab", "down":
		return e.move(1)
	case "shift+tab", "up":
		return e.move(-1)
	case "esc":
		return tea.Quit
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
	}
	return nil
}

func (e *editor) click(m tea.Mouse) tea.Cmd {
	if m.Button != tea.MouseLeft || e.hits == nil {
		return nil
	}
	if e.preview {
		e.preview = false
		return nil
	}
	id := e.hits.Hit(m.X, m.Y).ID()
	if id == "" || id == "input" {
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

// View draws the editor, or the preview pane over it.
func (e *editor) View() tea.View {
	var content string
	if e.preview {
		help := styleDim.Render("  bwrap command · ↑↓ / wheel to scroll · Esc to close")
		content = e.view.View() + "\n" + help
		e.hits = nil
	} else {
		lines, focusLine := e.lines()
		height := e.height
		if height > 0 && len(lines) > height {
			if focusLine < e.top {
				e.top = focusLine
			} else if focusLine >= e.top+height {
				e.top = focusLine - height + 1
			}
			e.top = min(e.top, len(lines)-height)
		} else {
			e.top = 0
		}
		content, e.hits = render(lines, e.top, height)
	}
	return screen(content, "box · "+e.opts.Profile.Program)
}

// lines lays out the whole screen and says which line holds the focus.
func (e *editor) lines() ([]line, int) {
	w := max(40, e.width)
	var ls []line
	add := func(l ...seg) { ls = append(ls, line(l)) }

	title := fmt.Sprintf(" box · %s · profile %q", e.opts.Profile.Program, strings.TrimSpace(e.name.Value()))
	if e.opts.New {
		title += " (new)"
	}
	right := e.opts.Workdir
	if gap := w - lipgloss.Width(title) - lipgloss.Width(right) - 1; gap > 0 {
		add(styled(styleTitle, title), txt(strings.Repeat(" ", gap)), styled(styleDim, right))
	} else {
		add(styled(styleTitle, title))
	}
	add()

	rw := e.p.Workdir.Mode == "rw"
	add(styled(styleSection, " Project folder  "),
		e.widget("wd:rw", radio(rw)+" read-write"), txt("  "),
		e.widget("wd:ro", radio(!rw)+" read-only"), txt("     "),
		styled(styleSection, "Network  "),
		e.widget("net:off", radio(!e.p.Network)+" off"), txt("  "),
		e.widget("net:on", radio(e.p.Network)+" on"))
	if e.p.Network {
		add(styled(styleWarn, "   Network on reaches the internet, your LAN, the Windows host on WSL and local services."))
	}
	add()

	pathWidth := 24
	for _, it := range e.items {
		pathWidth = max(pathWidth, lipgloss.Width(it.path)+2)
	}
	pathWidth = min(pathWidth, max(24, w-30))
	folder := func(i int, it item) {
		l := line{txt("  "), e.widget(fmt.Sprintf("f:%d:on", i), check(it.on)+" "+pad(it.path, pathWidth)),
			txt(" "), e.widget(fmt.Sprintf("f:%d:mode", i), mode(it.rw))}
		if it.extra {
			l = append(l, txt("   "), e.widget(fmt.Sprintf("f:%d:rm", i), "[remove]"))
		}
		if it.suggest && !it.on {
			l = append(l, styled(styleDim, "   suggested"))
		}
		ls = append(ls, l)
	}
	add(styled(styleSection, " Program's own folders"), styled(styleDim, "  (created if missing)"))
	n := 0
	for i, it := range e.items {
		if !it.extra {
			folder(i, it)
			n++
		}
	}
	if n == 0 {
		add(styled(styleDim, "  none"))
	}
	add(styled(styleSection, " Extra folders"))
	for i, it := range e.items {
		if it.extra {
			folder(i, it)
		}
	}
	add(txt("  "), e.widget("add:folder", "[+ add folder]"))
	if e.inputKind == "folder" {
		add(txt("  folder: "), seg{text: e.input.View(), id: "input"})
		e.inputLine(add, "Tab completes · Enter adds · Esc closes")
	}
	add()

	add(styled(styleSection, " Environment variables passed in"), styled(styleDim, "  (TERM, LANG and the locale always are)"))
	var groups [][]seg
	for i, v := range e.env {
		groups = append(groups, []seg{e.widget(fmt.Sprintf("e:%d", i), check(v.on)+" "+v.name), txt("  ")})
	}
	groups = append(groups, []seg{e.widget("add:env", "[+ add]")})
	ls = append(ls, flow("  ", groups, w)...)
	if e.inputKind == "env" {
		add(txt("  name: "), seg{text: e.input.View(), id: "input"})
		e.inputLine(add, "Enter adds · Esc closes")
	}
	add()

	nameSeg := seg{text: "[" + e.name.View() + "]", id: "name"}
	add(txt(" Profile name: "), nameSeg, txt("   "),
		e.widget("preview", "[ Preview command ]"), txt("  "),
		e.widget("cancel", "[ Cancel ]"), txt("  "),
		seg{text: e.saveLabel(), id: "save"})
	if e.msg != "" {
		for _, m := range strings.Split(e.msg, "\n") {
			add(styled(styleError, " "+m))
		}
	}
	add(styled(styleDim, " Tab/↑↓ move · Space/Enter toggle · click anything · Esc cancels"))

	focusLine := 0
	for y, l := range ls {
		for _, s := range l {
			if s.id == e.focus || (s.id == "input" && e.inputKind != "") {
				focusLine = y
			}
		}
	}
	return ls, focusLine
}

func (e *editor) inputLine(add func(...seg), help string) {
	if e.inputErr != "" {
		add(styled(styleError, "  "+e.inputErr))
	} else {
		add(styled(styleDim, "  "+help))
	}
}

func (e *editor) saveLabel() string {
	label := "[ ▶ Save & run ]"
	if e.focus == "save" && e.inputKind == "" {
		return styleFocus.Render(label)
	}
	return styleSave.Render(label)
}
