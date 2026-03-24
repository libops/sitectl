package debugui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

type Row struct {
	Label string
	Value string
}

// MutedStyle is the lipgloss style used for de-emphasised text.
// Exposed for callers that need the raw Style (e.g. spinner.WithStyle).
var MutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9FB3C8"))

var (
	panelStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#112235")).
			Padding(1, 2)
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#98C1D9"))
	mutedStyle          = MutedStyle
	sectionDividerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#29425E"))
	statusOKStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7BD389"))
	statusWarningStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#F4C95D"))
	statusFailedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#F28482"))
	rowStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#112235"))
)

func RenderPanel(title, body string) string {
	header := Title(title)
	content := header
	if strings.TrimSpace(body) != "" {
		content += "\n\n" + body
	}
	return panelStyle.Width(PanelWidth()).Render(content)
}

func FormatRows(rows []Row) string {
	labelWidth := 0
	for _, row := range rows {
		if width := len(strings.TrimSpace(row.Label)); width > labelWidth {
			labelWidth = width
		}
	}

	lines := make([]string, 0, len(rows))
	rowWidth := ContentWidth()
	for _, row := range rows {
		label := strings.TrimSpace(row.Label)
		value := strings.TrimSpace(row.Value)
		if label == "" {
			lines = append(lines, renderRow(rowWidth, "", value))
			continue
		}
		lines = append(lines, renderRow(rowWidth, fmt.Sprintf("%-*s", labelWidth, label), value))
	}
	return strings.Join(lines, "\n")
}

func Title(text string) string {
	return titleStyle.Render(strings.TrimSpace(text))
}

func Muted(text string) string {
	return mutedStyle.Render(text)
}

func Divider() string {
	return sectionDividerStyle.Width(ContentWidth()).Render(strings.Repeat("─", ContentWidth()))
}

func Status(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "ok":
		return statusOKStyle.Render("OK")
	case "warning":
		return statusWarningStyle.Render("WARNING")
	case "failed":
		return statusFailedStyle.Render("FAILED")
	default:
		return mutedStyle.Render(strings.ToUpper(strings.TrimSpace(state)))
	}
}

func PanelWidth() int {
	if columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS"))); err == nil && columns > 0 {
		return max(40, columns)
	}
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return max(40, width)
	}
	return 100
}

func ContentWidth() int {
	return max(20, PanelWidth()-4)
}

func renderRow(width int, label, value string) string {
	valueWidth := max(0, width-lipgloss.Width(label)-2)
	row := label
	if strings.TrimSpace(label) != "" {
		row += "  "
	}
	row += lipgloss.NewStyle().
		Width(valueWidth).
		Background(lipgloss.Color("#112235")).
		Render(value)
	return rowStyle.Width(width).Render(row)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
