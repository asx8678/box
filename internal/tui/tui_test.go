package tui

import (
	"errors"
	"image/color"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/asx8678/box/internal/profile"
)

func press(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func typeText(m tea.Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// clickOn renders the model and clicks the middle of the widget with id.
func clickOn(t *testing.T, m tea.Model, hits func() *lipgloss.Compositor, id string) {
	t.Helper()
	m.View()
	c := hits()
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			if c.Hit(x, y).ID() == id {
				m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
				return
			}
		}
	}
	t.Fatalf("no widget %q on screen", id)
}

func newTestEditor(t *testing.T) *editor {
	p := profile.Default("tool")
	p.Home.RO = []string{"~/.tool"}
	p.Env.Pass = []string{"TOOL_API_KEY"}
	e := newEditor(Options{
		Profile:   p,
		Name:      "default",
		New:       true,
		Workdir:   "~/code/proj",
		Home:      "/home/u",
		Suggested: []string{"~/.cache/tool"},
		EnvHints:  []string{"AWS_PROFILE"},
		Exists:    func(name string) bool { return name == "taken" },
		CheckPath: func(path string, rw, mustExist bool) error {
			if strings.Contains(path, "ssh") {
				return errors.New("refused: " + path)
			}
			if path == "~/missing" && mustExist {
				return errors.New("~/missing doesn't exist")
			}
			return nil
		},
		Plan: func(p profile.Profile, name string) (string, error) { return "bwrap ...", nil },
	})
	e.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return e
}

func (e *editor) hitsFn() func() *lipgloss.Compositor {
	return func() *lipgloss.Compositor { return e.hits }
}

func TestEditorMouse(t *testing.T) {
	e := newTestEditor(t)
	clickOn(t, e, e.hitsFn(), "net:on")
	clickOn(t, e, e.hitsFn(), "wd:ro")
	clickOn(t, e, e.hitsFn(), "f:1:on")   // tick the suggested ~/.cache/tool
	clickOn(t, e, e.hitsFn(), "f:0:mode") // ~/.tool ro → rw
	clickOn(t, e, e.hitsFn(), "e:1")      // tick AWS_PROFILE
	clickOn(t, e, e.hitsFn(), "save")
	if !e.result.Saved {
		t.Fatalf("not saved: %q", e.msg)
	}
	p := e.result.Profile
	if !p.Network || p.Workdir.Mode != "ro" {
		t.Errorf("switches: %+v", p)
	}
	if !slices.Equal(p.Home.RW, []string{"~/.tool", "~/.cache/tool"}) || len(p.Home.RO) != 0 {
		t.Errorf("home %+v", p.Home)
	}
	if !slices.Equal(p.Env.Pass, []string{"TOOL_API_KEY", "AWS_PROFILE"}) {
		t.Errorf("env %v", p.Env.Pass)
	}
}

func TestEditorKeyboardFocusOrder(t *testing.T) {
	e := newTestEditor(t)
	ids := e.ids()
	if ids[0] != "wd:rw" || ids[len(ids)-1] != "save" {
		t.Fatalf("ids %v", ids)
	}
	e.setFocus("save")
	e.Update(press(tea.KeyTab)) // from the last widget wraps to the first
	if e.focus != "wd:rw" {
		t.Fatalf("focus %s", e.focus)
	}
	for e.focus != "net:on" {
		e.Update(press(tea.KeyTab))
	}
	e.Update(press(tea.KeySpace))
	if !e.p.Network {
		t.Error("space didn't switch network on")
	}
	e.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if e.focus != "net:off" {
		t.Errorf("shift+tab went to %s", e.focus)
	}
}

func TestEditorAddFolder(t *testing.T) {
	e := newTestEditor(t)
	clickOn(t, e, e.hitsFn(), "add:folder")
	if e.inputKind != "folder" || e.input.Value() != "~/" {
		t.Fatalf("input %q %q", e.inputKind, e.input.Value())
	}
	typeText(e, ".ssh")
	if !strings.Contains(e.inputErr, "refused") {
		t.Errorf("live check didn't run: %q", e.inputErr)
	}
	e.Update(press(tea.KeyEnter))
	if e.inputKind != "folder" {
		t.Error("a refused folder must not be added")
	}
	e.input.SetValue("~/code/lib/")
	e.Update(press(tea.KeyEnter))
	if e.inputKind != "" {
		t.Fatalf("not added: %q", e.inputErr)
	}
	last := e.items[len(e.items)-1]
	if last.path != "~/code/lib" || !last.extra || last.rw || !last.on {
		t.Errorf("added %+v", last)
	}
	clickOn(t, e, e.hitsFn(), "f:2:rm")
	if len(e.items) != 2 {
		t.Errorf("remove left %d items", len(e.items))
	}
}

