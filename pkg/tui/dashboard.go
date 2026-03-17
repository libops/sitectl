package tui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/sparkline"
	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/plugin"
	zone "github.com/lrstanley/bubblezone/v2"
	"golang.org/x/crypto/ssh"
)

type siteGroup struct {
	Name     string
	Contexts []config.Context
}

type screenMode int

const (
	screenDashboard screenMode = iota
	screenLogs
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayActions
	overlaySettings
	overlayChooser
	overlayInfo
	overlayCommands
)

type refreshTickMsg time.Time

type summaryLoadedMsg struct {
	ContextName string
	Summary     docker.ProjectSummary
	Err         error
}

type logsLoadedMsg struct {
	ContextName string
	Logs        string
	Err         error
}

type commandFinishedMsg struct {
	Command string
	Output  string
	Err     error
}

type commandExecFinishedMsg struct {
	Command string
	Err     error
}

type menuItem struct {
	title  string
	desc   string
	action string
}

func (i menuItem) Title() string       { return i.title }
func (i menuItem) Description() string { return i.desc }
func (i menuItem) FilterValue() string { return i.title + " " + i.desc }

type keyMap struct {
	Left     key.Binding
	Right    key.Binding
	Up       key.Binding
	Down     key.Binding
	Actions  key.Binding
	Settings key.Binding
	NewApp   key.Binding
	Command  key.Binding
	Palette  key.Binding
	Terminal key.Binding
	Logs     key.Binding
	Refresh  key.Binding
	Enter    key.Binding
	Back     key.Binding
	Quit     key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("h/left", "site")),
		Right:    key.NewBinding(key.WithKeys("right", "l", "tab"), key.WithHelp("l/right", "next site")),
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("k/up", "env up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j/down", "env down")),
		Actions:  key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "actions")),
		Settings: key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "settings")),
		NewApp:   key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "choose app")),
		Command:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "command bar")),
		Palette:  key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		Terminal: key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "run in terminal")),
		Logs:     key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "logs")),
		Refresh:  key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "refresh")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Back:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Left, k.Up, k.Command, k.Palette, k.Logs, k.Refresh, k.Terminal, k.Back, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Left, k.Right, k.Up, k.Down}, {k.Command, k.Palette, k.Logs, k.Refresh}, {k.Actions, k.Settings, k.NewApp, k.Terminal, k.Enter, k.Back, k.Quit}}
}

type dashboardModel struct {
	cfg            *config.Config
	sites          []siteGroup
	plugins        []plugin.InstalledPlugin
	currentContext string

	siteIndex int
	envIndex  int
	width     int
	height    int

	screen  screenMode
	overlay overlayMode

	loading    bool
	loadingLog bool
	summary    docker.ProjectSummary
	summaryErr error
	logsErr    error

	lastMessage string
	infoTitle   string
	infoBody    string
	logsTitle   string

	historyCPU    map[string][]float64
	historyMemory map[string][]float64

	help          help.Model
	keys          keyMap
	spin          spinner.Model
	detail        viewport.Model
	logs          viewport.Model
	actions       list.Model
	settings      list.Model
	chooser       list.Model
	commands      list.Model
	commandParent string

	commandInput     textinput.Model
	commandRunning   bool
	commandQuitArmed bool
}

func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError + 100,
	})))
	defer slog.SetDefault(previousLogger)

	zone.NewGlobal()
	defer zone.Close()

	model := newDashboardModel(cfg, plugin.DiscoverInstalled())
	program := tea.NewProgram(model)
	_, err = program.Run()
	return err
}

func newDashboardModel(cfg *config.Config, plugins []plugin.InstalledPlugin) *dashboardModel {
	current, _ := config.Current()
	keys := defaultKeyMap()

	m := &dashboardModel{
		cfg:            cfg,
		sites:          groupContexts(cfg),
		plugins:        pluginsWithTemplates(plugins),
		currentContext: current,
		width:          120,
		height:         36,
		keys:           keys,
		help:           help.New(),
		spin:           spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(spinnerStyle)),
		historyCPU:     map[string][]float64{},
		historyMemory:  map[string][]float64{},
	}
	m.help.Styles = helpStyles()
	m.siteIndex, m.envIndex = defaultSelection(m.sites, current)
	m.detail = viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	m.detail.MouseWheelEnabled = true
	m.detail.SetContent("Loading...")
	m.logs = viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	m.logs.MouseWheelEnabled = true
	m.logs.SetContent("No logs loaded.")
	m.logsTitle = "Logs"
	m.actions = newMenuModel("Actions", []menuItem{
		{title: "Refresh", desc: "Reload summary for the selected environment", action: "refresh"},
		{title: "Logs", desc: "Open a log view for this environment", action: "logs"},
		{title: "Choose App", desc: "Open the plugin-backed app chooser", action: "chooser"},
	})
	m.settings = newMenuModel("Settings", []menuItem{
		{title: "Context Details", desc: "Inspect context configuration for the selected environment", action: "context-info"},
		{title: "Plugin Details", desc: "Inspect the selected plugin and template repo", action: "plugin-info"},
	})
	m.chooser = newMenuModel("Choose An App", pluginMenuItems(m.plugins))
	m.commands = newMenuModel("Commands", commandPaletteItems("", m.selectedContextName(), m.selectedSiteName(), m.selectedPluginName()))
	m.commandInput = textinput.New()
	m.commandInput.Prompt = "sitectl --context " + m.selectedContextName() + " "
	m.commandInput.Placeholder = "compose ps"
	m.commandInput.ShowSuggestions = true
	m.commandInput.SetWidth(60)
	m.commandInput.Focus()
	m.refreshCommandSuggestions()
	m.syncLayout()
	return m
}

func (m *dashboardModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.spin.Tick,
		nextRefreshCmd(),
	}
	if ctx, ok := m.selectedContext(); ok {
		m.loading = true
		cmds = append(cmds, loadSummaryCmd(ctx))
	}
	return tea.Batch(cmds...)
}

