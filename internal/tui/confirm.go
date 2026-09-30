package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Confirm asks a yes/no question on one screen. No is the default: only an
// explicit yes (y, a click on Yes, or Enter on Yes) returns true.
func Confirm(title string, body []string, question string) (bool, error) {
	m := &confirm{title: title, body: body, question: question}
	final, err := tea.NewProgram(m).Run()
	if err != nil {
		return false, err
	}
	return final.(*confirm).yes, nil
}

type confirm struct {
	title, question string
	body            []string
	onYes           bool // which button has focus
	yes             bool
	hits            *lipgloss.Compositor
}

func (m *confirm) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m *confirm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y", "Y":
			m.yes = true
			return m, tea.Quit
		case "n", "N", "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "left", "right", "up", "down", "tab", "shift+tab", "h", "l", "j", "k":
			m.onYes = !m.onYes
		case "enter", "space":
			m.yes = m.onYes
			return m, tea.Quit
		}
	case tea.BackgroundColorMsg:
		applyTheme(msg.IsDark())
	case tea.MouseMotionMsg:
		if m.hits != nil {
			switch m.hits.Hit(msg.Mouse().X, msg.Mouse().Y).ID() {
			case "o:0":
				m.onYes = true
			case "o:1":
				m.onYes = false
			}
		}
	case tea.MouseClickMsg:
		if msg.Mouse().Button != tea.MouseLeft || m.hits == nil {
			return m, nil
		}
		switch m.hits.Hit(msg.Mouse().X, msg.Mouse().Y).ID() {
		case "o:0":
			m.yes = true
			return m, tea.Quit
		case "o:1":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *confirm) View() tea.View {
	const w = 72
	ls := banner(m.title, line{styled(styleWarn, "this can't be undone")}, w)
	ls = append(ls, line{})
	for _, b := range m.body {
		for _, l := range wrap(b, stylePlain, w-4) {
			ls = append(ls, append(line{txt(" ")}, l...))
		}
	}
	selected := 1
	if m.onYes {
		selected = 0
	}
	ls = append(ls, line{}, section(m.question, ""), line{})
	ls = append(ls, menu([]string{"Yes", "No"}, nil, selected, "o:", w)...)
	ls = append(ls, line{})
	ls = append(ls, hints(w, "y/n", "to answer", "↑↓", "to move", "enter", "to choose", "esc", "means no")...)
	var content string
	content, m.hits = render(ls, 0, 0)
	return screen(content, "box")
}
