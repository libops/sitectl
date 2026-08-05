package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

const choiceListZoneID = "prompt:choices"

type Choice struct {
	Value            string
	Label            string
	Help             string
	AllowCustomInput bool
}

type ChoicePromptOptions struct {
	Name         string
	Sections     []string
	Choices      []Choice
	DefaultValue string
}

type choicePromptItem struct {
	title  string
	desc   string
	value  string
	custom bool
}

func (i choicePromptItem) Title() string       { return i.title }
func (i choicePromptItem) Description() string { return i.desc }
func (i choicePromptItem) FilterValue() string { return i.title + " " + i.desc }

type choicePromptDelegate struct {
	list.DefaultDelegate
}

func (d choicePromptDelegate) Render(w io.Writer, model list.Model, index int, item list.Item) {
	var rendered strings.Builder
	d.DefaultDelegate.Render(&rendered, model, index, item)
	_, _ = fmt.Fprint(w, zone.Mark(choiceItemZoneID(index), rendered.String()))
}

type choicePromptKeys struct {
	Confirm key.Binding
	Cancel  key.Binding
}

func (k choicePromptKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Confirm, k.Cancel}
}

func (k choicePromptKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Confirm, k.Cancel}}
}

type choicePromptModel struct {
	name      string
	sections  []string
	list      list.Model
	input     textinput.Model
	help      help.Model
	keys      choicePromptKeys
	width     int
	height    int
	cancelled bool
	value     string
}

func PromptChoice(opts ChoicePromptOptions) (string, bool, error) {
	if !CanPromptInteractively() {
		return "", false, nil
	}
	model := newChoicePromptModel(opts)
	resultModel, err := runPromptProgram(model)
	if err != nil {
		return "", true, err
	}

	result, ok := resultModel.(*choicePromptModel)
	if !ok {
		return "", true, fmt.Errorf("unexpected prompt result type %T", resultModel)
	}
	if result.cancelled {
		return "", true, fmt.Errorf("prompt cancelled")
	}
	return result.value, true, nil
}

func newChoicePromptModel(opts ChoicePromptOptions) *choicePromptModel {
	if zone.DefaultManager == nil {
		zone.NewGlobal()
	}
	items := make([]list.Item, 0, len(opts.Choices))
	for _, choice := range opts.Choices {
		desc := strings.TrimSpace(choice.Help)
		if desc == "" {
			desc = choice.Label
		}
		items = append(items, choicePromptItem{
			title:  choice.Label,
			desc:   desc,
			value:  choice.Value,
			custom: choice.AllowCustomInput,
		})
	}

	delegate := list.NewDefaultDelegate()
	stylePromptDelegate(&delegate)
	l := list.New(items, choicePromptDelegate{DefaultDelegate: delegate}, 0, 0)
	l.Title = opts.Name
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()

	input := textinput.New()
	input.Prompt = "custom> "
	input.Placeholder = "Enter a custom value"
	input.SetValue(defaultCustomInput(opts.Choices, opts.DefaultValue))
	stylePromptInput(&input)

	helpModel := help.New()
	helpModel.ShowAll = false

	m := &choicePromptModel{
		name:     opts.Name,
		sections: append([]string{}, opts.Sections...),
		list:     l,
		input:    input,
		help:     helpModel,
		keys: choicePromptKeys{
			Confirm: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
			Cancel:  key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "cancel")),
		},
		width:  80,
		height: 24,
	}
	m.selectDefault(opts.DefaultValue)
	m.syncInputFocus()
	return m
}

func (m *choicePromptModel) Init() tea.Cmd {
	if m.selectedCustom() {
		return m.input.Focus()
	}
	return nil
}

