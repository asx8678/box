package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Choice is one option in the picker.
type Choice struct {
	Name   string
	Detail string // one-line summary
}

// Pick shows a one-screen list and returns the chosen name. It returns
// ErrCancelled if the user leaves without choosing.
func Pick(title string, choices []Choice) (string, error) {
	m := &picker{title: title, choices: choices, chosen: -1}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return "", err
	}
	if p := final.(*picker); p.chosen >= 0 {
		return choices[p.chosen].Name, nil
	}
	return "", ErrCancelled
}

type picker struct {
	title   string
	choices []Choice
	cursor  int
	chosen  int
	width   int
	hits    *lipgloss.Compositor
}

func (m *picker) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.BackgroundColorMsg:
		applyTheme(msg.IsDark())
	case tea.MouseMotionMsg:
		// Like a native list: the row under the mouse is selected.
		if m.hits != nil {
			if i, err := strconv.Atoi(strings.TrimPrefix(m.hits.Hit(msg.Mouse().X, msg.Mouse().Y).ID(), "c:")); err == nil {
				m.cursor = i
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k", "shift+tab":
			m.cursor = (m.cursor - 1 + len(m.choices)) % len(m.choices)
		case "down", "j", "tab":
			m.cursor = (m.cursor + 1) % len(m.choices)
		case "enter", "space":
			m.chosen = m.cursor
			return m, tea.Quit
		case "esc", "q", "ctrl+c":
			return m, tea.Quit
		default:
			// 1–9 pick directly.
			if n, err := strconv.Atoi(msg.String()); err == nil && n >= 1 && n <= len(m.choices) {
				m.chosen = n - 1
				return m, tea.Quit
			}
		}
	case tea.MouseClickMsg:
		if msg.Mouse().Button != tea.MouseLeft || m.hits == nil {
			return m, nil
		}
		id := m.hits.Hit(msg.Mouse().X, msg.Mouse().Y).ID()
		if id == "cancel" {
			return m, tea.Quit
		}
		if i, err := strconv.Atoi(strings.TrimPrefix(id, "c:")); err == nil {
			m.chosen = i
			return m, tea.Quit
		}
	case tea.MouseWheelMsg:
		if msg.Mouse().Button == tea.MouseWheelUp {
			m.cursor = max(0, m.cursor-1)
		} else {
			m.cursor = min(len(m.choices)-1, m.cursor+1)
		}
	}
	return m, nil
}

func (m *picker) View() tea.View {
	w := 80
	if m.width > 0 {
		w = min(max(m.width, 50), 100)
	}
	nameWidth := 8
	var names, details []string
	for _, c := range m.choices {
		nameWidth = max(nameWidth, lipgloss.Width(c.Name)+3)
	}
	for _, c := range m.choices {
		names = append(names, pad(c.Name, nameWidth))
		details = append(details, c.Detail)
	}
	ls := banner(m.title, line{styled(styleDim, fmt.Sprintf("%d profiles, none remembered for this folder", len(m.choices)))}, w)
	ls = append(ls, line{}, section("Which profile should run here?", ""), line{})
	ls = append(ls, menu(names, details, m.cursor, "c:", w)...)
	ls = append(ls, line{}, line{txt("   "), seg{text: styleDim.Render("Cancel"), id: "cancel"}}, line{})
	ls = append(ls, hints(w, "↑↓", "to move", "enter", "to run", "1–9", "to pick", "esc", "to cancel")...)
	var content string
	content, m.hits = render(ls, 0, 0)
	return screen(content, "box")
}