func (m *dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncLayout()
		return m, nil

	case refreshTickMsg:
		cmds := []tea.Cmd{nextRefreshCmd()}
		if ctx, ok := m.selectedContext(); ok {
			cmds = append(cmds, loadSummaryCmd(ctx))
			if m.screen == screenLogs && strings.HasPrefix(m.logsTitle, "Logs") {
				cmds = append(cmds, loadLogsCmd(ctx))
			}
		}
		return m, tea.Batch(cmds...)

	case summaryLoadedMsg:
		if ctx, ok := m.selectedContext(); ok && ctx.Name == msg.ContextName {
			m.loading = false
			m.summary = msg.Summary
			m.summaryErr = msg.Err
			if msg.Err == nil {
				m.pushHistory(
					msg.ContextName,
					msg.Summary.CPUPercent,
					memoryPercent(msg.Summary),
				)
			}
			m.syncDetailContent()
		}
		return m, nil

	case logsLoadedMsg:
		if ctx, ok := m.selectedContext(); ok && ctx.Name == msg.ContextName {
			m.loadingLog = false
			m.logsErr = msg.Err
			content := msg.Logs
			if msg.Err != nil {
				content = msg.Err.Error()
			}
			if strings.TrimSpace(content) == "" {
				content = "No logs returned."
			}
			m.logs.SetContent(content)
			m.logs.GotoBottom()
		}
		return m, nil

	case commandFinishedMsg:
		m.commandRunning = false
		m.commandQuitArmed = false
		m.screen = screenLogs
		m.logsTitle = "Command Output"
		content := msg.Output
		if msg.Err != nil {
			if strings.TrimSpace(content) == "" {
				content = msg.Err.Error()
			} else {
				content += "\n\n" + msg.Err.Error()
			}
		}
		if strings.TrimSpace(content) == "" {
			content = "Command completed with no output."
		}
		m.logs.SetContent(content)
		m.logs.GotoTop()
		m.syncLayout()
		return m, nil

	case commandExecFinishedMsg:
		m.commandRunning = false
		m.commandQuitArmed = false
		if msg.Err != nil {
			m.lastMessage = fmt.Sprintf("Command failed: %v", msg.Err)
		} else {
			m.lastMessage = fmt.Sprintf("Terminal command finished: %s", msg.Command)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		if m.overlay != overlayNone {
			return m.updateOverlay(msg)
		}
		if m.screen == screenLogs {
			var cmd tea.Cmd
			m.logs, cmd = m.logs.Update(msg)
			return m, cmd
		}
		switch msg := msg.(type) {
		case tea.MouseReleaseMsg:
			return m.handleMouseRelease(msg)
		case tea.MouseWheelMsg:
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	if m.overlay != overlayNone {
		return m.updateOverlay(msg)
	}
	if m.screen == screenLogs {
		var cmd tea.Cmd
		m.logs, cmd = m.logs.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}

func (m *dashboardModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.overlay == overlayNone && m.commandInput.Focused() {
		switch {
		case msg.String() == "ctrl+c":
			if strings.TrimSpace(m.commandInput.Value()) != "" {
				m.commandInput.SetValue("")
				m.commandQuitArmed = false
				m.lastMessage = "Command cleared."
				return m, nil
			}
			if m.commandQuitArmed {
				return m, tea.Quit
			}
			m.commandQuitArmed = true
			m.lastMessage = "Command is empty. Press ctrl+c again to quit."
			return m, nil
		case msg.String() == "ctrl+a":
			m.commandQuitArmed = false
			m.commandInput.SetCursor(0)
			return m, nil
		case key.Matches(msg, m.keys.Back):
			m.commandQuitArmed = false
			m.commandInput.Blur()
			return m, nil
		case key.Matches(msg, m.keys.Terminal):
			m.commandQuitArmed = false
			return m.runCommand(true)
		case msg.String() == "enter":
			m.commandQuitArmed = false
			return m.runCommand(false)
		default:
			m.commandQuitArmed = false
			var cmd tea.Cmd
			m.commandInput, cmd = m.commandInput.Update(msg)
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back):
		if m.overlay != overlayNone {
			if m.overlay == overlayInfo {
				m.syncDetailContent()
			}
			m.overlay = overlayNone
			return m, nil
		}
		if m.screen == screenLogs {
			m.screen = screenDashboard
			m.syncLayout()
			return m, nil
		}
		return m, tea.Quit
	}

	if m.overlay != overlayNone {
		if msg.String() == "enter" {
			return m.handleOverlaySelection()
		}
		return m.updateOverlay(msg)
	}

	if m.screen == screenLogs {
		switch {
		case key.Matches(msg, m.keys.Refresh):
			if ctx, ok := m.selectedContext(); ok && strings.HasPrefix(m.logsTitle, "Logs") {
				return m, loadLogsCmd(ctx)
			}
		case key.Matches(msg, m.keys.Logs):
			m.screen = screenDashboard
			m.syncLayout()
			return m, nil
		case key.Matches(msg, m.keys.Terminal):
			return m.runCommand(true)
		case msg.String() == "enter":
			return m.runCommand(false)
		case key.Matches(msg, m.keys.Up), key.Matches(msg, m.keys.Down):
			var cmd tea.Cmd
			m.logs, cmd = m.logs.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.logs, cmd = m.logs.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Left):
		if m.siteIndex > 0 {
			m.siteIndex--
			m.envIndex = defaultEnvIndex(m.selectedSiteContexts(), m.currentContext)
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Right):
		if m.siteIndex < len(m.sites)-1 {
			m.siteIndex++
			m.envIndex = defaultEnvIndex(m.selectedSiteContexts(), m.currentContext)
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Up):
		if m.envIndex > 0 {
			m.envIndex--
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Down):
		if contexts := m.selectedSiteContexts(); m.envIndex < len(contexts)-1 {
			m.envIndex++
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Actions):
		m.overlay = overlayActions
		return m, nil
	case key.Matches(msg, m.keys.Settings):
		m.overlay = overlaySettings
		return m, nil
	case key.Matches(msg, m.keys.NewApp):
		m.overlay = overlayChooser
		return m, nil
	case key.Matches(msg, m.keys.Command):
		m.commandInput.Focus()
		return m, nil
	case key.Matches(msg, m.keys.Palette):
		m.commandParent = ""
		m.commands = newMenuModel("Commands", commandPaletteItems("", m.selectedContextName(), m.selectedSiteName(), m.selectedPluginName()))
		m.overlay = overlayCommands
		return m, nil
	case key.Matches(msg, m.keys.Logs):
		return m.openLogs()
	case key.Matches(msg, m.keys.Refresh):
		if ctx, ok := m.selectedContext(); ok {
			m.loading = true
			return m, loadSummaryCmd(ctx)
		}
	}

	switch {
	case key.Matches(msg, m.keys.Terminal):
		return m.runCommand(true)
	case msg.String() == "enter":
		return m.runCommand(false)
	}

	var cmd tea.Cmd
	m.commandInput, cmd = m.commandInput.Update(msg)
	return m, cmd
}

func (m *dashboardModel) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if msg.Mouse().Button != tea.MouseLeft {
		return m, nil
	}

	for _, targetSite := range m.sites {
		if z := zone.Get("tab:" + targetSite.Name); z != nil && z.InBounds(msg) {
			for i, site := range m.sites {
				if site.Name == targetSite.Name {
					m.siteIndex = i
					m.envIndex = defaultEnvIndex(site.Contexts, m.currentContext)
					return m.reloadSelected()
				}
			}
		}
	}

	for i, ctx := range m.selectedSiteContexts() {
		if z := zone.Get("env:" + ctx.Name); z != nil && z.InBounds(msg) {
			m.envIndex = i
			return m.reloadSelected()
		}
	}

	if z := zone.Get("chip:actions"); z != nil && z.InBounds(msg) {
		m.overlay = overlayActions
	}
	if z := zone.Get("chip:settings"); z != nil && z.InBounds(msg) {
		m.overlay = overlaySettings
	}
	if z := zone.Get("chip:new"); z != nil && z.InBounds(msg) {
		m.overlay = overlayChooser
	}
	if z := zone.Get("chip:logs"); z != nil && z.InBounds(msg) {
		return m.openLogs()
	}
	if z := zone.Get("chip:refresh"); z != nil && z.InBounds(msg) {
		if ctx, ok := m.selectedContext(); ok {
			m.loading = true
			return m, loadSummaryCmd(ctx)
		}
	}
	if z := zone.Get("chip:command"); z != nil && z.InBounds(msg) {
		m.commandInput.Focus()
		return m, nil
	}
	if z := zone.Get("chip:palette"); z != nil && z.InBounds(msg) {
		m.commandParent = ""
		m.commands = newMenuModel("Commands", commandPaletteItems("", m.selectedContextName(), m.selectedSiteName(), m.selectedPluginName()))
		m.overlay = overlayCommands
	}

	return m, nil
}

func (m *dashboardModel) updateOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.overlay {
	case overlayActions:
		var cmd tea.Cmd
		m.actions, cmd = m.actions.Update(msg)
		return m, cmd
	case overlaySettings:
		var cmd tea.Cmd
		m.settings, cmd = m.settings.Update(msg)
		return m, cmd
	case overlayChooser:
		var cmd tea.Cmd
		m.chooser, cmd = m.chooser.Update(msg)
		return m, cmd
	case overlayCommands:
		var cmd tea.Cmd
		m.commands, cmd = m.commands.Update(msg)
		return m, cmd
	case overlayInfo:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m *dashboardModel) handleOverlaySelection() (tea.Model, tea.Cmd) {
	var item menuItem
	switch m.overlay {
	case overlayActions:
		selected, _ := m.actions.SelectedItem().(menuItem)
		item = selected
	case overlaySettings:
		selected, _ := m.settings.SelectedItem().(menuItem)
		item = selected
	case overlayChooser:
		selected, _ := m.chooser.SelectedItem().(menuItem)
		item = selected
	case overlayCommands:
		selected, _ := m.commands.SelectedItem().(menuItem)
		item = selected
	}

	switch item.action {
	case "refresh":
		m.overlay = overlayNone
		if ctx, ok := m.selectedContext(); ok {
			m.loading = true
			return m, loadSummaryCmd(ctx)
		}
	case "logs":
		m.overlay = overlayNone
		return m.openLogs()
	case "chooser":
		m.overlay = overlayChooser
		return m, nil
	case "context-info":
		if ctx, ok := m.selectedContext(); ok {
			m.infoTitle = "Context Details"
			m.infoBody = renderContextInfo(ctx)
			m.detail.SetContent(m.infoBody)
			m.detail.GotoTop()
			m.overlay = overlayInfo
			return m, nil
		}
	case "plugin-info":
		if ctx, ok := m.selectedContext(); ok {
			m.infoTitle = "Plugin Details"
			m.infoBody = renderPluginInfo(findPlugin(m.plugins, ctx.Plugin), ctx.Plugin)
			m.detail.SetContent(m.infoBody)
			m.detail.GotoTop()
			m.overlay = overlayInfo
			return m, nil
		}
	default:
		if strings.HasPrefix(item.action, "plugin:") {
			m.lastMessage = fmt.Sprintf("Bootstrap flow pending: %s", strings.TrimPrefix(item.action, "plugin:"))
			m.overlay = overlayNone
			return m, nil
		}
		if strings.HasPrefix(item.action, "palette:") {
			parent := strings.TrimPrefix(item.action, "palette:")
			m.commandParent = parent
			m.commands = newMenuModel(commandPaletteTitle(parent), commandPaletteItems(parent, m.selectedContextName(), m.selectedSiteName(), m.selectedPluginName()))
			return m, nil
		}
		if strings.HasPrefix(item.action, "fill:") {
			m.commandInput.SetValue(strings.TrimPrefix(item.action, "fill:"))
			m.overlay = overlayNone
			m.commandInput.Focus()
			return m, nil
		}
	}

	m.overlay = overlayNone
	return m, nil
}

func (m *dashboardModel) openLogs() (tea.Model, tea.Cmd) {
	ctx, ok := m.selectedContext()
	if !ok {
		return m, nil
	}
	m.screen = screenLogs
	m.loadingLog = true
	m.logsTitle = "Logs | tail 20 | auto-refresh"
	m.syncLayout()
	return m, loadLogsCmd(ctx)
}

func (m *dashboardModel) reloadSelected() (tea.Model, tea.Cmd) {
	m.summary = docker.ProjectSummary{}
	m.summaryErr = nil
	m.refreshCommandSuggestions()
	m.syncDetailContent()
	if ctx, ok := m.selectedContext(); ok {
		m.loading = true
		cmds := []tea.Cmd{loadSummaryCmd(ctx)}
		if m.screen == screenLogs {
			m.loadingLog = true
			cmds = append(cmds, loadLogsCmd(ctx))
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m *dashboardModel) View() tea.View {
	content := m.render()
	v := tea.NewView(zone.Scan(content))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *dashboardModel) render() string {
	if m.width < 100 || m.height < 28 {
		return docStyle.Render(panelStyle.Width(max(40, m.width-6)).Render("Terminal too small for the sitectl dashboard.\n\nResize to at least 100x28."))
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.renderTabs(),
		m.renderHeaderChips(),
		m.renderTitle(),
		m.renderResourceHeader(),
		m.renderMainArea(),
		m.renderCommandFooter(),
		footerStyle.Render(m.help.View(m.keys)),
	)

	if strings.TrimSpace(m.lastMessage) != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, body, subtleStyle.Render(m.lastMessage))
	}

	rendered := docStyle.Render(body)
	if m.overlay != overlayNone {
		return overlay(rendered, m.renderOverlay(), m.width, 1)
	}
	return rendered
}

func (m *dashboardModel) renderTabs() string {
	tabs := make([]string, 0, len(m.sites))
	for i, site := range m.sites {
		label := fmt.Sprintf("%d:%s", i+1, site.Name)
		tab := tabStyle.Render(label)
		if i == m.siteIndex {
			tab = activeTabStyle.Render(label)
		}
		tabs = append(tabs, zone.Mark("tab:"+site.Name, tab))
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, tabs...)
}

func (m *dashboardModel) renderHeaderChips() string {
	chips := []string{
		zone.Mark("chip:actions", chipStyle.Render("[ctrl+a] Actions")),
		zone.Mark("chip:settings", chipStyle.Render("[ctrl+s] Settings")),
		zone.Mark("chip:new", chipStyle.Render("[ctrl+n] Choose App")),
		zone.Mark("chip:command", chipStyle.Render("[/] Command")),
		zone.Mark("chip:palette", chipStyle.Render("[ctrl+p] Palette")),
		zone.Mark("chip:logs", chipStyle.Render("[ctrl+g] Logs")),
		zone.Mark("chip:refresh", chipStyle.Render("[ctrl+r] Refresh")),
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, chips...)
}

func (m *dashboardModel) renderTitle() string {
	site := m.sites[m.siteIndex]
	ctx, _ := m.selectedContext()
	contextName := "-"
	if ctx.Name != "" {
		contextName = ctx.Name
	}
	line := strings.Repeat("-", max(4, m.width-len(site.Name)-len(contextName)-20))
	return titleStyle.Render(fmt.Sprintf(" Sitectl | %s | %s ", site.Name, contextName)) + subtleStyle.Render(line)
}

func (m *dashboardModel) renderResourceHeader() string {
	ctx, _ := m.selectedContext()
	historyKey := ctx.Name
	widths := splitWidth(max(m.width-8, 60), 2)
	cpuDetail := fmt.Sprintf("%.1f%% total across %d containers", m.summary.CPUPercent, m.summary.Total)
	memDetail := fmt.Sprintf("%s / %s", humanBytes(m.summary.MemoryBytes), humanBytes(m.summary.MemoryLimitBytes))
	if m.loading {
		cpuDetail = "Refreshing docker stats..."
		memDetail = "Refreshing docker stats..."
	}
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		renderChartBox("CPU", m.historyCPU[historyKey], cpuDetail, "#F4A261", widths[0]),
		renderChartBox("Memory", m.historyMemory[historyKey], memDetail, "#98C1D9", widths[1]),
	)
}

func (m *dashboardModel) renderMainArea() string {
	switch m.screen {
	case screenLogs:
		return m.renderLogsArea()
	default:
		return m.renderDashboardArea()
	}
}

func (m *dashboardModel) renderDashboardArea() string {
	width := max(m.width-6, 80)
	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderEnvironmentCards(width),
		m.renderDetailsPanel(width),
	)
}

