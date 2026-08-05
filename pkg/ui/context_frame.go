package ui

import (
	"os"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

const (
	// PromptOutputEnvironment selects the terminal stream used by interactive prompts.
	PromptOutputEnvironment = "SITECTL_PROMPT_OUTPUT"
	// PromptOutputStderr keeps stdout available for machine-readable protocols such as plugin RPC.
	PromptOutputStderr = "stderr"
)

var (
	promptFrameBackground = lipgloss.Color("#0D1B2A")
	promptFrameForeground = lipgloss.Color("#D9E2EC")
	promptFrameAccent     = lipgloss.Color("#F4A261")
	promptFrameBorder     = lipgloss.Color("#486581")
	promptFrameMuted      = lipgloss.Color("#7C98B3")

	promptFrameDocStyle = lipgloss.NewStyle().
				Padding(1, 2).
				Foreground(promptFrameForeground).
				Background(promptFrameBackground)
	promptFrameTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E0FBFC"))
	promptFramePanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(promptFrameBorder).
				Padding(1, 2)
	promptFrameMutedStyle = lipgloss.NewStyle().Foreground(promptFrameMuted)
)

func promptView(content string, width int) tea.View {
	frameWidth := clampInt(width-6, 40, 96)
	title := promptFrameTitleStyle.Render(" Sitectl | Interactive ")
	rule := promptFrameMutedStyle.Render(strings.Repeat("-", max(2, frameWidth-lipgloss.Width(title))))
	body := lipgloss.JoinVertical(
		lipgloss.Left,
		title+rule,
		promptFramePanelStyle.Width(frameWidth).Render(content),
		promptFrameMutedStyle.Render("Guided workflow · enter: confirm · esc: cancel"),
	)
	v := tea.NewView(promptFrameDocStyle.Render(body))
	v.AltScreen = true
	v.BackgroundColor = promptFrameBackground
	v.ForegroundColor = promptFrameForeground
	v.WindowTitle = "sitectl · interactive"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func CanPromptInteractively() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	return term.IsTerminal(int(promptOutputFile().Fd()))
}

func PromptTerminalWidth() (int, bool) {
	width, _, err := term.GetSize(int(promptOutputFile().Fd()))
	return width, err == nil && width > 0
}

func runPromptProgram(model tea.Model) (tea.Model, error) {
	options := []tea.ProgramOption{tea.WithInput(os.Stdin)}
	if promptOutputFile() == os.Stderr {
		options = append(options, tea.WithOutput(os.Stderr))
	}
	return tea.NewProgram(model, options...).Run()
}

func promptOutputFile() *os.File {
	if os.Getenv(PromptOutputEnvironment) == PromptOutputStderr {
		return os.Stderr
	}
	return os.Stdout
}

func promptLayoutWidth(width int) int {
	return clampInt(width-14, 32, 88)
}

func stylePromptInput(input *textinput.Model) {
	styles := input.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Bold(true).Foreground(promptFrameAccent)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(promptFrameForeground)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(promptFrameMuted)
	styles.Blurred = styles.Focused
	styles.Cursor.Color = promptFrameAccent
	input.SetStyles(styles)
}

func stylePromptDelegate(delegate *list.DefaultDelegate) {
	delegate.Styles.NormalTitle = lipgloss.NewStyle().Foreground(promptFrameForeground).PaddingLeft(2)
	delegate.Styles.NormalDesc = lipgloss.NewStyle().Foreground(promptFrameMuted).PaddingLeft(2)
	delegate.Styles.SelectedTitle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(promptFrameAccent).
		Bold(true).
		Foreground(promptFrameAccent).
		PaddingLeft(1)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedTitle.Bold(false).Foreground(lipgloss.Color("#98C1D9"))
}
