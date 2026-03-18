package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type TextPromptOptions struct {
	Sections []string
	Prompt   string
}

type textPromptKeys struct {
	Confirm key.Binding
	Cancel  key.Binding
}

func (k textPromptKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Confirm, k.Cancel}
}

func (k textPromptKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Confirm, k.Cancel}}
}

type textPromptModel struct {
	sections  []string
	prompt    string
	input     textinput.Model
	help      help.Model
	keys      textPromptKeys
	width     int
	height    int
	cancelled bool
	value     string
}

func PromptText(opts TextPromptOptions) (string, bool, error) {
	model := newTextPromptModel(opts)
	resultModel, err := tea.NewProgram(model).Run()
	if err != nil {
		return "", true, err
	}

	result, ok := resultModel.(*textPromptModel)
	if !ok {
		return "", true, fmt.Errorf("unexpected prompt result type %T", resultModel)
	}
	if result.cancelled {
		return "", true, fmt.Errorf("prompt cancelled")
	}
	return strings.TrimSpace(result.value), true, nil
}

func newTextPromptModel(opts TextPromptOptions) *textPromptModel {
	input := textinput.New()
	input.Prompt = strings.TrimSpace(opts.Prompt) + " "
	input.Focus()

	helpModel := help.New()
	helpModel.ShowAll = false

	return &textPromptModel{
		sections: append([]string{}, opts.Sections...),
		prompt:   opts.Prompt,
		input:    input,
		help:     helpModel,
		keys: textPromptKeys{
			Confirm: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
			Cancel:  key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "cancel")),
		},
		width:  80,
		height: 24,
	}
}

func (m *textPromptModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *textPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLayout()
		return m, nil

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Cancel):
			m.cancelled = true
			return m, tea.Quit
		case key.Matches(msg, m.keys.Confirm):
			m.value = m.input.Value()
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *textPromptModel) View() tea.View {
	headerParts := make([]string, 0, len(m.sections)+2)
	headerParts = append(headerParts, m.sections...)
	if len(headerParts) > 0 {
		headerParts = append(headerParts, "")
	}

	inputBox := customBoxStyle.Width(max(20, m.width-6)).Render(m.input.View())
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		append(headerParts, inputBox, "", footerHelpStyle.Render(m.help.View(m.keys)))...,
	)
	return tea.NewView(promptDocStyle.Render(content))
}

func (m *textPromptModel) syncLayout() {
	w := clampInt(m.width-4, 40, 100)
	m.help.SetWidth(w)
	m.input.SetWidth(w - 8)
}