func (m *dashboardModel) renderLogsArea() string {
	ctx, _ := m.selectedContext()
	hint := "Auto-refreshing the latest 20 log lines. Scroll with mouse wheel or j/k. Press esc or ctrl+g to return."
	if m.logsTitle == "Command Output" {
		hint = "Command output. Press esc to return to the dashboard and keep using the footer command bar."
	}
	header := panelStyle.Width(max(40, m.width-6)).Render(strings.Join([]string{
		m.logsTitle,
		fmt.Sprintf("Context: %s", ctx.Name),
		hint,
	}, "\n"))
	body := panelStyle.Width(max(40, m.width-6)).Height(max(10, m.height-14)).Render(m.logs.View())
	if m.loadingLog {
		header = panelStyle.Width(max(40, m.width-6)).Render(m.spin.View() + " Loading logs...\nContext: " + ctx.Name)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func (m *dashboardModel) renderEnvironmentCards(width int) string {
	site := m.sites[m.siteIndex]
	cards := make([]string, 0, len(site.Contexts)+1)
	cards = append(cards, sectionTitleStyle.Render("Environments"))
	if len(site.Contexts) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, cards...)
	}
	count := len(site.Contexts)
	gapTotal := max(0, count-1)
	selectedWidth := 34
	compactWidth := 18
	if count == 1 {
		selectedWidth = width - 2
	}
	if count > 1 && selectedWidth+compactWidth*(count-1)+gapTotal > width {
		compactWidth = max(14, (width-selectedWidth-gapTotal)/(count-1))
	}
	if count > 1 && selectedWidth+compactWidth*(count-1)+gapTotal > width {
		selectedWidth = max(24, width-compactWidth*(count-1)-gapTotal)
	}
	if selectedWidth+compactWidth*(count-1)+gapTotal > width {
		selectedWidth = max(18, (width-gapTotal)/count)
		compactWidth = selectedWidth
	}
	row := make([]string, 0, len(site.Contexts))
	for i, ctx := range site.Contexts {
		selected := i == m.envIndex
		cardWidth := compactWidth
		lines := []string{strings.ToUpper(envLabel(ctx)), ctx.Name}
		if selected {
			cardWidth = selectedWidth
			lines = append(lines,
				fmt.Sprintf("plugin: %s", firstNonEmpty(ctx.Plugin, "core")),
				fmt.Sprintf("compose: %s", firstNonEmpty(ctx.EffectiveComposeProjectName(), "-")),
				fmt.Sprintf("network: %s", firstNonEmpty(ctx.EffectiveComposeNetwork(), "-")),
			)
			if ctx.DockerHostType == config.ContextRemote {
				lines = append(lines, fmt.Sprintf("host: %s", firstNonEmpty(ctx.SSHHostname, "-")))
			} else {
				lines = append(lines, fmt.Sprintf("dir: %s", firstNonEmpty(ctx.ProjectDir, "-")))
			}
		} else {
			lines = append(lines, firstNonEmpty(ctx.Plugin, "core"))
		}
		if ctx.Name == m.currentContext {
			lines = append(lines, accentStyle.Render("current"))
		}
		body := strings.Join(lines, "\n")
		style := cardStyle.Width(cardWidth)
		if i == m.envIndex {
			style = selectedCardStyle.Width(cardWidth)
		}
		row = append(row, zone.Mark("env:"+ctx.Name, style.Render(body)))
	}
	cards = append(cards, lipgloss.JoinHorizontal(lipgloss.Top, row...))
	return lipgloss.JoinVertical(lipgloss.Left, cards...)
}