func (m *choicePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLayout()
		return m, nil

	case tea.MouseClickMsg:
		if msg.Mouse().Button != tea.MouseLeft {
			return m, nil
		}
		for index := range m.list.Items() {
			if itemZone := zone.Get(choiceItemZoneID(index)); itemZone != nil && itemZone.InBounds(msg) {
				m.list.Select(index)
				m.syncInputFocus()
				if item, ok := m.selectedItem(); ok && !item.custom {
					m.value = item.value
					return m, tea.Quit
				}
				return m, nil
			}
		}
		return m, nil

	case tea.MouseWheelMsg:
		listZone := zone.Get(choiceListZoneID)
		if listZone == nil || !listZone.InBounds(msg) {
			return m, nil
		}
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.list.CursorUp()
		case tea.MouseWheelDown:
			m.list.CursorDown()
		}
		m.syncInputFocus()
		return m, nil

	case tea.KeyPressMsg:
		navigationKey := isTextInputNavKey(msg)
		switch {
		case key.Matches(msg, m.keys.Cancel):
			m.cancelled = true
			return m, tea.Quit

		case key.Matches(msg, m.keys.Confirm):
			item, ok := m.selectedItem()
			if !ok {
				m.cancelled = true
				return m, tea.Quit
			}
			if item.custom {
				value := strings.TrimSpace(m.input.Value())
				if value != "" {
					m.value = value
				} else {
					m.value = item.value
				}
			} else {
				m.value = item.value
			}
			return m, tea.Quit
		}

		if m.selectedCustom() && !isNavigationKey(msg.String()) {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}

		prevIndex := m.list.Index()
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		if m.list.Index() != prevIndex {
			m.syncInputFocus()
		}

		if m.selectedCustom() && navigationKey {
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			cmd = tea.Batch(cmd, inputCmd)
		}
		return m, cmd
	}

	prevIndex := m.list.Index()
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	if m.list.Index() != prevIndex {
		m.syncInputFocus()
	}
	return m, cmd
}

func (m *choicePromptModel) View() tea.View {
	headerParts := make([]string, 0, len(m.sections)+2)
	headerParts = append(headerParts, m.sections...)
	if len(headerParts) > 0 {
		headerParts = append(headerParts, "")
	}

	bodyParts := []string{zone.Mark(choiceListZoneID, m.list.View())}
	if m.selectedCustom() {
		bodyParts = append(bodyParts, "", customBoxStyle.Width(promptLayoutWidth(m.width)).Render(m.input.View()))
	}
	bodyParts = append(bodyParts, "", footerHelpStyle.Render("click: choose  wheel: scroll  "+m.help.View(m.keys)))

	content := lipgloss.JoinVertical(lipgloss.Left, append(headerParts, bodyParts...)...)
	view := promptView(content, m.width)
	view.Content = zone.Scan(view.Content)
	return view
}

func choiceItemZoneID(index int) string {
	return fmt.Sprintf("prompt:choice:%d", index)
}

func (m *choicePromptModel) syncLayout() {
	w := promptLayoutWidth(m.width)
	m.help.SetWidth(w)

	headerHeight := 0
	for _, section := range m.sections {
		headerHeight += lipgloss.Height(section)
	}
	if headerHeight > 0 {
		headerHeight++
	}
	inputHeight := 0
	if m.selectedCustom() {
		inputHeight = 6
	}
	listHeight := clampInt(m.height-headerHeight-inputHeight-10, 4, 18)
	m.list.SetSize(w, listHeight)
	m.input.SetWidth(w - 8)
}

func (m *choicePromptModel) selectDefault(defaultValue string) {
	for i, item := range m.list.Items() {
		candidate, ok := item.(choicePromptItem)
		if ok && candidate.value == defaultValue {
			m.list.Select(i)
			return
		}
	}
}

func (m *choicePromptModel) selectedItem() (choicePromptItem, bool) {
	item, ok := m.list.SelectedItem().(choicePromptItem)
	return item, ok
}

func (m *choicePromptModel) selectedCustom() bool {
	item, ok := m.selectedItem()
	return ok && item.custom
}

func (m *choicePromptModel) syncInputFocus() {
	if m.selectedCustom() {
		_ = m.input.Focus()
		return
	}
	m.input.Blur()
}

func defaultCustomInput(choices []Choice, defaultValue string) string {
	trimmed := strings.TrimSpace(defaultValue)
	if trimmed == "" {
		return ""
	}
	customSelected := false
	for _, choice := range choices {
		if choice.AllowCustomInput {
			customSelected = true
			continue
		}
		if trimmed == choice.Value {
			return ""
		}
	}
	if !customSelected {
		return ""
	}
	return trimmed
}

func isNavigationKey(key string) bool {
	switch key {
	case "up", "down", "j", "k", "pgup", "pgdown", "home", "end":
		return true
	default:
		return false
	}
}

func isTextInputNavKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "left", "right", "backspace", "delete", "ctrl+h", "ctrl+u", "ctrl+k", "home", "end":
		return true
	default:
		return false
	}
}

func clampInt(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

var (
	customBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#486581")).
			Padding(1, 2)
	footerHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7C98B3"))
)
