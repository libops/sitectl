package component

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/libops/sitectl/pkg/config"
)

type InputFunc func(question ...string) (string, error)

type StateGuidance struct {
	Question     string
	OnHelp       string
	OffHelp      string
	DefaultState State
}

const renderWidth = 80

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
)

func PromptState(name string, guidance StateGuidance, input InputFunc) (State, error) {
	if input == nil {
		input = config.GetInput
	}

	defaultState := normalizeState(guidance.DefaultState)
	if defaultState == "" {
		defaultState = StateOn
	}

	lines := renderStatePrompt(name, guidance, defaultState)
	value, err := input(lines...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return defaultState, nil
	}

	state, err := ParseState(value)
	if err != nil {
		return "", fmt.Errorf("invalid %s value %q: %w", name, value, err)
	}
	return state, nil
}

func RenderSection(title, body string) string {
	lines := []string{}
	if strings.TrimSpace(title) != "" {
		lines = append(lines, sectionTitleStyle.Render(strings.ToUpper(strings.TrimSpace(title))))
		lines = append(lines, "")
	}
	if strings.TrimSpace(body) != "" {
		lines = append(lines, "  "+strings.ReplaceAll(questionStyle.Render(wrapText(strings.TrimSpace(body), renderWidth-2)), "\n", "\n  "))
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
		lines = append(lines, introBoxStyle.Render(wrapText(strings.TrimSpace(body), renderWidth-8)))
	}
	return strings.Join(lines, "\n")
}

func RenderPromptLine(text string) string {
	return promptStyle.Render(text)
}

func renderStatePrompt(name string, guidance StateGuidance, defaultState State) []string {
	lines := []string{}
	title := strings.TrimSpace(name)
	if guidance.Question != "" {
		lines = append(lines, strings.Split(RenderSection(title, guidance.Question), "\n")...)
	} else if title != "" {
		lines = append(lines, sectionTitleStyle.Render(title))
	}
	if guidance.OnHelp != "" {
		lines = append(lines, wrapPrefixedText(
			fmt.Sprintf("  %s  ", onLabelStyle.Render("on")),
			strings.TrimSpace(guidance.OnHelp),
			renderWidth,
		))
	}
	if guidance.OffHelp != "" {
		lines = append(lines, wrapPrefixedText(
			fmt.Sprintf("  %s ", offLabelStyle.Render("off")),
			strings.TrimSpace(guidance.OffHelp),
			renderWidth,
		))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, promptStyle.Render(fmt.Sprintf("Choose %s (%s/%s) [%s]: ", name, StateOn, StateOff, defaultState)))
	return lines
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