func (m *dashboardModel) renderDetailsPanel(width int) string {
	content := m.detail.View()
	if m.loading {
		content = m.spin.View() + " Loading Docker Compose status..."
	}

	panelHeight := min(max(10, m.height-30), 16)
	return panelStyle.Width(max(32, width-4)).Height(panelHeight).Render(
		sectionTitleStyle.MarginBottom(0).Render("Selected Environment Status") + "\n" + content,
	)
}

func (m *dashboardModel) renderOverlay() string {
	title := "Menu"
	content := ""
	switch m.overlay {
	case overlayActions:
		title = "Actions"
		content = m.actions.View()
	case overlaySettings:
		title = "Settings"
		content = m.settings.View()
	case overlayChooser:
		title = "Choose An App"
		content = m.chooser.View()
	case overlayInfo:
		title = m.infoTitle
		content = m.detail.View()
	case overlayCommands:
		title = commandPaletteTitle(m.commandParent)
		content = m.commands.View()
	}
	return overlayPanelStyle.Width(min(72, max(48, m.width-12))).Render(sectionTitleStyle.Render(title) + "\n" + content)
}

func (m *dashboardModel) renderCommandFooter() string {
	contextName := m.selectedContextName()
	status := accentStyle.Render("ready")
	if m.commandRunning {
		status = accentStyle.Render(m.spin.View() + " running")
	}
	hint := subtleStyle.Render("type a sitectl subcommand  enter: run here  ctrl+x: terminal  ctrl+p: palette")
	bar := footerCommandStyle.Width(max(40, m.width-6)).Render(
		fmt.Sprintf("Context: %s  [%s]\n%s\n%s", contextName, status, m.commandInput.View(), hint),
	)
	return bar
}

