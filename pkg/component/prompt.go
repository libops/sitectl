package component

import (
	"fmt"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/libops/sitectl/pkg/config"
	"golang.org/x/term"
)

type InputFunc func(question ...string) (string, error)

type StateGuidance struct {
	Question     string
	OnHelp       string
	OffHelp      string
	DefaultState State
}

type Choice struct {
	Value            string
	Label            string
	Help             string
	Aliases          []string
	AllowCustomInput bool
}

const defaultRenderWidth = 80

var (
	sectionTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	introTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69"))
	introBoxStyle     = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("238")).
				Background(lipgloss.Color("236")).
				Padding(1, 2)
	questionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	onLabelStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	offLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	promptStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	okStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	failStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	infoStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229"))
	commandStyle  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Background(lipgloss.Color("235")).
			Padding(1, 2)
)

func PromptState(name string, guidance StateGuidance, input InputFunc) (State, error) {
	if input == nil {
		input = config.GetInput
	}

	defaultState := normalizeState(guidance.DefaultState)
	if defaultState == "" {
		defaultState = StateOn
	}

	sections := []string{}
	title := strings.TrimSpace(name)
	if guidance.Question != "" {
		sections = append(sections, strings.Split(RenderSection(title, guidance.Question), "\n")...)
	} else if title != "" {
		sections = append(sections, sectionTitleStyle.Render(title))
	}

	choice, err := PromptChoice(name, renderStateChoices(guidance), stateChoiceValue(defaultState), input, sections...)
	if err != nil {
		return "", err
	}

	state, err := ParseState(choice)
	if err != nil {
		return "", fmt.Errorf("invalid %s value %q: %w", name, choice, err)
	}
	return state, nil
}

func PromptChoice(name string, choices []Choice, defaultValue string, input InputFunc, sections ...string) (string, error) {
	if interactiveValue, ok, err := promptChoiceInteractive(name, choices, defaultValue, sections); ok {
		return interactiveValue, err
	}

	if input == nil {
		input = config.GetInput
	}

	lines := []string{}
	lines = append(lines, sections...)
	lines = append(lines, renderChoiceLines(name, choices, defaultValue)...)
	value, err := input(lines...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}

	choice, ok := matchChoice(choices, value)
	if !ok {
		if custom := customChoice(choices); custom != nil {
			return value, nil
		}
		valid := make([]string, 0, len(choices))
		for _, choice := range choices {
			valid = append(valid, choice.Value)
		}
		return "", fmt.Errorf("invalid %s value %q: expected one of %s", name, value, strings.Join(valid, ", "))
	}
	return choice.Value, nil
}

func promptChoiceInteractive(name string, choices []Choice, defaultValue string, sections []string) (string, bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return "", false, nil
	}
	if len(choices) == 0 {
		return "", false, fmt.Errorf("no choices configured for %s", name)
	}

	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return "", false, nil
	}
	defer func() {
		_ = term.Restore(fd, oldState)
	}()

	selected := defaultChoiceIndex(choices, defaultValue)
	customInput := defaultCustomInput(choices, defaultValue)
	lines := renderInteractiveChoiceLines(choices, selected, customInput)
	hint := truncateStyledLine(mutedStyle.Render("Use up/down to select, Enter to confirm"), promptRenderWidth())

	if len(sections) > 0 {
		fmt.Fprint(os.Stdout, strings.Join(sections, "\r\n")+"\r\n")
	}
	staticLines := []string{}
	staticLines = append(staticLines, hint, "")
	fmt.Fprint(os.Stdout, "\r\n"+strings.Join(staticLines, "\r\n")+"\r\n")
	fmt.Fprint(os.Stdout, "\x1b[s")
	fmt.Fprint(os.Stdout, strings.Join(lines, "\r\n"))
	renderedLineCount := len(lines)

	for {
		var buf [3]byte
		n, err := os.Stdin.Read(buf[:])
		if err != nil {
			fmt.Fprintln(os.Stdout)
			return "", true, err
		}
		if n == 0 {
			continue
		}

		switch {
		case n == 1 && (buf[0] == '\r' || buf[0] == '\n'):
			fmt.Fprint(os.Stdout, "\r\n")
			if choices[selected].AllowCustomInput {
				value := strings.TrimSpace(customInput)
				if value != "" {
					return value, true, nil
				}
			}
			return choices[selected].Value, true, nil
		case n == 1 && buf[0] == 3:
			fmt.Fprint(os.Stdout, "\r\n")
			return "", true, fmt.Errorf("prompt cancelled")
		case n == 1 && (buf[0] == 127 || buf[0] == 8):
			if choices[selected].AllowCustomInput && len(customInput) > 0 {
				customInput = customInput[:len(customInput)-1]
			}
		case n >= 3 && buf[0] == 27 && buf[1] == 91 && buf[2] == 65:
			selected = (selected - 1 + len(choices)) % len(choices)
		case n >= 3 && buf[0] == 27 && buf[1] == 91 && buf[2] == 66:
			selected = (selected + 1) % len(choices)
		default:
			if choices[selected].AllowCustomInput && isPrintableInput(buf[:n]) {
				customInput += string(buf[:n])
			}
		}

		fmt.Fprint(os.Stdout, "\x1b[u")
		for i := 0; i < renderedLineCount; i++ {
			fmt.Fprint(os.Stdout, "\r\x1b[2K\x1b[1B")
		}
		fmt.Fprint(os.Stdout, "\x1b[u")
		lines = renderInteractiveChoiceLines(choices, selected, customInput)
		fmt.Fprint(os.Stdout, strings.Join(lines, "\r\n"))
		renderedLineCount = len(lines)
	}
}

