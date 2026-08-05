package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
)

var (
	docStyle = lipgloss.NewStyle().
			Padding(1, 2).
			Foreground(lipgloss.Color("#D9E2EC")).
			Background(lipgloss.Color("#0D1B2A"))

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E0FBFC"))

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7C98B3"))

	accentStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#F4A261"))

	sectionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#98C1D9")).
				MarginBottom(1)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#486581")).
			Padding(1, 2).
			MarginRight(1).
			MarginBottom(1)

	contextCardStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#486581")).
				Padding(0, 1).
				MarginRight(1)

	activeContextCardStyle = contextCardStyle.
				BorderForeground(lipgloss.Color("#F4A261"))

	addContextCardStyle = contextCardStyle.
				BorderForeground(lipgloss.Color("#5F7890"))

	contextActionStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#0D1B2A")).
				Background(lipgloss.Color("#98C1D9"))

	contextActionPendingStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#E9C46A"))

	unknownMetricStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280"))

	chipStyle = lipgloss.NewStyle().
			Padding(0, 1).
			MarginRight(1).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#34506B")).
			Foreground(lipgloss.Color("#C9D6DF"))

	dangerChipStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0D1B2A")).
			Background(lipgloss.Color("#E76F51")).
			Padding(0, 1).
			MarginRight(1)

	contextMenuCloseStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#0D1B2A")).
				Background(lipgloss.Color("#98C1D9")).
				Padding(0, 1)

	footerCommandStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#34506B")).
				Padding(0, 2)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9FB3C8"))

	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F4A261"))
)

func helpStyles() help.Styles {
	styles := help.New().Styles
	styles.ShortKey = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#98C1D9"))
	styles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C98B3"))
	styles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("#486581"))
	styles.FullKey = styles.ShortKey
	styles.FullDesc = styles.ShortDesc
	styles.FullSeparator = styles.ShortSeparator
	styles.Ellipsis = styles.ShortSeparator
	return styles
}