func (m *dashboardModel) syncLayout() {
	hpad, _ := docStyle.GetFrameSize()
	m.help.SetWidth(max(20, m.width-hpad))

	detailHeight := min(max(8, m.height-32), 14)
	m.detail.SetWidth(max(40, m.width-hpad-8))
	m.detail.SetHeight(detailHeight)

	logHeight := max(10, m.height-14)
	m.logs.SetWidth(max(30, m.width-hpad-8))
	m.logs.SetHeight(logHeight)

	menuWidth := min(58, max(36, m.width/2))
	menuHeight := min(18, max(10, m.height/2))
	m.actions.SetSize(menuWidth, menuHeight)
	m.settings.SetSize(menuWidth, menuHeight)
	m.chooser.SetSize(menuWidth, menuHeight)
	m.commands.SetSize(menuWidth, menuHeight)
	m.commandInput.SetWidth(max(20, m.width-18))
	m.commandInput.Prompt = "sitectl --context " + m.selectedContextName() + " "

	m.syncDetailContent()
}

func (m *dashboardModel) syncDetailContent() {
	ctx, ok := m.selectedContext()
	if !ok {
		m.detail.SetContent("No context selected.")
		return
	}
	if m.overlay == overlayInfo && strings.TrimSpace(m.infoBody) != "" {
		m.detail.SetContent(m.infoBody)
		return
	}
	if m.summaryErr != nil {
		m.detail.SetContent(m.summaryErr.Error())
		return
	}

	lines := []string{
		fmt.Sprintf("Status: %s", strings.ToUpper(firstNonEmpty(m.summary.Status, "unknown"))),
		fmt.Sprintf("CPU: %.1f%%", m.summary.CPUPercent),
		fmt.Sprintf("Memory: %s / %s", humanBytes(m.summary.MemoryBytes), humanBytes(m.summary.MemoryLimitBytes)),
		fmt.Sprintf("Containers: %d total, %d running, %d healthy, %d stopped", m.summary.Total, m.summary.Running, m.summary.Healthy, m.summary.Stopped),
		"",
		"Services:",
	}
	if len(m.summary.Services) == 0 {
		lines = append(lines, "  No Compose containers found for this context.")
	} else {
		for _, service := range m.summary.Services {
			lines = append(lines, fmt.Sprintf("  %s  %s", service.Service, firstNonEmpty(service.Status, service.State)))
		}
	}
	lines = append(lines,
		"",
		fmt.Sprintf("Context directory: %s", firstNonEmpty(ctx.ProjectDir, "-")),
		fmt.Sprintf("Compose project: %s", firstNonEmpty(ctx.EffectiveComposeProjectName(), "-")),
		fmt.Sprintf("Compose network: %s", firstNonEmpty(ctx.EffectiveComposeNetwork(), "-")),
	)
	m.detail.SetContent(strings.Join(lines, "\n"))
}