func RenderSection(title, body string) string {
	lines := []string{}
	if strings.TrimSpace(title) != "" {
		lines = append(lines, sectionTitleStyle.Render(strings.ToUpper(strings.TrimSpace(title))))
		lines = append(lines, "")
	}
	if strings.TrimSpace(body) != "" {
		width := promptRenderWidth()
		for _, line := range strings.Split(wrapText(strings.TrimSpace(body), width-2), "\n") {
			lines = append(lines, "  "+questionStyle.Render(line))
		}
	}
	return strings.Join(lines, "\n")
}

func RenderIntroSection(title, body string) string {
	lines := []string{}
	if strings.TrimSpace(title) != "" {
		lines = append(lines, introTitleStyle.Render(strings.TrimSpace(title)))
		lines = append(lines, "")
	}
	if strings.TrimSpace(body) != "" {
		width := promptRenderWidth()
		lines = append(lines, introBoxStyle.Render(strings.TrimSpace(wrapText(strings.TrimSpace(body), width-8))))
	}
	return strings.Join(lines, "\n")
}

func RenderPromptLine(text string) string {
	return promptStyle.Render(text)
}

func RenderChecklistItem(label, state, detail string) string {
	prefix := mutedStyle.Render("  • ")
	stateStyle := infoStyle
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "ok":
		stateStyle = okStyle
	case "failed":
		stateStyle = failStyle
	case "fallback":
		stateStyle = infoStyle
	}

	line := prefix + questionStyle.Render(strings.TrimSpace(label)) + mutedStyle.Render(": ") + stateStyle.Render(state)
	if strings.TrimSpace(detail) != "" {
		line += mutedStyle.Render("  " + strings.TrimSpace(detail))
	}
	return line
}

func RenderCommandBlock(text string) string {
	return commandStyle.Render(text)
}

func renderStateChoices(guidance StateGuidance) []Choice {
	return []Choice{
		{
			Value:   string(StateOn),
			Label:   "on",
			Help:    strings.TrimSpace(guidance.OnHelp),
			Aliases: []string{"y", "yes", "1"},
		},
		{
			Value:   string(StateOff),
			Label:   "off",
			Help:    strings.TrimSpace(guidance.OffHelp),
			Aliases: []string{"n", "no", "2"},
		},
	}
}

