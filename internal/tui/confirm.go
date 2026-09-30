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

func (m *confirm) Init() tea.Cmd { return nil }

func (m *confirm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y", "Y":
			m.yes = true
			return m, tea.Quit
		case "n", "N", "esc", "q", "ctrl+c":
			return m, tea.Quit
		case "left", "right", "tab", "shift+tab", "h", "l":
			m.onYes = !m.onYes
		case "enter", "space":
			m.yes = m.onYes
			return m, tea.Quit
		}
	case tea.MouseClickMsg:
		if msg.Mouse().Button != tea.MouseLeft || m.hits == nil {
			return m, nil
		}
		switch m.hits.Hit(msg.Mouse().X, msg.Mouse().Y).ID() {
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
	ls := []line{{styled(styleTitle, " "+m.title)}, {}}
	for _, b := range m.body {
		ls = append(ls, line{txt("   " + b)})
	}
	yes, no := "[ Yes ]", "[ No ]"
	if m.onYes {
		yes = styleFocus.Render(yes)
	} else {
		no = styleFocus.Render(no)
	}
	ls = append(ls, line{},
		line{styled(styleWarn, " "+m.question)},
		line{},
		line{txt("   "), seg{text: yes, id: "yes"}, txt("   "), seg{text: no, id: "no"}},
		line{},
		line{styled(styleDim, " y / n · ←→ and Enter · click · Esc means no")})
	var content string
	content, m.hits = render(ls, 0, 0)
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