func (m *dashboardModel) pushHistory(contextName string, cpu, memory float64) {
	m.historyCPU[contextName] = appendLimited(m.historyCPU[contextName], cpu, 24)
	m.historyMemory[contextName] = appendLimited(m.historyMemory[contextName], memory, 24)
}

func (m *dashboardModel) selectedSiteContexts() []config.Context {
	if len(m.sites) == 0 || m.siteIndex >= len(m.sites) {
		return nil
	}
	return m.sites[m.siteIndex].Contexts
}

func (m *dashboardModel) selectedContext() (config.Context, bool) {
	contexts := m.selectedSiteContexts()
	if len(contexts) == 0 || m.envIndex >= len(contexts) {
		return config.Context{}, false
	}
	return contexts[m.envIndex], true
}

func newMenuModel(title string, items []menuItem) list.Model {
	delegate := list.NewDefaultDelegate()
	converted := make([]list.Item, 0, len(items))
	for _, item := range items {
		converted = append(converted, item)
	}
	m := list.New(converted, delegate, 48, 12)
	m.Title = title
	m.SetFilteringEnabled(false)
	m.SetShowStatusBar(false)
	m.SetShowHelp(false)
	m.DisableQuitKeybindings()
	return m
}

func pluginMenuItems(plugins []plugin.InstalledPlugin) []menuItem {
	items := make([]menuItem, 0, len(plugins))
	for _, p := range plugins {
		items = append(items, menuItem{
			title:  p.Name,
			desc:   firstNonEmpty(p.TemplateRepo, p.Description, "No template repo configured"),
			action: "plugin:" + p.Name,
		})
	}
	if len(items) == 0 {
		items = append(items, menuItem{
			title:  "No plugins found",
			desc:   "Install a sitectl-* plugin that exposes a template repo.",
			action: "",
		})
	}
	return items
}

func loadSummaryCmd(ctx config.Context) tea.Cmd {
	return func() tea.Msg {
		summary, err := docker.SummarizeProject(&ctx)
		return summaryLoadedMsg{ContextName: ctx.Name, Summary: summary, Err: err}
	}
}

func loadLogsCmd(ctx config.Context) tea.Cmd {
	return func() tea.Msg {
		logs, err := fetchComposeLogs(ctx)
		return logsLoadedMsg{ContextName: ctx.Name, Logs: logs, Err: err}
	}
}

func nextRefreshCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return refreshTickMsg(t) })
}

func fetchComposeLogs(ctx config.Context) (string, error) {
	args := composeArgs(ctx, "logs", "--tail", "20", "--timestamps", "--no-color")
	if ctx.DockerHostType == config.ContextLocal {
		cmd := exec.Command("docker", args...)
		cmd.Dir = ctx.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	remoteCmd := fmt.Sprintf("cd %s && ", shellquote.Join(ctx.ProjectDir))
	if ctx.RunSudo {
		remoteCmd += "sudo "
	}
	remoteCmd += shellquote.Join(append([]string{"docker"}, args...)...)

	client, err := ctx.DialSSH()
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	output, err := session.CombinedOutput(remoteCmd)
	if err != nil {
		if _, ok := err.(*ssh.ExitError); ok && len(output) > 0 {
			return string(output), nil
		}
		return string(output), err
	}
	return string(output), nil
}

func composeArgs(ctx config.Context, subcommand ...string) []string {
	args := []string{"compose"}
	for _, file := range ctx.ComposeFile {
		args = append(args, "-f", file)
	}
	for _, env := range ctx.EnvFile {
		args = append(args, "--env-file", env)
	}
	args = append(args, subcommand...)
	return args
}

func groupContexts(cfg *config.Config) []siteGroup {
	if cfg == nil || len(cfg.Contexts) == 0 {
		return nil
	}

	siteMap := map[string][]config.Context{}
	for _, ctx := range cfg.Contexts {
		siteName := firstNonEmpty(ctx.Site, ctx.ProjectName, ctx.Name, "default")
		siteMap[siteName] = append(siteMap[siteName], ctx)
	}

	names := make([]string, 0, len(siteMap))
	for name := range siteMap {
		names = append(names, name)
	}
	sort.Strings(names)

	sites := make([]siteGroup, 0, len(names))
	for _, name := range names {
		contexts := siteMap[name]
		sort.Slice(contexts, func(i, j int) bool {
			leftEnv := envLabel(contexts[i])
			rightEnv := envLabel(contexts[j])
			leftRank := envSortRank(leftEnv)
			rightRank := envSortRank(rightEnv)
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			if leftEnv != rightEnv {
				return leftEnv < rightEnv
			}
			return contexts[i].Name < contexts[j].Name
		})
		sites = append(sites, siteGroup{Name: name, Contexts: contexts})
	}

	return sites
}

func pluginsWithTemplates(discovered []plugin.InstalledPlugin) []plugin.InstalledPlugin {
	filtered := make([]plugin.InstalledPlugin, 0, len(discovered))
	for _, installed := range discovered {
		if strings.TrimSpace(installed.TemplateRepo) == "" {
			continue
		}
		filtered = append(filtered, installed)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})
	return filtered
}