func renderChoiceLines(name string, choices []Choice, defaultValue string) []string {
	lines := []string{}
	width := promptRenderWidth()
	for index, choice := range choices {
		labelStyle := infoStyle
		switch choice.Value {
		case string(StateOn):
			labelStyle = onLabelStyle
		case string(StateOff):
			labelStyle = offLabelStyle
		}
		help := strings.TrimSpace(choice.Help)
		if help == "" {
			help = choice.Label
		}
		lines = append(lines, wrapPrefixedText(
			fmt.Sprintf("  %d. %s  ", index+1, labelStyle.Render(choice.Label)),
			help,
			width,
		))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	labels := make([]string, 0, len(choices))
	for _, choice := range choices {
		labels = append(labels, choice.Label)
	}
	defaultLabel := defaultValue
	for _, choice := range choices {
		if choice.Value == defaultValue {
			defaultLabel = choice.Label
			break
		}
	}
	lines = append(lines, promptStyle.Render(
		fmt.Sprintf("Choose %s (%s) [%s]: ", name, strings.Join(labels, "/"), defaultLabel),
	))
	return lines
}

func renderInteractiveChoiceLines(choices []Choice, selected int, customInput string) []string {
	lines := []string{}
	width := promptRenderWidth()
	for index, choice := range choices {
		labelStyle := infoStyle
		switch choice.Value {
		case string(StateOn):
			labelStyle = onLabelStyle
		case string(StateOff):
			labelStyle = offLabelStyle
		}

		prefix := "  "
		if index == selected {
			prefix = selectedStyle.Render("> ")
		}

		help := strings.TrimSpace(choice.Help)
		if choice.AllowCustomInput {
			help = strings.TrimSpace(customInput)
			if index == selected && help == "" {
				help = mutedStyle.Render("|")
			}
		} else if help == "" {
			help = choice.Label
		}
		lines = append(lines, strings.Split(wrapPrefixedText(
			prefix+labelStyle.Render(choice.Label)+"  ",
			help,
			width,
		), "\n")...)
	}
	return lines
}

func matchChoice(choices []Choice, value string) (Choice, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, choice := range choices {
		candidates := append([]string{choice.Value, choice.Label}, choice.Aliases...)
		for _, candidate := range candidates {
			if normalized == strings.ToLower(strings.TrimSpace(candidate)) {
				return choice, true
			}
		}
	}
	return Choice{}, false
}

func defaultChoiceIndex(choices []Choice, defaultValue string) int {
	for index, choice := range choices {
		if choice.Value == defaultValue {
			return index
		}
	}
	return 0
}

func customChoice(choices []Choice) *Choice {
	for i := range choices {
		if choices[i].AllowCustomInput {
			return &choices[i]
		}
	}
	return nil
}

func defaultCustomInput(choices []Choice, defaultValue string) string {
	custom := customChoice(choices)
	if custom == nil {
		return ""
	}
	trimmed := strings.TrimSpace(defaultValue)
	if trimmed == "" || trimmed == custom.Value {
		return ""
	}
	for _, choice := range choices {
		if !choice.AllowCustomInput && trimmed == choice.Value {
			return ""
		}
	}
	return trimmed
}

func isPrintableInput(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	for _, b := range value {
		if b < 32 || b == 127 {
			return false
		}
	}
	return true
}

func stateChoiceValue(state State) string {
	if normalizeState(state) == StateOff {
		return string(StateOff)
	}
	return string(StateOn)
}

func promptRenderWidth() int {
	width := defaultRenderWidth
	if term.IsTerminal(int(os.Stdout.Fd())) {
		if terminalWidth, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && terminalWidth > 0 {
			width = terminalWidth
		}
	}
	if width < 40 {
		return 40
	}
	if width > 100 {
		return 100
	}
	return width
}

func truncateStyledLine(value string, width int) string {
	if width <= 0 {
		return value
	}
	if visibleWidth(value) <= width {
		return value
	}

	plain := stripANSIEscape(value)
	if len(plain) <= width {
		return plain
	}
	if width <= 1 {
		return plain[:width]
	}
	return plain[:width-1] + "…"
}

func stripANSIEscape(value string) string {
	var out strings.Builder
	out.Grow(len(value))

	inEscape := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch == '\x1b':
			inEscape = true
		case inEscape && ch == 'm':
			inEscape = false
		case !inEscape:
			out.WriteByte(ch)
		}
	}
	return out.String()
}

func wrapText(text string, width int) string {
	return wrapPrefixedText("", text, width)
}

func wrapPrefixedText(prefix, text string, width int) string {
	parts := strings.Split(text, "\n")
	wrapped := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			wrapped = append(wrapped, strings.TrimRight(prefix, " "))
			continue
		}
		wrapped = append(wrapped, wrapPrefixedParagraph(prefix, part, width))
	}
	return strings.Join(wrapped, "\n")
}

func wrapPrefixedParagraph(prefix, text string, width int) string {
	if width <= visibleWidth(prefix)+1 {
		return prefix + text
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return strings.TrimRight(prefix, " ")
	}

	indent := strings.Repeat(" ", visibleWidth(prefix))
	lines := []string{}
	current := prefix
	currentWidth := visibleWidth(prefix)

	for _, word := range words {
		wordWidth := len(word)
		if currentWidth > visibleWidth(prefix) && currentWidth+1+wordWidth > width {
			lines = append(lines, current)
			current = indent + word
			currentWidth = visibleWidth(indent) + wordWidth
			continue
		}
		if currentWidth == visibleWidth(prefix) {
			current += word
			currentWidth += wordWidth
			continue
		}
		current += " " + word
		currentWidth += 1 + wordWidth
	}

	lines = append(lines, current)
	return strings.Join(lines, "\n")
}

func visibleWidth(value string) int {
	width := 0
	inEscape := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		switch {
		case ch == '\x1b':
			inEscape = true
		case inEscape && ch == 'm':
			inEscape = false
		case !inEscape:
			width++
		}
	}
	return width
}
