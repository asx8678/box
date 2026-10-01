package tui

import (
	"errors"
	"image/color"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/asx8678/box/internal/profile"
)

// The tests send keys at once; TestEarlyKeysAreIgnored covers the grace.
func TestMain(m *testing.M) {
	keyGrace = 0
	os.Exit(m.Run())
}

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
	if e.result == nil {
		t.Fatalf("not saved: %q", e.msg)
	}
	p := e.result.Profile
	if p.Network != profile.NetOn || p.Workdir.Mode != "ro" {
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
	if e.p.Network != profile.NetOn {
		t.Error("space didn't switch network on")
	}
	e.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if e.focus != "net:restricted" {
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
	if e.result != nil || !strings.Contains(e.msg, "already exists") {
		t.Errorf("saved over an existing profile: %q", e.msg)
	}
	e.name.SetValue("bad name")
	e.save()
	if e.result != nil || e.focus != "name" {
		t.Errorf("invalid name accepted: %q", e.msg)
	}
	e.name.SetValue("online")
	e.save()
	if e.result == nil || e.result.Name != "online" {
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
	if cmd == nil || e.result != nil {
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
	if e.result != nil {
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
	if e.result == nil {
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
	if e.result == nil {
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

// plain removes the colour codes from drawn text.
func plain(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;:]*[A-Za-z]`).ReplaceAllString(s, "")
}

func kiroEditor(t *testing.T, w, h int) *editor {
	p, err := profile.Preset("kiro-cli")
	if err != nil {
		t.Fatal(err)
	}
	e := newEditor(Options{
		Profile:   p,
		Name:      "default",
		New:       true,
		Home:      "/home/u",
		Suggested: []string{"~/.cache/kiro-cli", "~/.config/kiro"},
		Exists:    func(string) bool { return false },
		CheckPath: func(string, bool, bool) error { return nil },
		Plan:      func(profile.Profile, string) (string, error) { return "bwrap ...", nil },
	})
	e.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return e
}

func TestEditorRestrictedNetwork(t *testing.T) {
	e := kiroEditor(t, 100, 50)
	clickOn(t, e, e.hitsFn(), "net:restricted")
	if e.p.Network != profile.NetRestricted {
		t.Fatalf("network %q", e.p.Network)
	}
	for _, id := range []string{"own", "gall", "add:host"} {
		if !slices.Contains(e.ids(), id) {
			t.Errorf("no %s in the focus order: %v", id, e.ids())
		}
	}
	// The program's own servers can't be unticked.
	clickOn(t, e, e.hitsFn(), "own")
	if !strings.Contains(e.note, "always allowed") || !strings.Contains(e.describe("own"), "runtime.us-east-1.kiro.dev") {
		t.Errorf("own servers: note %q, help %q", e.note, e.describe("own"))
	}
	aws := slices.IndexFunc(e.groups, func(g groupItem) bool { return g.ID == "aws" })
	clickOn(t, e, e.hitsFn(), "g:"+strconv.Itoa(aws))
	if !strings.Contains(e.describe("g:"+strconv.Itoa(aws)), "every AWS account") {
		t.Errorf("the AWS group must say what it reaches: %q", e.describe("g:"+strconv.Itoa(aws)))
	}
	clickOn(t, e, e.hitsFn(), "gall") // one click: all official documentation
	if !e.allDocs() {
		t.Error("All didn't tick every documentation group")
	}

	clickOn(t, e, e.hitsFn(), "add:host")
	typeText(e, "*.com")
	e.Update(press(tea.KeyEnter))
	if e.inputKind != "host" || e.inputErr == "" {
		t.Fatalf("*.com must be refused: kind %q, err %q", e.inputKind, e.inputErr)
	}
	e.input.SetValue("https://Wiki.Example.com/page")
	e.Update(press(tea.KeyEnter))
	e.Update(press(tea.KeyEnter)) // on "+ Add host" again
	e.input.SetValue("old.example.com")
	e.Update(press(tea.KeyEnter))
	if !slices.Equal(e.hosts, []string{"wiki.example.com", "old.example.com"}) {
		t.Fatalf("hosts %v, input error %q", e.hosts, e.inputErr)
	}
	clickOn(t, e, e.hitsFn(), "h:1:rm")
	// Custom takes IP addresses too, and refuses what isn't one.
	for in, want := range map[string]string{
		"10.0.0.5:8443":          "10.0.0.5:8443",
		"http://[2001:DB8::1]/x": "2001:db8::1",
		"999.1.1.1":              "",
		"10.0.0.0/24":            "",
	} {
		e.Update(press(tea.KeyEnter))
		e.input.SetValue(in)
		e.Update(press(tea.KeyEnter))
		if added := e.inputKind == ""; added != (want != "") || (added && e.hosts[len(e.hosts)-1] != want) {
			t.Errorf("custom %q: added %v, hosts %v, error %q", in, added, e.hosts, e.inputErr)
		}
		e.Update(press(tea.KeyEscape))
		e.setFocus("add:host")
	}
	e.hosts = e.hosts[:1]

	clickOn(t, e, e.hitsFn(), "save")
	if e.result == nil {
		t.Fatalf("not saved: %q", e.msg)
	}
	p := e.result.Profile
	want := []string{"docs-microsoft", "docs-kubernetes", "docs-aws", "docs-languages", "docs-tools", "aws"}
	if p.Network != profile.NetRestricted || !slices.Equal(p.Allow.Groups, want) || !slices.Equal(p.Allow.Hosts, []string{"wiki.example.com"}) {
		t.Errorf("saved network %q, allow %+v", p.Network, p.Allow)
	}
	if err := p.Validate("kiro-cli"); err != nil {
		t.Error(err)
	}
}

func TestRestrictedKeepsChoicesAndWarnsUnknownPrograms(t *testing.T) {
	e := newTestEditor(t) // "tool": box has no servers listed for it
	e.activate("net:restricted")
	if slices.Contains(e.ids(), "own") || !strings.Contains(plain(e.View().Content), "no list of servers") {
		t.Error("an unknown program must be told it gets no network of its own")
	}
	e.activate("gall")
	e.activate("net:on")
	if slices.Contains(e.ids(), "gall") {
		t.Error("the allowlist shows only for a restricted network")
	}
	if p := e.build(); p.Network != profile.NetOn || len(p.Allow.Groups) == 0 {
		t.Errorf("switching away must keep the choices: %q %+v", p.Network, p.Allow)
	}
}

// An input opened low on a short terminal scrolls into view with the lines
// under it; it used to open below the footer.
func TestInputStaysInView(t *testing.T) {
	for _, kind := range []string{"folder", "host"} {
		e := kiroEditor(t, 80, 24)
		e.activate("net:restricted")
		e.View()
		for e.focus != "add:"+kind {
			e.Update(press(tea.KeyTab))
			e.View()
		}
		e.Update(press(tea.KeyEnter))
		out := e.View().Content
		if !strings.Contains(out, "╭") || !strings.Contains(out, "esc closes") {
			t.Errorf("%s input isn't fully on screen:\n%s", kind, out)
		}
	}
}

func TestTinyTerminalDoesNotCrash(t *testing.T) {
	for h := 0; h < 12; h++ {
		e := kiroEditor(t, 80, h)
		e.View()
		e.activate("net:restricted")
		e.openInput("host")
		e.View()
		e.closeInput()
		e.openPreview()
		e.View()
	}
	e := kiroEditor(t, 5, 5)
	e.View()
}

func TestLaunching(t *testing.T) {
	var quick, held strings.Builder
	Launching(&quick, "kiro-cli", "kiro-cli · profile default · network on · project read-write", 0)
	if got := quick.String(); got != " ✻ box  kiro-cli · profile default · network on · project read-write\n" {
		t.Errorf("a plain run prints one line, without colour codes off a terminal: %q", got)
	}
	Launching(&held, "kiro-cli", "summary", time.Nanosecond)
	want := " ✻ box  summary\n        launching kiro-cli in the sandbox" + strings.Repeat(" ·", LaunchSteps) + "\n"
	if held.String() != want {
		t.Errorf("after the editor: %q, want %q", held.String(), want)
	}
	if total := LaunchSteps * LaunchStep; total < time.Second || total > 3*time.Second {
		t.Errorf("the hold is %v; it should be a moment, not a wait", total)
	}
}

func TestEarlyKeysAreIgnored(t *testing.T) {
	keyGrace = 50 * time.Millisecond
	defer func() { keyGrace = 0 }()
	m := &confirm{title: "t", question: "q?"}
	m.Update(press('y')) // typed before the screen appeared
	m.View()
	m.Update(press('y')) // within the grace
	if m.yes {
		t.Fatal("a y typed before the question showed answered it")
	}
	time.Sleep(60 * time.Millisecond)
	m.Update(press('y'))
	if !m.yes {
		t.Error("y after the grace didn't answer")
	}
}

func TestSaveKeepsTextInAnOpenAddBox(t *testing.T) {
	e := newTestEditor(t)
	e.openInput("env")
	typeText(e, "EXTRA_VAR")
	clickOn(t, e, e.hitsFn(), "save")
	if e.result == nil || !slices.Contains(e.result.Profile.Env.Pass, "EXTRA_VAR") {
		t.Fatalf("the typed variable was dropped: %+v", e.result)
	}
	e = newTestEditor(t)
	e.openInput("env")
	typeText(e, "bad name")
	clickOn(t, e, e.hitsFn(), "save")
	if e.result != nil || e.inputKind != "env" || e.inputErr == "" {
		t.Errorf("an invalid add box should stop the save: result %v, kind %q, err %q", e.result, e.inputKind, e.inputErr)
	}
}

func TestCancelConfirmsFromTheKeyboard(t *testing.T) {
	e := newTestEditor(t)
	e.setFocus("wd:ro") // a change, so Cancel asks first
	e.Update(press(tea.KeySpace))
	e.setFocus("cancel")
	if _, cmd := e.Update(press(tea.KeyEnter)); cmd != nil {
		t.Fatal("the first Enter on Cancel should only warn")
	}
	if _, cmd := e.Update(press(tea.KeyEnter)); cmd == nil {
		t.Error("the second Enter on Cancel should leave")
	}
}

func TestControlCharactersAreShownEscaped(t *testing.T) {
	if got := Printable("a\x1b[31mb\n"); got != `a\x1b[31mb\x0a` {
		t.Errorf("Printable: %q", got)
	}
	e := newTestEditor(t)
	e.items = append(e.items, item{path: "/opt/\x1b]0;evil\x07x", on: true, extra: true})
	if v := e.View().Content; strings.Contains(v, "\x1b]0;evil") {
		t.Error("a raw escape sequence from a folder name reached the screen")
	}
}
