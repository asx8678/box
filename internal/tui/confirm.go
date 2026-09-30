package tui

import (
	tea "charm.land/bubbletea/v2"
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
	page
	title, question string
	body            []string
	onYes           bool // which button has the focus
	yes             bool
}

func (m *confirm) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (m *confirm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.page.update(msg) {
		return m, nil
	}
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
	case tea.MouseClickMsg:
		switch m.clicked(msg) {
		case "yes":
			m.yes = true
			return m, tea.Quit
		case "no":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *confirm) View() tea.View {
	w := min(layoutWidth(m.width), 76)
	ls := banner(m.title, line{styled(styleWarn, "⚠ this can't be undone")}, w)
	ls = append(ls, line{})
	for _, b := range m.body {
		ls = append(ls, indent(wrap(b, stylePlain, w-4), 1)...)
	}
	ls = append(ls, line{}, line{txt(" "), styled(styleBold, m.question)}, line{}, rule(w), line{})
	buttons := line{
		seg{text: btn("No", secondary, !m.onYes, m.hover == "no"), id: "no"}, txt("  "),
		seg{text: btn("Yes", danger, m.onYes, m.hover == "yes"), id: "yes"}, txt(" "),
	}
	ls = append(ls, rightAlign(buttons, w), line{})
	ls = append(ls, hints(w, "y", "yes", "n", "no", "←→", "switch", "enter", "press", "esc", "no")...)
	return m.draw(ls, "box")
}