func defaultSelection(sites []siteGroup, current string) (int, int) {
	for i, site := range sites {
		for j, ctx := range site.Contexts {
			if ctx.Name == current {
				return i, j
			}
		}
	}
	return 0, 0
}

func defaultEnvIndex(contexts []config.Context, current string) int {
	for i, ctx := range contexts {
		if ctx.Name == current {
			return i
		}
	}
	return 0
}

func envLabel(ctx config.Context) string {
	return firstNonEmpty(ctx.Environment, "unknown")
}

func envSortRank(value string) int {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "local":
		return 0
	case "dev", "development":
		return 1
	case "test", "testing", "stage", "staging":
		return 2
	case "prod", "production":
		return 3
	default:
		return 4
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func findPlugin(plugins []plugin.InstalledPlugin, name string) plugin.InstalledPlugin {
	for _, p := range plugins {
		if p.Name == name {
			return p
		}
	}
	return plugin.InstalledPlugin{Name: name}
}

func (m *dashboardModel) selectedContextName() string {
	if ctx, ok := m.selectedContext(); ok {
		return ctx.Name
	}
	return "-"
}

func (m *dashboardModel) selectedSiteName() string {
	if len(m.sites) == 0 || m.siteIndex >= len(m.sites) {
		return "-"
	}
	return m.sites[m.siteIndex].Name
}

func (m *dashboardModel) selectedPluginName() string {
	if ctx, ok := m.selectedContext(); ok {
		return ctx.Plugin
	}
	return ""
}

func (m *dashboardModel) refreshCommandSuggestions() {
	m.commandInput.SetSuggestions(commandSuggestions(m.selectedContextName(), m.selectedSiteName(), m.selectedPluginName()))
	if m.commandParent != "" {
		m.commands = newMenuModel(commandPaletteTitle(m.commandParent), commandPaletteItems(m.commandParent, m.selectedContextName(), m.selectedSiteName(), m.selectedPluginName()))
	}
}

func renderContextInfo(ctx config.Context) string {
	lines := []string{
		fmt.Sprintf("Name: %s", ctx.Name),
		fmt.Sprintf("Site: %s", firstNonEmpty(ctx.Site, "-")),
		fmt.Sprintf("Environment: %s", envLabel(ctx)),
		fmt.Sprintf("Plugin: %s", firstNonEmpty(ctx.Plugin, "-")),
		fmt.Sprintf("Docker Host Type: %s", firstNonEmpty(string(ctx.DockerHostType), "-")),
		fmt.Sprintf("Project Name: %s", firstNonEmpty(ctx.ProjectName, "-")),
		fmt.Sprintf("Compose Project: %s", firstNonEmpty(ctx.EffectiveComposeProjectName(), "-")),
		fmt.Sprintf("Compose Network: %s", firstNonEmpty(ctx.EffectiveComposeNetwork(), "-")),
		fmt.Sprintf("Project Dir: %s", firstNonEmpty(ctx.ProjectDir, "-")),
		fmt.Sprintf("Docker Socket: %s", firstNonEmpty(ctx.DockerSocket, "-")),
	}
	if ctx.DockerHostType == config.ContextRemote {
		lines = append(lines,
			fmt.Sprintf("SSH Host: %s", firstNonEmpty(ctx.SSHHostname, "-")),
			fmt.Sprintf("SSH User: %s", firstNonEmpty(ctx.SSHUser, "-")),
			fmt.Sprintf("SSH Port: %d", ctx.SSHPort),
		)
	}
	if len(ctx.ComposeFile) > 0 {
		lines = append(lines, "", "Compose Files:")
		for _, file := range ctx.ComposeFile {
			lines = append(lines, "  "+file)
		}
	}
	if len(ctx.EnvFile) > 0 {
		lines = append(lines, "", "Env Files:")
		for _, file := range ctx.EnvFile {
			lines = append(lines, "  "+file)
		}
	}
	return strings.Join(lines, "\n")
}

func renderPluginInfo(p plugin.InstalledPlugin, fallbackName string) string {
	name := firstNonEmpty(p.Name, fallbackName, "unknown")
	lines := []string{
		fmt.Sprintf("Name: %s", name),
		fmt.Sprintf("Description: %s", firstNonEmpty(p.Description, "-")),
		fmt.Sprintf("Version: %s", firstNonEmpty(p.Version, "-")),
		fmt.Sprintf("Author: %s", firstNonEmpty(p.Author, "-")),
		fmt.Sprintf("Binary: %s", firstNonEmpty(p.BinaryName, "-")),
		fmt.Sprintf("Path: %s", firstNonEmpty(p.Path, "-")),
		fmt.Sprintf("Template Repo: %s", firstNonEmpty(p.TemplateRepo, "-")),
	}
	return strings.Join(lines, "\n")
}

func commandPaletteTitle(parent string) string {
	if strings.TrimSpace(parent) == "" {
		return "Commands"
	}
	return strings.ToUpper(parent[:1]) + parent[1:] + " Commands"
}

func commandPaletteItems(parent, contextName, siteName, pluginName string) []menuItem {
	switch parent {
	case "compose":
		return []menuItem{
			{title: "ps", desc: "Show compose service status", action: "fill:compose ps"},
			{title: "logs", desc: "Fetch recent compose logs", action: "fill:compose logs --tail 80 --no-color"},
			{title: "up", desc: "Start services in detached mode", action: "fill:compose up"},
			{title: "down", desc: "Stop and remove services", action: "fill:compose down"},
			{title: "restart", desc: "Restart all services", action: "fill:compose restart"},
			{title: "exec", desc: "Open a shell in a service container", action: "fill:compose exec -it drupal bash"},
		}
	case "config":
		return []menuItem{
			{title: "validate", desc: "Validate the selected context", action: "fill:config validate"},
			{title: "current-context", desc: "Show active context resolution", action: "fill:config current-context"},
			{title: "get-environments", desc: "List environments for this site", action: "fill:config get-environments " + siteName},
			{title: "get-sites", desc: "List configured sites", action: "fill:config get-sites"},
		}
	case "port-forward":
		return []menuItem{
			{title: "traefik", desc: "Forward a common HTTP admin port", action: "fill:port-forward 8080:traefik:8080"},
			{title: "solr", desc: "Forward Solr admin for a remote site", action: "fill:port-forward 8983:solr:8983"},
		}
	case "plugin":
		return []menuItem{
			{title: pluginName, desc: "Open plugin help", action: "fill:" + pluginName + " --help"},
		}
	default:
		items := []menuItem{
			{title: "compose", desc: "Docker Compose commands for the selected environment", action: "palette:compose"},
			{title: "config", desc: "Context-aware configuration commands", action: "palette:config"},
			{title: "make", desc: "Run project make targets through sitectl", action: "fill:make"},
			{title: "port-forward", desc: "Forward ports to remote services", action: "palette:port-forward"},
			{title: "sequelace", desc: "Open database tooling for this context", action: "fill:sequelace"},
		}
		if strings.TrimSpace(pluginName) != "" && pluginName != "core" {
			items = append(items, menuItem{title: pluginName, desc: "Plugin-specific commands", action: "palette:plugin"})
		}
		if strings.TrimSpace(contextName) != "" {
			items = append(items, menuItem{title: "help", desc: "Show sitectl help", action: "fill:--help"})
		}
		return items
	}
}

func commandSuggestions(contextName, siteName, pluginName string) []string {
	items := []string{
		"compose ps",
		"compose logs --tail 80 --no-color",
		"compose up",
		"compose down",
		"compose restart",
		"compose exec -it drupal bash",
		"config validate",
		"config current-context",
		"config get-sites",
		"config get-environments " + siteName,
		"make",
		"port-forward 8080:traefik:8080",
		"sequelace",
	}
	if strings.TrimSpace(pluginName) != "" && pluginName != "core" {
		items = append(items, pluginName+" --help")
	}
	return items
}

func (m *dashboardModel) runCommand(interactive bool) (tea.Model, tea.Cmd) {
	raw := strings.TrimSpace(m.commandInput.Value())
	if raw == "" {
		return m, nil
	}
	display, args, err := normalizeSitectlCommand(raw, m.selectedContextName())
	if err != nil {
		m.lastMessage = err.Error()
		return m, nil
	}

	if interactive || isInteractiveArgs(args) {
		m.commandRunning = true
		m.commandInput.SetValue("")
		return m, runSitectlInteractiveCmd(display, args)
	}

	m.commandRunning = true
	m.logsTitle = "Command Output"
	m.logs.SetContent("Running " + display + "...")
	m.screen = screenLogs
	m.commandInput.SetValue("")
	return m, runSitectlCaptureCmd(display, args)
}

func normalizeSitectlCommand(raw, contextName string) (string, []string, error) {
	args, err := shellquote.Split(raw)
	if err != nil {
		return "", nil, fmt.Errorf("parse command: %w", err)
	}
	if len(args) == 0 {
		return "", nil, fmt.Errorf("command cannot be empty")
	}
	if args[0] == "sitectl" {
		args = args[1:]
	}
	if len(args) == 0 {
		return "", nil, fmt.Errorf("command cannot be empty")
	}
	if !containsContextArg(args) && strings.TrimSpace(contextName) != "" && contextName != "-" {
		args = append([]string{"--context", contextName}, args...)
	}
	return "sitectl " + strings.Join(args, " "), args, nil
}

func containsContextArg(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--context" {
			return true
		}
		if strings.HasPrefix(args[i], "--context=") {
			return true
		}
	}
	return false
}

func isInteractiveArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "port-forward", "sequelace":
		return true
	case "compose":
		if len(args) < 2 {
			return false
		}
		switch args[1] {
		case "exec", "run", "attach", "watch":
			return true
		case "logs":
			for _, arg := range args[2:] {
				if arg == "-f" || arg == "--follow" {
					return true
				}
			}
		}
	}
	return false
}

func runSitectlCaptureCmd(display string, args []string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return commandFinishedMsg{Command: display, Err: err}
		}
		cmd := exec.Command(exe, args...)
		output, err := cmd.CombinedOutput()
		return commandFinishedMsg{Command: display, Output: string(output), Err: err}
	}
}

func runSitectlInteractiveCmd(display string, args []string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return commandExecFinishedMsg{Command: display, Err: err} }
	}
	cmd := exec.Command(exe, args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return commandExecFinishedMsg{Command: display, Err: err}
	})
}

func appendLimited(values []float64, next float64, limit int) []float64 {
	values = append(values, next)
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return values
}

func memoryPercent(summary docker.ProjectSummary) float64 {
	if summary.MemoryLimitBytes == 0 {
		return 0
	}
	return (float64(summary.MemoryBytes) / float64(summary.MemoryLimitBytes)) * 100
}

func humanBytes(value uint64) string {
	if value == 0 {
		return "0B"
	}
	const (
		kb = 1000
		mb = kb * 1000
		gb = mb * 1000
		tb = gb * 1000
	)
	switch {
	case value >= tb:
		return fmt.Sprintf("%.1fTB", float64(value)/tb)
	case value >= gb:
		return fmt.Sprintf("%.1fGB", float64(value)/gb)
	case value >= mb:
		return fmt.Sprintf("%.1fMB", float64(value)/mb)
	case value >= kb:
		return fmt.Sprintf("%.1fKB", float64(value)/kb)
	default:
		return fmt.Sprintf("%dB", value)
	}
}

func renderChartBox(title string, values []float64, detail, border string, width int) string {
	innerWidth := max(8, width-6)
	chart := sparkline.New(innerWidth, 4)
	chart.PushAll(values)
	chart.DrawBraille()
	content := sectionTitleStyle.MarginBottom(0).Render(title) + "\n" + chart.View() + "\n" + detail
	style := panelStyle.Width(width)
	if strings.TrimSpace(border) != "" {
		style = style.BorderForeground(lipgloss.Color(border))
	}
	return style.Render(content)
}