func TestEditorRefusesUnsafeToggle(t *testing.T) {
	e := newTestEditor(t)
	e.items = append(e.items, item{path: "~/.ssh", rw: false, suggest: true})
	clickOn(t, e, e.hitsFn(), "f:2:on")
	if e.items[2].on || !strings.Contains(e.msg, "refused") {
		t.Errorf("unsafe folder ticked: %+v, msg %q", e.items[2], e.msg)
	}
}

func TestEditorSaveChecksName(t *testing.T) {
	e := newTestEditor(t)
	e.name.SetValue("taken")
	e.Update(press(tea.KeyTab)) // save → wd:rw; back to save via activate
	e.save()
	if e.result.Saved || !strings.Contains(e.msg, "already exists") {
		t.Errorf("saved over an existing profile: %q", e.msg)
	}
	e.name.SetValue("bad name")
	e.save()
	if e.result.Saved || e.focus != "name" {
		t.Errorf("invalid name accepted: %q", e.msg)
	}
	e.name.SetValue("online")
	e.save()
	if !e.result.Saved || e.result.Name != "online" {
		t.Errorf("valid save failed: %q", e.msg)
	}
}

func TestEditorPreviewAndCancel(t *testing.T) {
	e := newTestEditor(t)
	clickOn(t, e, e.hitsFn(), "preview")
	if !e.preview || !strings.Contains(e.View().Content, "bwrap") {
		t.Fatal("preview didn't open")
	}
	e.Update(press(tea.KeyEscape))
	if e.preview {
		t.Fatal("esc didn't close the preview")
	}
	_, cmd := e.Update(press(tea.KeyEscape))
	if cmd == nil || e.result.Saved {
		t.Error("esc should cancel without saving")
	}
}

func TestPicker(t *testing.T) {
	choices := []Choice{{"default", "net off"}, {"online", "net on"}}
	m := &picker{title: "t", choices: choices, chosen: -1}
	m.Update(press(tea.KeyDown))
	m.Update(press(tea.KeyEnter))
	if m.chosen != 1 {
		t.Errorf("keyboard chose %d", m.chosen)
	}
	m = &picker{title: "t", choices: choices, chosen: -1}
	clickOn(t, m, func() *lipgloss.Compositor { return m.hits }, "c:0")
	if m.chosen != 0 {
		t.Errorf("click chose %d", m.chosen)
	}
	m = &picker{title: "t", choices: choices, chosen: -1}
	m.Update(press(tea.KeyEscape))
	if m.chosen != -1 {
		t.Error("esc chose something")
	}
}

func TestConfirm(t *testing.T) {
	m := &confirm{title: "t", question: "q?"}
	m.Update(press(tea.KeyEnter))
	if m.yes {
		t.Error("Enter must default to No")
	}
	m = &confirm{title: "t", question: "q?"}
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !m.yes {
		t.Error("y didn't confirm")
	}
	m = &confirm{title: "t", question: "q?"}
	clickOn(t, m, func() *lipgloss.Compositor { return m.hits }, "yes")
	if !m.yes {
		t.Error("clicking Yes didn't confirm")
	}
}

func TestDirSuggestions(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"code", "config", ".hidden"} {
		if err := mkdir(root + "/" + d); err != nil {
			t.Fatal(err)
		}
	}
	got := dirSuggestions(root+"/co", "/home/u")
	if !slices.Equal(got, []string{root + "/code/", root + "/config/"}) {
		t.Errorf("got %v", got)
	}
	if got := dirSuggestions(root+"/", "/home/u"); slices.Contains(got, root+"/.hidden/") {
		t.Error("hidden folders offered without a dot")
	}
	if got := dirSuggestions(root+"/.h", "/home/u"); !slices.Equal(got, []string{root + "/.hidden/"}) {
		t.Errorf("dot prefix: %v", got)
	}
	for in, want := range map[string]string{"~/code/": "~/code", "~/": "~", "/opt//x/": "/opt/x"} {
		if got := cleanInput(in); got != want {
			t.Errorf("cleanInput(%q) = %q", in, got)
		}
	}
}

func mkdir(p string) error { return os.Mkdir(p, 0o755) }

func TestEditorRetickFolderCreatedOnFirstRun(t *testing.T) {
	e := newTestEditor(t)
	e.items = append(e.items, item{path: "~/missing", rw: true, on: true})
	clickOn(t, e, e.hitsFn(), "f:2:on") // untick
	clickOn(t, e, e.hitsFn(), "f:2:on") // tick again: a program folder may not exist yet
	if !e.items[2].on {
		t.Errorf("couldn't re-tick a program folder that doesn't exist yet: %q", e.msg)
	}
}

func TestHoverAndTheme(t *testing.T) {
	e := newTestEditor(t)
	e.View()
	for y := 0; y < 60; y++ {
		for x := 0; x < 100; x++ {
			if e.hits.Hit(x, y).ID() == "net:on" {
				e.Update(tea.MouseMotionMsg{X: x, Y: y})
				if e.hover != "net:on" {
					t.Fatalf("hover %q", e.hover)
				}
				defer applyTheme(true)
				dark := styleAccent.Render("x")
				e.Update(tea.BackgroundColorMsg{Color: color.White})
				if styleAccent.Render("x") == dark {
					t.Error("a light background didn't switch the palette")
				}
				return
			}
		}
	}
	t.Fatal("net:on not on screen")
}

func TestPickerHoverSelects(t *testing.T) {
	m := &picker{title: "t", choices: []Choice{{"a", ""}, {"b", ""}}, chosen: -1}
	m.View()
	for y := 0; y < 30; y++ {
		for x := 0; x < 80; x++ {
			if m.hits.Hit(x, y).ID() == "c:1" {
				m.Update(tea.MouseMotionMsg{X: x, Y: y})
				if m.cursor != 1 {
					t.Errorf("cursor %d", m.cursor)
				}
				return
			}
		}
	}
	t.Fatal("row not on screen")
}

func TestEscGuardsUnsavedChanges(t *testing.T) {
	e := newTestEditor(t)
	if _, cmd := e.Update(press(tea.KeyEscape)); cmd == nil {
		t.Fatal("esc with no changes should leave")
	}
	e = newTestEditor(t)
	clickOn(t, e, e.hitsFn(), "net:on") // a change
	if _, cmd := e.Update(press(tea.KeyEscape)); cmd != nil || !e.armed || !strings.Contains(e.note, "unsaved") {
		t.Fatalf("first esc should warn, got note %q armed %v", e.note, e.armed)
	}
	if _, cmd := e.Update(press(tea.KeyEscape)); cmd == nil {
		t.Fatal("second esc should discard and leave")
	}
	// Any other key in between disarms it.
	e = newTestEditor(t)
	clickOn(t, e, e.hitsFn(), "net:on")
	e.Update(press(tea.KeyEscape))
	e.Update(press(tea.KeyTab))
	if _, cmd := e.Update(press(tea.KeyEscape)); cmd != nil {
		t.Error("esc after another key should warn again, not leave")
	}
	if e.result.Saved {
		t.Error("nothing should have been saved")
	}
}

func TestShortcuts(t *testing.T) {
	e := newTestEditor(t)
	e.setFocus("wd:rw")
	e.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if !e.preview {
		t.Fatal("p should open the preview")
	}
	e.Update(press(tea.KeyEscape))
	e.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if e.inputKind != "folder" {
		t.Fatal("a should open the add-folder box")
	}
	e.Update(press(tea.KeyEscape))
	e.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !e.result.Saved {
		t.Errorf("ctrl+s should save: %q", e.msg)
	}
}

func TestWheelScrollsWithoutMovingFocus(t *testing.T) {
	e := newTestEditor(t)
	e.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	e.setFocus("wd:rw")
	e.View()
	e.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	e.View()
	if e.focus != "wd:rw" || e.top == 0 {
		t.Errorf("focus %q top %d", e.focus, e.top)
	}
}

func TestPreviewButtons(t *testing.T) {
	e := newTestEditor(t)
	clickOn(t, e, e.hitsFn(), "preview")
	clickOn(t, e, e.hitsFn(), "pv:back")
	if e.preview {
		t.Fatal("Back should close the preview")
	}
	clickOn(t, e, e.hitsFn(), "preview")
	clickOn(t, e, e.hitsFn(), "pv:save")
	if !e.result.Saved {
		t.Errorf("Save & run in the preview should save: %q", e.msg)
	}
}

func TestCompletionClick(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(root+"/code", 0o755)
	e := newTestEditor(t)
	e.opts.Home = root
	e.Update(tea.WindowSizeMsg{Width: 80, Height: 60})
	e.openInput("folder")
	clickOn(t, e, e.hitsFn(), "sug:0")
	if e.input.Value() != "~/code/" {
		t.Errorf("value %q", e.input.Value())
	}
}

func TestConfirmButtons(t *testing.T) {
	m := &confirm{title: "t", question: "q?"}
	clickOn(t, m, func() *lipgloss.Compositor { return m.hits }, "no")
	if m.yes {
		t.Error("No returned yes")
	}
	m = &confirm{title: "t", question: "q?"}
	m.Update(press(tea.KeyRight))
	m.Update(press(tea.KeyEnter))
	if !m.yes {
		t.Error("→ then enter should press Yes")
	}
}

func TestColorizeKeepsText(t *testing.T) {
	in := "env -i \\\n  HOME=/h \\\n/usr/bin/bwrap \\\n  --bind /a /a \\\n  -- /bin/x arg \\\n  3<<<'program=x\nprofile=y'\n# note"
	out := ansiStrip(colorize(in))
	if out != in {
		t.Errorf("colorize changed the text:\n%s", out)
	}
}

func ansiStrip(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;:]*[A-Za-z]`).ReplaceAllString(s, "")
}
