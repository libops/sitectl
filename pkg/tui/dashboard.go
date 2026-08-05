package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/kballard/go-shellquote"
	"github.com/libops/sitectl/internal/tuitour"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/helpers"
	"github.com/libops/sitectl/pkg/plugin"
	zone "github.com/lrstanley/bubblezone/v2"
)

type siteGroup struct {
	Name     string
	Contexts []config.Context
}

type screenMode int

const (
	screenDashboard screenMode = iota
	screenLogs
	screenTour
)

const maxCommandOutputBytes = 1 << 20
const maxCommandHistoryEntries = 100
const commandHistoryBrowseNone = -1
const localSummaryBatchSize = 3

type refreshTickMsg time.Time

type summaryLoadedMsg struct {
	ContextName string
	Summary     docker.ProjectSummary
	Err         error
}

type contextLifecycleFinishedMsg struct {
	ContextName string
	Action      string
	Output      string
	Err         error
}

type contextSummaryState struct {
	Summary docker.ProjectSummary
	Err     error
	Loading bool
	Loaded  bool
}

type contextPosition struct {
	SiteIndex int
	EnvIndex  int
	SiteName  string
	Context   config.Context
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

type commandStreamStartedMsg struct {
	ID      int
	Command string
	Events  <-chan commandStreamEvent
}

type commandStreamEventMsg struct {
	Event  commandStreamEvent
	Events <-chan commandStreamEvent
}

type commandStreamEvent struct {
	ID       int
	Command  string
	Output   string
	Err      error
	Done     bool
	Canceled bool
}

type commandExecFinishedMsg struct {
	Command string
	Err     error
}

type stateReloadedMsg struct {
	Config         *config.Config
	Plugins        []plugin.InstalledPlugin
	CurrentContext string
	Err            error
}

type menuItem struct {
	title  string
	desc   string
	action string
}

func (i menuItem) Title() string       { return i.title }
func (i menuItem) Description() string { return i.desc }
func (i menuItem) FilterValue() string { return i.title + " " + i.desc }

const onboardingListZoneID = "onboarding:choices"

type menuDelegate struct {
	list.DefaultDelegate
}

func (d menuDelegate) Render(w io.Writer, model list.Model, index int, item list.Item) {
	var rendered strings.Builder
	d.DefaultDelegate.Render(&rendered, model, index, item)
	_, _ = fmt.Fprint(w, zone.Mark(onboardingItemZoneID(index), rendered.String()))
}

type keyMap struct {
	Left      key.Binding
	Right     key.Binding
	Up        key.Binding
	Down      key.Binding
	Create    key.Binding
	Delete    key.Binding
	Lifecycle key.Binding
	Command   key.Binding
	Terminal  key.Binding
	Refresh   key.Binding
	Enter     key.Binding
	Back      key.Binding
	Quit      key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("h/left", "select")),
		Right:     key.NewBinding(key.WithKeys("right", "l", "tab"), key.WithHelp("l/right", "next")),
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("k/up", "row up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j/down", "row down")),
		Create:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "add")),
		Delete:    key.NewBinding(key.WithKeys("delete"), key.WithHelp("del", "delete")),
		Lifecycle: key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "start/stop")),
		Command:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "command")),
		Terminal:  key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "terminal")),
		Refresh:   key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "refresh")),
		Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Left, k.Create, k.Delete, k.Lifecycle, k.Command, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Left, k.Right, k.Up, k.Down}, {k.Create, k.Delete, k.Lifecycle, k.Command, k.Refresh, k.Terminal, k.Enter, k.Back, k.Quit}}
}

type dashboardModel struct {
	cfg                     *config.Config
	sites                   []siteGroup
	plugins                 []plugin.InstalledPlugin
	tourPanes               []tuitour.Pane
	currentContext          string
	pendingContextSelection string
	pendingWorkingDir       string
	contextMenuName         string

	siteIndex          int
	envIndex           int
	contextPage        int
	localRefreshCursor int
	width              int
	height             int

	screen screenMode

	loading    bool
	loadingLog bool
	summary    docker.ProjectSummary
	summaryErr error
	logsErr    error

	lastMessage string
	logsTitle   string
	logTarget   string
	detailBody  string
	logsBody    string

	contextSummaries map[string]contextSummaryState
	contextActions   map[string]string

	help    help.Model
	keys    keyMap
	spin    spinner.Model
	detail  viewport.Model
	logs    viewport.Model
	chooser list.Model

	contextNameInput textinput.Model
	creatingContext  bool
	commandInput     textinput.Model
	commandRunning   bool
	commandQuitArmed bool
	commandRunID     int
	commandCancel    context.CancelFunc
	commandOutput    bool
	commandHistory   []string
	commandHistoryAt int
	commandDraft     string
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
		cfg:              cfg,
		sites:            groupContexts(cfg),
		plugins:          plugins,
		tourPanes:        loadTourPanes(),
		currentContext:   current,
		width:            120,
		height:           36,
		keys:             keys,
		help:             help.New(),
		spin:             spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(spinnerStyle)),
		contextSummaries: map[string]contextSummaryState{},
		contextActions:   map[string]string{},
		commandHistoryAt: commandHistoryBrowseNone,
	}
	m.help.Styles = helpStyles()
	m.siteIndex, m.envIndex = defaultSelection(m.sites, current)
	m.syncContextPageToSelection()
	m.detail = viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	m.detail.MouseWheelEnabled = true
	m.detailBody = "Loading..."
	m.detail.SetContent(m.detailBody)
	m.logs = viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	m.logs.MouseWheelEnabled = true
	m.logsBody = "No logs loaded."
	m.logs.SetContent(m.logsBody)
	m.logsTitle = "Logs"
	m.chooser = newMenuModel(chooserTitle(m.sites), chooserItems(m.sites, m.plugins))
	m.contextNameInput = textinput.New()
	m.contextNameInput.Prompt = "Context name: "
	m.contextNameInput.Placeholder = "museum-local"
	m.contextNameInput.SetWidth(48)
	m.commandInput = textinput.New()
	m.commandInput.Prompt = "sitectl --context " + m.selectedContextName() + " "
	m.commandInput.Placeholder = "compose ps"
	m.commandInput.ShowSuggestions = true
	m.commandInput.SetWidth(60)
	m.refreshCommandSuggestions()
	m.syncLayout()
	return m
}

func (m *dashboardModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.spin.Tick,
		nextRefreshCmd(),
	}
	cmds = append(cmds, m.queueSummaryLoads(m.nextSummaryRefreshContexts())...)
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
			if m.screen == screenLogs && strings.HasPrefix(m.logsTitle, "Logs") {
				cmds = append(cmds, loadLogsCmd(ctx))
			} else if m.screen == screenLogs && strings.TrimSpace(m.logTarget) != "" {
				cmds = append(cmds, loadContainerLogsCmd(ctx, m.logTarget))
			}
		}
		cmds = append(cmds, m.queueSummaryLoads(m.nextSummaryRefreshContexts())...)
		return m, tea.Batch(cmds...)

	case summaryLoadedMsg:
		state := m.contextSummaries[msg.ContextName]
		state.Loading = false
		state.Err = msg.Err
		if msg.Err == nil {
			state.Summary = msg.Summary
			state.Loaded = true
		}
		m.contextSummaries[msg.ContextName] = state
		if ctx, ok := m.selectedContext(); ok && ctx.Name == msg.ContextName {
			m.syncSelectedSummary()
			m.syncDetailContent()
		}
		return m, nil

	case contextLifecycleFinishedMsg:
		delete(m.contextActions, msg.ContextName)
		if msg.Err != nil {
			m.lastMessage = fmt.Sprintf("Failed to %s context %s: %v", msg.Action, msg.ContextName, msg.Err)
			if output := strings.TrimSpace(msg.Output); output != "" {
				m.lastMessage += " · " + truncateMetricText(strings.Join(strings.Fields(output), " "), 120)
			}
		} else {
			m.lastMessage = fmt.Sprintf("Context %s %s completed.", msg.ContextName, msg.Action)
		}
		ctx, ok := findContextByName(m.sites, msg.ContextName)
		if !ok {
			return m, nil
		}
		return m, tea.Batch(m.queueSummaryLoads([]config.Context{ctx})...)

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
			m.logsBody = content
			m.logs.SetContent(content)
			m.logs.GotoBottom()
		}
		return m, nil

	case commandStreamStartedMsg:
		if msg.ID != m.commandRunID {
			return m, nil
		}
		return m, waitForCommandStream(msg.ID, msg.Events)

	case commandStreamEventMsg:
		if msg.Event.ID != m.commandRunID {
			return m, nil
		}
		if msg.Event.Output != "" {
			m.appendCommandOutput(msg.Event.Output)
		}
		if msg.Event.Done {
			m.commandRunning = false
			m.commandQuitArmed = false
			m.commandCancel = nil
			if msg.Event.Canceled {
				if !m.commandOutput {
					m.logsBody = "Command stopped."
					m.logs.SetContent(m.logsBody)
				}
				m.lastMessage = fmt.Sprintf("Command stopped: %s", msg.Event.Command)
			} else if msg.Event.Err != nil {
				detail := fmt.Sprintf("\n\nCommand failed: %v", msg.Event.Err)
				m.appendCommandOutput(detail)
				m.lastMessage = fmt.Sprintf("Command failed: %v", msg.Event.Err)
			} else {
				if !m.commandOutput {
					m.logsBody = "Command completed with no output."
					m.logs.SetContent(m.logsBody)
					m.logs.GotoTop()
				}
				m.lastMessage = fmt.Sprintf("Command finished: %s", msg.Event.Command)
			}
			return m, reloadStateCmd()
		}
		return m, waitForCommandStream(msg.Event.ID, msg.Events)

	case commandFinishedMsg:
		m.commandRunning = false
		m.commandQuitArmed = false
		m.commandCancel = nil
		m.screen = screenLogs
		m.logsTitle = "Command Output"
		m.logTarget = ""
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
		m.logsBody = content
		m.logs.SetContent(content)
		m.logs.GotoTop()
		m.syncLayout()
		return m, nil

	case commandExecFinishedMsg:
		m.commandRunning = false
		m.commandQuitArmed = false
		if msg.Err == nil && strings.TrimSpace(m.pendingWorkingDir) != "" {
			if err := os.Chdir(m.pendingWorkingDir); err != nil {
				msg.Err = fmt.Errorf("command completed but sitectl could not leave the deleted project directory: %w", err)
			}
		}
		m.pendingWorkingDir = ""
		if msg.Err != nil {
			m.lastMessage = fmt.Sprintf("Command failed: %v", msg.Err)
		} else {
			m.lastMessage = fmt.Sprintf("Terminal command finished: %s", msg.Command)
		}
		return m, reloadStateCmd()

	case stateReloadedMsg:
		if msg.Err != nil {
			m.lastMessage = fmt.Sprintf("Failed to reload sitectl state: %v", msg.Err)
			return m, nil
		}
		preserveCommandOutput := m.screen == screenLogs && m.logsTitle == "Command Output"
		m.cfg = msg.Config
		m.sites = groupContexts(msg.Config)
		m.plugins = msg.Plugins
		m.currentContext = msg.CurrentContext
		selection := m.currentContext
		if _, ok := findContextByName(m.sites, m.contextMenuName); ok {
			selection = m.contextMenuName
		}
		if strings.TrimSpace(m.pendingContextSelection) != "" {
			if _, ok := findContextByName(m.sites, m.pendingContextSelection); ok {
				selection = m.pendingContextSelection
			}
			m.pendingContextSelection = ""
		}
		m.siteIndex, m.envIndex = defaultSelection(m.sites, selection)
		m.contextPage = 0
		m.localRefreshCursor = 0
		m.contextSummaries = retainContextSummaries(m.contextSummaries, m.sites)
		if _, ok := findContextByName(m.sites, m.contextMenuName); !ok {
			m.contextMenuName = ""
		}
		m.syncContextPageToSelection()
		m.summary = docker.ProjectSummary{}
		m.summaryErr = nil
		m.loading = false
		m.loadingLog = false
		m.logsErr = nil
		if !preserveCommandOutput {
			m.logsTitle = "Logs"
			m.logTarget = ""
			m.screen = screenDashboard
		}
		m.chooser = newMenuModel(chooserTitle(m.sites), chooserItems(m.sites, m.plugins))
		m.refreshCommandSuggestions()
		m.syncLayout()
		cmds := m.queueSummaryLoads(m.nextSummaryRefreshContexts())
		return m, tea.Batch(cmds...)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		if click, ok := msg.(tea.MouseClickMsg); ok {
			return m.handleMouseClick(click)
		}
		if m.screen == screenLogs {
			var cmd tea.Cmd
			m.logs, cmd = m.logs.Update(msg)
			return m, cmd
		}
		switch msg := msg.(type) {
		case tea.MouseWheelMsg:
			if !m.hasContexts() && !m.creatingContext && m.screen == screenDashboard {
				if z := zone.Get(onboardingListZoneID); z != nil && z.InBounds(msg) {
					switch msg.Mouse().Button {
					case tea.MouseWheelUp:
						m.chooser.CursorUp()
					case tea.MouseWheelDown:
						m.chooser.CursorDown()
					}
					return m, nil
				}
			}
			if z := zone.Get("contexts:pager"); z != nil && z.InBounds(msg) {
				switch msg.Mouse().Button {
				case tea.MouseWheelUp:
					m.changeContextPage(-1)
				case tea.MouseWheelDown:
					m.changeContextPage(1)
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
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
	if m.creatingContext {
		return m.handleContextNameKey(msg)
	}
	if !m.hasContexts() {
		if m.screen == screenTour {
			return m.handleTourKey(msg)
		}
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case msg.String() == "enter":
			return m.handleOnboardingSelection()
		default:
			var cmd tea.Cmd
			m.chooser, cmd = m.chooser.Update(msg)
			return m, cmd
		}
	}

	if m.commandInput.Focused() {
		switch {
		case msg.String() == "ctrl+c":
			if m.commandRunning && m.commandCancel != nil {
				m.commandCancel()
				m.commandCancel = nil
				m.lastMessage = "Stopping command..."
				return m, nil
			}
			if strings.TrimSpace(m.commandInput.Value()) != "" {
				m.commandInput.SetValue("")
				m.resetCommandHistoryNavigation()
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
			m.resetCommandHistoryNavigation()
			m.commandInput.Blur()
			return m, nil
		case key.Matches(msg, m.keys.Terminal):
			m.commandQuitArmed = false
			m.resetCommandHistoryNavigation()
			return m.runCommand(true)
		case msg.String() == "enter":
			m.commandQuitArmed = false
			m.resetCommandHistoryNavigation()
			return m.runCommand(false)
		case msg.String() == "up":
			m.commandQuitArmed = false
			m.previousCommandHistory()
			return m, nil
		case msg.String() == "down":
			m.commandQuitArmed = false
			m.nextCommandHistory()
			return m, nil
		default:
			m.commandQuitArmed = false
			m.resetCommandHistoryNavigation()
			var cmd tea.Cmd
			m.commandInput, cmd = m.commandInput.Update(msg)
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.commandRunning && m.commandCancel != nil {
			m.commandCancel()
			m.commandCancel = nil
			m.lastMessage = "Stopping command..."
			return m, nil
		}
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back):
		if m.contextMenuName != "" {
			m.contextMenuName = ""
			return m, nil
		}
		if m.screen == screenLogs {
			if m.commandRunning && m.commandCancel != nil {
				m.commandCancel()
				m.commandCancel = nil
				m.lastMessage = "Stopping command..."
				return m, nil
			}
			m.screen = screenDashboard
			m.syncLayout()
			return m, nil
		}
		return m, tea.Quit
	}

	if m.screen == screenLogs {
		switch {
		case key.Matches(msg, m.keys.Refresh):
			if ctx, ok := m.selectedContext(); ok && strings.HasPrefix(m.logsTitle, "Logs") {
				return m, loadLogsCmd(ctx)
			}
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
		m.contextMenuName = ""
		if m.moveContextSelection(-1) {
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Right):
		m.contextMenuName = ""
		if m.moveContextSelection(1) {
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Up):
		m.contextMenuName = ""
		if m.moveContextSelection(-m.contextsPerPage()) {
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Down):
		m.contextMenuName = ""
		if m.moveContextSelection(m.contextsPerPage()) {
			return m.reloadSelected()
		}
	case key.Matches(msg, m.keys.Create):
		return m.beginContextCreation()
	case key.Matches(msg, m.keys.Delete):
		return m.deleteSelectedContext()
	case key.Matches(msg, m.keys.Lifecycle):
		return m.toggleSelectedContext()
	case key.Matches(msg, m.keys.Command):
		m.contextMenuName = ""
		m.commandInput.Focus()
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		if ctx, ok := m.selectedContext(); ok {
			return m, tea.Batch(m.queueSummaryLoads([]config.Context{ctx})...)
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

func (m *dashboardModel) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if m.screen == screenLogs {
		if msg.Mouse().Button == tea.MouseLeft {
			if z := zone.Get("logs:back"); z != nil && z.InBounds(msg) {
				m.screen = screenDashboard
				m.logTarget = ""
				m.syncLayout()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.logs, cmd = m.logs.Update(msg)
		return m, cmd
	}

	if !m.hasContexts() && !m.creatingContext && m.screen == screenDashboard && msg.Mouse().Button == tea.MouseLeft {
		for index := range m.chooser.Items() {
			if z := zone.Get(onboardingItemZoneID(index)); z != nil && z.InBounds(msg) {
				m.chooser.Select(index)
				return m.handleOnboardingSelection()
			}
		}
		return m, nil
	}

	if msg.Mouse().Button == tea.MouseLeft {
		if z := zone.Get("context-menu:delete"); z != nil && z.InBounds(msg) {
			return m.deleteSelectedContext()
		}
		if z := zone.Get("context-menu:close"); z != nil && z.InBounds(msg) {
			m.contextMenuName = ""
			return m, nil
		}
		if z := zone.Get("context:add"); z != nil && z.InBounds(msg) {
			m.contextMenuName = ""
			return m.beginContextCreation()
		}
	}

	for index, position := range contextPositions(m.sites) {
		ctx := position.Context
		if msg.Mouse().Button == tea.MouseLeft {
			for _, action := range []string{"start", "stop"} {
				if z := zone.Get(contextLifecycleZoneID(action, ctx.Name)); z != nil && z.InBounds(msg) {
					m.contextMenuName = ""
					m.selectContextPosition(position, index)
					return m.runContextLifecycle(ctx, action)
				}
			}
		}
		if z := zone.Get("context:" + position.Context.Name); z != nil && z.InBounds(msg) {
			switch msg.Mouse().Button {
			case tea.MouseLeft:
				m.contextMenuName = ""
				m.selectContextPosition(position, index)
				return m.reloadSelected()
			case tea.MouseRight:
				m.contextMenuName = ctx.Name
				m.selectContextPosition(position, index)
				return m.reloadSelected()
			}
		}
	}
	if msg.Mouse().Button == tea.MouseLeft {
		for _, service := range m.summary.Services {
			if z := zone.Get(containerZoneID(service.Name)); z != nil && z.InBounds(msg) {
				m.contextMenuName = ""
				return m.openContainerLogs(service.Name)
			}
		}
	}

	return m, nil
}

func (m *dashboardModel) selectContextPosition(position contextPosition, index int) {
	m.commandInput.Blur()
	m.commandQuitArmed = false
	m.resetCommandHistoryNavigation()
	m.siteIndex = position.SiteIndex
	m.envIndex = position.EnvIndex
	m.contextPage = index / m.contextsPerPage()
	m.syncSelectedSummary()
	m.refreshCommandSuggestions()
	m.syncLayout()
}

func (m *dashboardModel) openContainerLogs(containerName string) (tea.Model, tea.Cmd) {
	ctx, ok := m.selectedContext()
	if !ok {
		return m, nil
	}
	m.logTarget = containerName
	m.screen = screenLogs
	m.loadingLog = true
	m.logsTitle = "Container Logs"
	m.syncLayout()
	return m, loadContainerLogsCmd(ctx, containerName)
}

func (m *dashboardModel) beginContextCreation() (tea.Model, tea.Cmd) {
	m.creatingContext = true
	m.contextMenuName = ""
	m.lastMessage = ""
	m.commandInput.Blur()
	m.contextNameInput.SetValue("")
	m.contextNameInput.Focus()
	return m, textinput.Blink
}

func (m *dashboardModel) handleContextNameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Quit):
		m.creatingContext = false
		m.contextNameInput.Blur()
		m.lastMessage = "Context creation cancelled."
		return m, nil
	case msg.String() == "enter":
		name := strings.TrimSpace(m.contextNameInput.Value())
		if name == "" {
			m.lastMessage = "Enter a context name before continuing."
			return m, nil
		}
		if strings.HasPrefix(name, "-") {
			m.lastMessage = "Context names cannot begin with a dash."
			return m, nil
		}
		if _, ok := findContextByName(m.sites, name); ok {
			m.lastMessage = fmt.Sprintf("Context %q already exists; choose a new name.", name)
			return m, nil
		}
		m.creatingContext = false
		m.contextNameInput.Blur()
		m.commandRunning = true
		m.pendingContextSelection = name
		display := "sitectl config set-context " + shellquote.Join(name)
		return m, runSitectlInteractiveCmd(display, []string{"config", "set-context", name})
	default:
		var cmd tea.Cmd
		m.contextNameInput, cmd = m.contextNameInput.Update(msg)
		return m, cmd
	}
}

func (m *dashboardModel) deleteSelectedContext() (tea.Model, tea.Cmd) {
	ctx, ok := m.selectedContext()
	if !ok || m.commandRunning {
		return m, nil
	}
	if action := strings.TrimSpace(m.contextActions[ctx.Name]); action != "" {
		m.lastMessage = fmt.Sprintf("Wait for context %s to finish %s before deleting it.", ctx.Name, contextLifecycleProgress(action))
		return m, nil
	}
	m.pendingWorkingDir = ""
	if ctx.DockerHostType == config.ContextLocal && strings.TrimSpace(ctx.ProjectDir) != "" {
		projectDir, pathErr := filepath.Abs(strings.TrimSpace(ctx.ProjectDir))
		cwd, cwdErr := os.Getwd()
		if pathErr == nil && cwdErr == nil && directoryContains(projectDir, cwd) {
			m.pendingWorkingDir = filepath.Dir(projectDir)
		}
	}
	m.commandRunning = true
	display := "sitectl config delete-context --delete-project -- " + shellquote.Join(ctx.Name)
	return m, runSitectlInteractiveCmd(display, []string{"config", "delete-context", "--delete-project", "--", ctx.Name})
}

func directoryContains(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func (m *dashboardModel) toggleSelectedContext() (tea.Model, tea.Cmd) {
	ctx, ok := m.selectedContext()
	if !ok {
		return m, nil
	}
	state := m.contextSummaries[ctx.Name]
	action, ok := contextLifecycleAction(state)
	if !ok {
		m.lastMessage = "Context stats must load before it can be started or stopped."
		return m, nil
	}
	return m.runContextLifecycle(ctx, action)
}

func (m *dashboardModel) runContextLifecycle(ctx config.Context, action string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(m.contextActions[ctx.Name]) != "" {
		return m, nil
	}
	state := m.contextSummaries[ctx.Name]
	expected, ok := contextLifecycleAction(state)
	if !ok || action != expected {
		m.lastMessage = fmt.Sprintf("Context %s no longer has a %s action available.", ctx.Name, action)
		return m, nil
	}
	if m.contextActions == nil {
		m.contextActions = map[string]string{}
	}
	m.contextActions[ctx.Name] = action
	m.lastMessage = fmt.Sprintf("%s context %s...", strings.ToUpper(action[:1])+action[1:], ctx.Name)
	return m, runContextLifecycleCmd(ctx, action, state.Summary)
}

func contextLifecycleAction(state contextSummaryState) (string, bool) {
	if !state.Loaded || state.Err != nil {
		return "", false
	}
	if state.Summary.Running > 0 {
		return "stop", true
	}
	return "start", true
}

func runContextLifecycleCmd(ctx config.Context, action string, summary docker.ProjectSummary) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return contextLifecycleFinishedMsg{ContextName: ctx.Name, Action: action, Err: err}
		}
		composeAction := action
		if action == "start" && summary.Total == 0 {
			composeAction = "up"
		}
		args := []string{"--context", ctx.Name, "compose", composeAction}
		command := exec.Command(exe, args...) // #nosec G204 -- the executable and arguments are internally constructed from the selected saved context.
		output, runErr := command.CombinedOutput()
		return contextLifecycleFinishedMsg{
			ContextName: ctx.Name,
			Action:      action,
			Output:      string(output),
			Err:         runErr,
		}
	}
}

func (m *dashboardModel) reloadSelected() (tea.Model, tea.Cmd) {
	m.syncContextPageToSelection()
	m.syncSelectedSummary()
	m.refreshCommandSuggestions()
	m.syncDetailContent()
	if ctx, ok := m.selectedContext(); ok {
		cmds := m.queueSummaryLoads([]config.Context{ctx})
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
	if m.creatingContext {
		return docStyle.Render(m.renderContextNamePrompt())
	}
	if !m.hasContexts() && m.screen == screenTour {
		return docStyle.Render(m.renderTourArea())
	}
	if !m.hasContexts() {
		return docStyle.Render(m.renderOnboarding())
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.renderTitle(),
		m.renderContextCards(max(m.width-6, 80)),
		m.renderMainArea(),
		m.renderCommandFooter(),
		footerStyle.Render(m.help.View(m.keys)),
	)

	if strings.TrimSpace(m.lastMessage) != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, body, subtleStyle.Render(m.lastMessage))
	}

	return docStyle.Render(body)
}

func (m *dashboardModel) renderTitle() string {
	site := m.sites[m.siteIndex]
	ctx, _ := m.selectedContext()
	contextName := "-"
	if ctx.Name != "" {
		contextName = ctx.Name
	}
	line := strings.Repeat("-", max(4, m.width-len(site.Name)-len(contextName)-24))
	return titleStyle.Render(fmt.Sprintf(" Sitectl | %s | %s ", contextName, site.Name)) + subtleStyle.Render(line)
}

func (m *dashboardModel) renderContextCards(width int) string {
	positions := contextPositions(m.sites)
	if len(positions) == 0 {
		return ""
	}

	perPage := contextCardsPerPage(width)
	pageCount := (len(positions) + perPage - 1) / perPage
	page := min(max(m.contextPage, 0), pageCount-1)
	start := page * perPage
	end := min(start+perPage, len(positions))

	heading := sectionTitleStyle.MarginBottom(0).Render("Contexts")
	pager := subtleStyle.Render(fmt.Sprintf(
		"  %d saved · page %d of %d · right-click for info · remote stats load when active",
		len(positions),
		page+1,
		pageCount,
	))

	const addCardWidth = 22
	cardWidth := max(28, (width-addCardWidth-perPage)/perPage)
	cards := make([]string, 0, end-start+1)
	for index := start; index < end; index++ {
		position := positions[index]
		active := position.SiteIndex == m.siteIndex && position.EnvIndex == m.envIndex
		cards = append(cards, m.renderContextCard(position, active, cardWidth))
	}
	cards = append(cards, m.renderAddContextCard(addCardWidth))

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		heading+pager,
		lipgloss.JoinHorizontal(lipgloss.Top, cards...),
	)
	return zone.Mark("contexts:pager", content)
}

func (m *dashboardModel) renderAddContextCard(width int) string {
	style := addContextCardStyle.Width(width).Height(6)
	frameWidth, _ := style.GetFrameSize()
	innerWidth := max(20, width-frameWidth)
	center := lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center)
	plus := accentStyle.Render("    │\n ───┼───\n    │")
	content := strings.Join([]string{
		center.Render(plus),
		center.Bold(true).Render("Add a new context"),
		center.Foreground(lipgloss.Color("#7C98B3")).Render("click or press n"),
	}, "\n")
	return zone.Mark("context:add", style.Render(content))
}

func (m *dashboardModel) renderContextCard(position contextPosition, active bool, width int) string {
	ctx := position.Context
	state := m.contextSummaries[ctx.Name]
	style := contextCardStyle.Width(width).Height(6)
	if active {
		style = activeContextCardStyle.Width(width).Height(6)
	}
	frameWidth, _ := style.GetFrameSize()
	innerWidth := max(20, width-frameWidth)

	title := ctx.Name
	if ctx.Name == m.currentContext {
		title += "  current"
	}
	if active {
		title = "› " + title
	}

	kind := strings.ToUpper(helpers.FirstNonEmpty(string(ctx.DockerHostType), "unknown"))
	location := fmt.Sprintf("%s · %s/%s", kind, position.SiteName, envLabel(ctx))
	statusText, statusColor := contextSummaryStatus(state)
	status := lipgloss.NewStyle().Foreground(lipgloss.Color(statusColor)).Render(statusText)
	metaWidth := max(6, innerWidth-lipgloss.Width(status)-2)
	meta := subtleStyle.Render(truncateMetricText(location, metaWidth))

	cpuKnown := state.Loaded
	memKnown := state.Loaded && state.Summary.MemoryLimitBytes > 0
	diskKnown := state.Loaded && state.Summary.DiskTotal > 0
	memoryDetail := "n/a"
	if memKnown {
		memoryDetail = fmt.Sprintf("%s/%s", humanBytes(state.Summary.MemoryBytes), humanBytes(state.Summary.MemoryLimitBytes))
	}
	diskDetail := "n/a"
	diskPercent := 0.0
	if diskKnown {
		diskPercent = (float64(state.Summary.DiskAvailable) / float64(state.Summary.DiskTotal)) * 100
		diskDetail = humanBytes(state.Summary.DiskAvailable) + " free"
	}

	lines := []string{
		titleStyle.Render(truncateMetricText(title, innerWidth)),
		status + "  " + meta,
		renderContextMetric("cpu", state.Summary.CPUPercent, fmt.Sprintf("%.0f%%", state.Summary.CPUPercent), severityColor(state.Summary.CPUPercent, 70, 90), cpuKnown, innerWidth),
		renderContextMetric("mem", memoryPercent(state.Summary), memoryDetail, severityColor(memoryPercent(state.Summary), 70, 90), memKnown, innerWidth),
		renderContextMetric("disk", diskPercent, diskDetail, reverseSeverityColor(diskPercent, 25, 10), diskKnown, innerWidth),
		m.renderContextCardFooter(ctx, state, innerWidth),
	}

	return zone.Mark("context:"+ctx.Name, style.Render(strings.Join(lines, "\n")))
}

func (m *dashboardModel) renderContextCardFooter(ctx config.Context, state contextSummaryState, width int) string {
	footer := contextSummaryFooter(ctx, state)
	pending := strings.TrimSpace(m.contextActions[ctx.Name])
	if pending != "" {
		label := "[" + contextLifecycleProgress(pending) + "...]"
		textWidth := max(0, width-lipgloss.Width(label)-1)
		return subtleStyle.Render(truncateMetricText(footer, textWidth)) + " " + contextActionPendingStyle.Render(label)
	}
	action, ok := contextLifecycleAction(state)
	if !ok {
		return subtleStyle.Render(truncateMetricText(footer, width))
	}
	label := "[" + action + "]"
	textWidth := max(0, width-lipgloss.Width(label)-1)
	button := zone.Mark(contextLifecycleZoneID(action, ctx.Name), contextActionStyle.Render(label))
	return subtleStyle.Render(truncateMetricText(footer, textWidth)) + " " + button
}

func contextLifecycleZoneID(action, contextName string) string {
	return "context:" + action + ":" + contextName
}

func contextLifecycleProgress(action string) string {
	if action == "stop" {
		return "stopping"
	}
	return action + "ing"
}

func contextSummaryStatus(state contextSummaryState) (string, string) {
	switch {
	case state.Loaded && state.Err != nil:
		return "● stale", "#E9C46A"
	case state.Loaded:
		status := strings.ToLower(helpers.FirstNonEmpty(state.Summary.Status, "unknown"))
		color := "#2A9D8F"
		if state.Summary.Running == 0 {
			color = "#7F8C8D"
		} else if state.Summary.Stopped > 0 {
			color = "#E9C46A"
		}
		return "● " + status, color
	case state.Loading:
		return "◌ loading", "#7F8C8D"
	case state.Err != nil:
		return "● unavailable", "#E76F51"
	default:
		return "○ unknown", "#7F8C8D"
	}
}

func contextSummaryFooter(ctx config.Context, state contextSummaryState) string {
	switch {
	case state.Loaded && state.Err != nil:
		return "refresh failed · showing cached stats"
	case state.Loaded && state.Loading:
		return "refreshing cached stats..."
	case state.Loaded && !state.Summary.CollectedAt.IsZero():
		age := time.Since(state.Summary.CollectedAt).Round(time.Second)
		if age < 0 {
			age = 0
		}
		return fmt.Sprintf("updated %s ago", age)
	case state.Err != nil:
		return "stats unavailable · ctrl+r retries"
	case state.Loading:
		return "loading stats..."
	case ctx.DockerHostType == config.ContextRemote:
		return "select this context to load stats"
	default:
		return "waiting for local stats"
	}
}

func renderContextMetric(label string, percent float64, detail, color string, known bool, width int) string {
	labelWidth := 5
	detailWidth := min(14, max(7, width/3))
	barWidth := max(4, width-labelWidth-detailWidth-2)
	labelText := subtleStyle.Width(labelWidth).Render(label)

	if !known {
		bar := unknownMetricStyle.Render(fuzzyMetricBar(barWidth))
		value := unknownMetricStyle.Width(detailWidth).Align(lipgloss.Right).Render("unknown")
		return labelText + " " + bar + " " + value
	}

	filled := int((clamp(percent, 0, 100) / 100) * float64(barWidth))
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.Repeat("█", filled)) +
		subtleStyle.Render(strings.Repeat("░", max(0, barWidth-filled)))
	value := subtleStyle.Width(detailWidth).Align(lipgloss.Right).Render(truncateMetricText(detail, detailWidth))
	return labelText + " " + bar + " " + value
}

func fuzzyMetricBar(width int) string {
	if width <= 0 {
		return ""
	}
	pattern := []rune("░▒")
	bar := make([]rune, width)
	for index := range bar {
		bar[index] = pattern[index%len(pattern)]
	}
	return string(bar)
}

func (m *dashboardModel) renderMainArea() string {
	switch m.screen {
	case screenLogs:
		return m.renderLogsArea()
	case screenTour:
		return m.renderTourArea()
	default:
		return m.renderDashboardArea()
	}
}

func (m *dashboardModel) renderDashboardArea() string {
	width := max(m.width-6, 80)
	if m.contextMenuName != "" {
		return m.renderContextMenuPanel(width)
	}
	return m.renderDetailsPanel(width)
}

func (m *dashboardModel) renderContextMenuPanel(width int) string {
	panelWidth := max(40, width-2)
	innerWidth := max(34, panelWidth-6)
	panelHeight := min(max(9, m.height-24), 18)
	ctx, ok := m.selectedContext()
	if !ok {
		return panelStyle.Width(panelWidth).Height(panelHeight).Render("No context selected.")
	}

	state := m.contextSummaries[ctx.Name]
	statusText, statusColor := contextSummaryStatus(state)
	status := lipgloss.NewStyle().Foreground(lipgloss.Color(statusColor)).Render(statusText)
	runtime := status
	if state.Loaded {
		runtime += subtleStyle.Render(fmt.Sprintf(" · %d/%d containers running", state.Summary.Running, state.Summary.Total))
	}

	target := strings.ToUpper(helpers.FirstNonEmpty(string(ctx.DockerHostType), "unknown"))
	switch ctx.DockerHostType {
	case config.ContextRemote:
		endpoint := helpers.FirstNonEmpty(ctx.SSHHostname, "unknown host")
		if strings.TrimSpace(ctx.SSHUser) != "" {
			endpoint = ctx.SSHUser + "@" + endpoint
		}
		if ctx.SSHPort != 0 {
			endpoint += fmt.Sprintf(":%d", ctx.SSHPort)
		}
		target += " · " + endpoint
	case config.ContextLocal:
		target += " · this machine"
	}

	defaultLabel := "no"
	if ctx.Name == m.currentContext {
		defaultLabel = "yes"
	}
	buttons := zone.Mark("context-menu:delete", dangerChipStyle.Render("[Delete context]")) +
		zone.Mark("context-menu:close", contextMenuCloseStyle.Render("[Close]"))
	lines := []string{
		sectionTitleStyle.MarginBottom(0).Render("Context information"),
		truncateMetricText(fmt.Sprintf("%s · %s/%s · default: %s", ctx.Name, m.selectedSiteName(), envLabel(ctx), defaultLabel), innerWidth),
		truncateMetricText(fmt.Sprintf("Target: %s · Plugin: %s", target, helpers.FirstNonEmpty(ctx.Plugin, "core")), innerWidth),
		truncateMetricText(fmt.Sprintf("Project: %s · Compose: %s", helpers.FirstNonEmpty(ctx.ProjectDir, "not configured"), helpers.FirstNonEmpty(ctx.EffectiveComposeProjectName(), "auto")), innerWidth),
		"Runtime: " + runtime + "  " + buttons,
	}
	return panelStyle.Width(panelWidth).Height(panelHeight).Render(strings.Join(lines, "\n"))
}

func (m *dashboardModel) renderLogsArea() string {
	ctx, _ := m.selectedContext()
	hint := "Auto-refreshing the latest 20 log lines. Scroll with mouse wheel or j/k. Press esc to return."
	if m.logsTitle == "Command Output" {
		hint = "Command output. Press esc to return to the dashboard and keep using the footer command bar."
	} else if strings.TrimSpace(m.logTarget) != "" {
		hint = "Auto-refreshing the latest 20 log lines for the selected container. Press esc to return."
	}
	back := zone.Mark("logs:back", chipStyle.Render("[Back]"))
	headerLines := []string{
		sectionTitleStyle.MarginBottom(0).Render(m.logsTitle),
		fmt.Sprintf("Context: %s", ctx.Name),
	}
	if strings.TrimSpace(m.logTarget) != "" {
		headerLines = append(headerLines, renderContainerHeader(m.summary, m.logTarget))
	}
	headerLines = append(headerLines, hint)
	header := panelStyle.Width(max(40, m.width-6)).Render(strings.Join(headerLines, "\n"))
	body := panelStyle.Width(max(40, m.width-6)).Height(max(10, m.height-14)).Render(
		back + "\n" + renderViewportWithScrollbar(m.logs, m.logsBody, max(34, m.width-12)),
	)
	if m.loadingLog {
		header = panelStyle.Width(max(40, m.width-6)).Render(m.spin.View() + " Loading logs...\nContext: " + ctx.Name)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func (m *dashboardModel) renderDetailsPanel(width int) string {
	panelWidth := max(40, width-2)
	content := renderViewportWithScrollbar(m.detail, m.detailBody, panelWidth-6)
	ctx, _ := m.selectedContext()
	state := m.contextSummaries[ctx.Name]
	if m.loading && !state.Loaded {
		content = m.spin.View() + " Loading Docker Compose status..."
	}

	panelHeight := min(max(9, m.height-24), 18)
	return panelStyle.Width(panelWidth).Height(panelHeight).Render(
		sectionTitleStyle.MarginBottom(0).Render("Active Context Containers") + "\n" + content,
	)
}

func (m *dashboardModel) renderOnboarding() string {
	width := max(56, min(88, m.width-10))
	intro := panelStyle.Width(width).Render(strings.Join([]string{
		titleStyle.Render("Sitectl | Get Started"),
		"",
		"No contexts are configured yet.",
		"Set up an existing Docker Compose site with sitectl, or create a new site from an installed plugin.",
		"",
		"Scroll and click an option to launch it. Keyboard navigation remains available.",
	}, "\n"))

	menu := panelStyle.Width(width).Render(zone.Mark(onboardingListZoneID, m.chooser.View()))
	footer := footerStyle.Width(width).Render("click: launch  wheel: scroll  enter: launch  q: quit")
	body := lipgloss.JoinVertical(lipgloss.Left, intro, menu, footer)
	if strings.TrimSpace(m.lastMessage) != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, body, subtleStyle.Render(m.lastMessage))
	}
	return body
}

func (m *dashboardModel) renderContextNamePrompt() string {
	width := max(56, min(88, m.width-10))
	body := panelStyle.Width(width).Render(strings.Join([]string{
		titleStyle.Render("Add a new context"),
		"",
		"Name the saved local or remote site environment.",
		"After you press enter, sitectl will open the existing reviewed context setup in your terminal.",
		"",
		m.contextNameInput.View(),
		"",
		subtleStyle.Render("enter: continue  esc: cancel"),
	}, "\n"))
	if strings.TrimSpace(m.lastMessage) != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, body, subtleStyle.Render(m.lastMessage))
	}
	return body
}

func (m *dashboardModel) renderTourArea() string {
	width := max(56, m.width-6)
	header := panelStyle.Width(width).Render(strings.Join([]string{
		titleStyle.Render("Sitectl Tour"),
		m.currentTourTitle(),
		fmt.Sprintf("Pane %d of %d", m.currentTourIndex()+1, len(m.tourPanes)),
		"left/right: next section  esc: back to setup/create",
	}, "\n"))
	body := panelStyle.Width(width).Height(max(12, m.height-12)).Render(renderViewportWithScrollbar(m.detail, m.detailBody, width-6))
	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func (m *dashboardModel) renderCommandFooter() string {
	contextName := m.selectedContextName()
	status := accentStyle.Render("ready")
	if m.commandRunning {
		status = accentStyle.Render(m.spin.View() + " running")
	}
	hint := subtleStyle.Render("press / for command  enter: run  ctrl+x: terminal")
	bar := footerCommandStyle.Width(max(40, m.width-6)).Render(
		fmt.Sprintf("Context: %s  [%s]\n%s\n%s", contextName, status, m.commandInput.View(), hint),
	)
	return bar
}

func (m *dashboardModel) syncLayout() {
	hpad, _ := docStyle.GetFrameSize()
	m.help.SetWidth(max(20, m.width-hpad))

	detailHeight := min(max(8, m.height-32), 14)
	if m.screen == screenTour {
		detailHeight = max(12, m.height-16)
	}
	m.detail.SetWidth(max(48, m.width-hpad-6))
	m.detail.SetHeight(detailHeight)

	logHeight := max(10, m.height-14)
	m.logs.SetWidth(max(30, m.width-hpad-8))
	m.logs.SetHeight(logHeight)

	chooserWidth := min(72, max(48, m.width-12))
	m.chooser.SetSize(chooserWidth, min(18, max(10, m.height/2)))
	m.contextNameInput.SetWidth(max(24, min(56, m.width-28)))
	m.commandInput.SetWidth(max(20, m.width-18))
	m.commandInput.Prompt = "sitectl --context " + m.selectedContextName() + " "
	m.syncContextPageToSelection()

	m.syncDetailContent()
}

func (m *dashboardModel) syncDetailContent() {
	if m.screen == screenTour {
		return
	}
	ctx, ok := m.selectedContext()
	if !ok {
		m.detailBody = "No context selected."
		m.detail.SetContent(m.detailBody)
		return
	}
	state := m.contextSummaries[ctx.Name]
	if !state.Loaded && state.Err == nil {
		m.detailBody = "Stats have not been loaded for this context yet."
		m.detail.SetContent(m.detailBody)
		return
	}
	if m.summaryErr != nil {
		m.detailBody = m.summaryErr.Error()
		m.detail.SetContent(m.detailBody)
		return
	}

	lines := []string{
		"Containers",
		"Click a container to view its logs.",
		"",
		fmt.Sprintf("  %-36s  %7s  %-22s  %s", "NAME", "CPU", "MEMORY", "STATUS"),
		"  " + strings.Repeat("─", 36) + "  " + strings.Repeat("─", 7) + "  " + strings.Repeat("─", 22) + "  " + strings.Repeat("─", 12),
	}
	if len(m.summary.Services) == 0 {
		lines = append(lines, "  No Compose containers found for this context.")
	} else {
		for _, service := range m.summary.Services {
			line := fmt.Sprintf(
				"  %-36s  %6.1f%%  %-22s  %s",
				truncateMetricText(helpers.FirstNonEmpty(service.Name, service.Service), 36),
				service.CPUPercent,
				truncateMetricText(containerMemorySummary(service), 22),
				truncateMetricText(helpers.FirstNonEmpty(service.Status, service.State), 12),
			)
			lines = append(lines, zone.Mark(containerZoneID(service.Name), line))
		}
	}
	m.detailBody = strings.Join(lines, "\n")
	m.detail.SetContent(m.detailBody)
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

func (m *dashboardModel) selectedContextValue() config.Context {
	ctx, _ := m.selectedContext()
	return ctx
}

func contextPositions(sites []siteGroup) []contextPosition {
	positions := make([]contextPosition, 0)
	for siteIndex, site := range sites {
		for envIndex, ctx := range site.Contexts {
			positions = append(positions, contextPosition{
				SiteIndex: siteIndex,
				EnvIndex:  envIndex,
				SiteName:  site.Name,
				Context:   ctx,
			})
		}
	}
	return positions
}

func contextCardsPerPage(width int) int {
	switch {
	case width >= 96:
		return 3
	case width >= 62:
		return 2
	default:
		return 1
	}
}

func (m *dashboardModel) contextsPerPage() int {
	return contextCardsPerPage(max(m.width-6, 80))
}

func (m *dashboardModel) selectedContextPositionIndex() int {
	for index, position := range contextPositions(m.sites) {
		if position.SiteIndex == m.siteIndex && position.EnvIndex == m.envIndex {
			return index
		}
	}
	return 0
}

func (m *dashboardModel) syncContextPageToSelection() {
	if !m.hasContexts() {
		m.contextPage = 0
		return
	}
	m.contextPage = m.selectedContextPositionIndex() / m.contextsPerPage()
}

func (m *dashboardModel) moveContextSelection(delta int) bool {
	positions := contextPositions(m.sites)
	if len(positions) == 0 || delta == 0 {
		return false
	}
	current := m.selectedContextPositionIndex()
	next := min(max(current+delta, 0), len(positions)-1)
	if next == current {
		return false
	}
	m.siteIndex = positions[next].SiteIndex
	m.envIndex = positions[next].EnvIndex
	m.contextPage = next / m.contextsPerPage()
	return true
}

func (m *dashboardModel) changeContextPage(delta int) {
	positions := contextPositions(m.sites)
	if len(positions) == 0 || delta == 0 {
		return
	}
	pageCount := (len(positions) + m.contextsPerPage() - 1) / m.contextsPerPage()
	m.contextPage = min(max(m.contextPage+delta, 0), pageCount-1)
}

func findContextByName(sites []siteGroup, name string) (config.Context, bool) {
	for _, position := range contextPositions(sites) {
		if strings.EqualFold(position.Context.Name, strings.TrimSpace(name)) {
			return position.Context, true
		}
	}
	return config.Context{}, false
}

func summaryRefreshContexts(sites []siteGroup, selected config.Context, cursor, batchSize int) ([]config.Context, int) {
	contexts := make([]config.Context, 0)
	seen := map[string]struct{}{}
	if strings.TrimSpace(selected.Name) != "" {
		contexts = appendUniqueContext(contexts, seen, selected)
	}

	locals := make([]config.Context, 0)
	for _, position := range contextPositions(sites) {
		ctx := position.Context
		if ctx.DockerHostType == config.ContextLocal && ctx.Name != selected.Name {
			locals = append(locals, ctx)
		}
	}
	if len(locals) == 0 || batchSize <= 0 {
		return contexts, 0
	}

	cursor = min(max(cursor, 0), len(locals)-1)
	count := min(batchSize, len(locals))
	for offset := 0; offset < count; offset++ {
		ctx := locals[(cursor+offset)%len(locals)]
		contexts = appendUniqueContext(contexts, seen, ctx)
	}
	return contexts, (cursor + count) % len(locals)
}

func (m *dashboardModel) nextSummaryRefreshContexts() []config.Context {
	contexts, nextCursor := summaryRefreshContexts(
		m.sites,
		m.selectedContextValue(),
		m.localRefreshCursor,
		localSummaryBatchSize,
	)
	m.localRefreshCursor = nextCursor
	return contexts
}

func appendUniqueContext(contexts []config.Context, seen map[string]struct{}, ctx config.Context) []config.Context {
	name := strings.TrimSpace(ctx.Name)
	if name == "" {
		return contexts
	}
	if _, ok := seen[name]; ok {
		return contexts
	}
	seen[name] = struct{}{}
	return append(contexts, ctx)
}

func retainContextSummaries(existing map[string]contextSummaryState, sites []siteGroup) map[string]contextSummaryState {
	retained := make(map[string]contextSummaryState, len(existing))
	for _, position := range contextPositions(sites) {
		if state, ok := existing[position.Context.Name]; ok {
			state.Loading = false
			retained[position.Context.Name] = state
		}
	}
	return retained
}

func (m *dashboardModel) queueSummaryLoads(contexts []config.Context) []tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(contexts))
	for _, ctx := range contexts {
		if strings.TrimSpace(ctx.Name) == "" {
			continue
		}
		state := m.contextSummaries[ctx.Name]
		if state.Loading || strings.TrimSpace(m.contextActions[ctx.Name]) != "" {
			continue
		}
		state.Loading = true
		m.contextSummaries[ctx.Name] = state
		cmds = append(cmds, loadSummaryCmd(ctx))
	}
	m.syncSelectedSummary()
	return cmds
}

func (m *dashboardModel) syncSelectedSummary() {
	ctx, ok := m.selectedContext()
	if !ok {
		m.summary = docker.ProjectSummary{}
		m.summaryErr = nil
		m.loading = false
		return
	}
	state := m.contextSummaries[ctx.Name]
	m.summary = state.Summary
	m.loading = state.Loading
	if state.Loaded {
		m.summaryErr = nil
	} else {
		m.summaryErr = state.Err
	}
}

func newMenuModel(title string, items []menuItem) list.Model {
	delegate := list.NewDefaultDelegate()
	converted := make([]list.Item, 0, len(items))
	for _, item := range items {
		converted = append(converted, item)
	}
	m := list.New(converted, menuDelegate{DefaultDelegate: delegate}, 48, 12)
	m.Title = title
	m.SetShowTitle(false)
	m.SetFilteringEnabled(false)
	m.SetShowStatusBar(false)
	m.SetShowHelp(false)
	m.DisableQuitKeybindings()
	return m
}

func onboardingItemZoneID(index int) string {
	return fmt.Sprintf("onboarding:choice:%d", index)
}

func pluginMenuItems(plugins []plugin.InstalledPlugin) []menuItem {
	items := make([]menuItem, 0, len(plugins))
	for _, p := range plugins {
		for _, spec := range p.CreateDefinitions {
			pluginName := strings.TrimSpace(p.Name)
			definitionName := strings.TrimSpace(spec.Name)
			if pluginName == "" || definitionName == "" {
				continue
			}
			target := pluginName + "/" + definitionName
			label := pluginName
			if len(p.CreateDefinitions) > 1 || !strings.EqualFold(definitionName, "default") {
				label = target
			}
			description := helpers.FirstNonEmpty(spec.Description, spec.DockerComposeRepo, p.TemplateRepo, "sitectl create "+target)
			items = append(items, menuItem{
				title:  fmt.Sprintf("Create a new %s stack", label),
				desc:   description,
				action: "create:" + target,
			})
		}
	}
	if len(items) == 0 {
		items = append(items, menuItem{
			title:  "No create definitions found",
			desc:   "Install a sitectl-* plugin that registers a create definition.",
			action: "",
		})
	}
	return items
}

func loadTourPanes() []tuitour.Pane {
	panes, err := tuitour.Load()
	if err != nil {
		return nil
	}
	return panes
}

func loadSummaryCmd(ctx config.Context) tea.Cmd {
	return func() tea.Msg {
		summary, err := docker.SummarizeProject(&ctx)
		return summaryLoadedMsg{ContextName: ctx.Name, Summary: summary, Err: err}
	}
}

func loadContainerLogsCmd(ctx config.Context, containerName string) tea.Cmd {
	return func() tea.Msg {
		logs, err := fetchContainerLogs(ctx, containerName)
		return logsLoadedMsg{ContextName: ctx.Name, Logs: logs, Err: err}
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
		cmd := exec.Command("docker", args...) // #nosec G204 -- docker arguments are assembled by sitectl from context configuration without a shell.
		cmd.Dir = ctx.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	return ctx.RunQuietCommand(exec.Command("docker", args...)) // #nosec G204 -- docker arguments are assembled by sitectl from context configuration without a shell.
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
		siteName := helpers.FirstNonEmpty(ctx.Site, ctx.Name, "default")
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

func envLabel(ctx config.Context) string {
	return helpers.FirstNonEmpty(ctx.Environment, "unknown")
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
}

func (m *dashboardModel) hasContexts() bool {
	return len(m.sites) > 0
}

func chooserTitle(sites []siteGroup) string {
	if len(sites) == 0 {
		return "Get Started"
	}
	return "Choose An App"
}

func chooserItems(sites []siteGroup, plugins []plugin.InstalledPlugin) []menuItem {
	if len(sites) == 0 {
		items := []menuItem{
			{
				title:  "Take the Tour",
				desc:   "Overview of contexts, plugins, and components.",
				action: "tour",
			},
			{
				title:  "Set Up Existing Project",
				desc:   "Register an existing Docker Compose site with sitectl.",
				action: "config-create",
			},
		}
		items = append(items, pluginMenuItems(plugins)...)
		return items
	}
	return pluginMenuItems(plugins)
}

func (m *dashboardModel) handleOnboardingSelection() (tea.Model, tea.Cmd) {
	selected, ok := m.chooser.SelectedItem().(menuItem)
	if !ok || strings.TrimSpace(selected.action) == "" {
		return m, nil
	}
	return m.executeChooserAction(selected.action)
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
	if m.commandRunning {
		m.lastMessage = "A command is already running. Press ctrl+c to stop it."
		return m, nil
	}
	raw := strings.TrimSpace(m.commandInput.Value())
	if raw == "" {
		return m, nil
	}
	display, args, err := normalizeSitectlCommand(raw, m.selectedContextName())
	if err != nil {
		m.lastMessage = err.Error()
		return m, nil
	}
	m.addCommandHistory(raw)

	if interactive || isInteractiveArgs(args) {
		m.commandRunning = true
		m.commandOutput = false
		m.commandCancel = nil
		m.commandInput.SetValue("")
		return m, runSitectlInteractiveCmd(display, args)
	}

	streamArgs := streamSafeSitectlArgs(args)
	streamDisplay := "sitectl " + shellquote.Join(streamArgs...)
	m.commandRunning = true
	m.commandOutput = false
	m.commandRunID++
	runID := m.commandRunID
	runCtx, cancel := context.WithCancel(context.Background())
	m.commandCancel = cancel
	m.logsTitle = "Command Output"
	m.logTarget = ""
	m.logsBody = "Running " + streamDisplay + "..."
	m.logs.SetContent(m.logsBody)
	m.screen = screenLogs
	m.commandInput.SetValue("")
	return m, runSitectlStreamCmd(runCtx, runID, streamDisplay, streamArgs)
}

func (m *dashboardModel) addCommandHistory(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if len(m.commandHistory) > 0 && m.commandHistory[len(m.commandHistory)-1] == raw {
		m.resetCommandHistoryNavigation()
		return
	}
	m.commandHistory = append(m.commandHistory, raw)
	if len(m.commandHistory) > maxCommandHistoryEntries {
		m.commandHistory = m.commandHistory[len(m.commandHistory)-maxCommandHistoryEntries:]
	}
	m.resetCommandHistoryNavigation()
}

func (m *dashboardModel) previousCommandHistory() bool {
	if len(m.commandHistory) == 0 {
		return false
	}
	if m.commandHistoryAt == commandHistoryBrowseNone {
		m.commandDraft = m.commandInput.Value()
		m.commandHistoryAt = len(m.commandHistory) - 1
	} else if m.commandHistoryAt > 0 {
		m.commandHistoryAt--
	}
	m.setCommandInputValue(m.commandHistory[m.commandHistoryAt])
	return true
}

func (m *dashboardModel) nextCommandHistory() bool {
	if len(m.commandHistory) == 0 || m.commandHistoryAt == commandHistoryBrowseNone {
		return false
	}
	if m.commandHistoryAt < len(m.commandHistory)-1 {
		m.commandHistoryAt++
		m.setCommandInputValue(m.commandHistory[m.commandHistoryAt])
		return true
	}
	m.commandHistoryAt = commandHistoryBrowseNone
	m.setCommandInputValue(m.commandDraft)
	m.commandDraft = ""
	return true
}

func (m *dashboardModel) resetCommandHistoryNavigation() {
	m.commandHistoryAt = commandHistoryBrowseNone
	m.commandDraft = ""
}

func (m *dashboardModel) setCommandInputValue(value string) {
	m.commandInput.SetValue(value)
	m.commandInput.SetCursor(len([]rune(value)))
}

func (m *dashboardModel) executeChooserAction(action string) (tea.Model, tea.Cmd) {
	switch {
	case action == "tour":
		if len(m.tourPanes) == 0 {
			m.lastMessage = "No embedded tour content found."
			return m, nil
		}
		m.screen = screenTour
		m.envIndex = 0
		m.syncLayout()
		m.syncTourContent()
		return m, nil
	case action == "config-create":
		return m.beginContextCreation()
	case strings.HasPrefix(action, "create:"):
		display, args, ok := createCommandForChooserAction(action)
		if !ok {
			return m, nil
		}
		m.commandRunning = true
		return m, runSitectlInteractiveCmd(display, args)
	default:
		return m, nil
	}
}

func createCommandForChooserAction(action string) (string, []string, bool) {
	if !strings.HasPrefix(action, "create:") {
		return "", nil, false
	}
	target := strings.TrimSpace(strings.TrimPrefix(action, "create:"))
	pluginName, definitionName, ok := strings.Cut(target, "/")
	if !ok || strings.TrimSpace(pluginName) == "" || strings.TrimSpace(definitionName) == "" || strings.HasPrefix(pluginName, "-") || strings.HasPrefix(definitionName, "-") {
		return "", nil, false
	}
	return "sitectl create " + shellquote.Join(target), []string{"create", target}, true
}

func (m *dashboardModel) handleTourKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.screen = screenDashboard
		m.syncLayout()
		return m, nil
	case key.Matches(msg, m.keys.Left), key.Matches(msg, m.keys.Up):
		if m.envIndex > 0 {
			m.envIndex--
			m.syncTourContent()
		}
		return m, nil
	case key.Matches(msg, m.keys.Right), key.Matches(msg, m.keys.Down):
		if m.envIndex < len(m.tourPanes)-1 {
			m.envIndex++
			m.syncTourContent()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}

func (m *dashboardModel) currentTourIndex() int {
	if len(m.tourPanes) == 0 {
		return 0
	}
	if m.envIndex < 0 {
		return 0
	}
	if m.envIndex >= len(m.tourPanes) {
		return len(m.tourPanes) - 1
	}
	return m.envIndex
}

func (m *dashboardModel) currentTourTitle() string {
	if len(m.tourPanes) == 0 {
		return "-"
	}
	return m.tourPanes[m.currentTourIndex()].Title
}

func (m *dashboardModel) syncTourContent() {
	if len(m.tourPanes) == 0 {
		m.detailBody = "No embedded tour content found."
		m.detail.SetContent(m.detailBody)
		return
	}
	rendered, err := glamour.Render(m.tourPanes[m.currentTourIndex()].Markdown, "dark")
	if err != nil {
		m.detailBody = err.Error()
		m.detail.SetContent(m.detailBody)
		return
	}
	m.detailBody = rendered
	m.detail.SetContent(m.detailBody)
	m.detail.GotoTop()
}

// renderViewportWithScrollbar renders the viewport content with a scrollbar on
// the right side. availWidth is the available panel content width (panel outer
// width minus its horizontal border and padding frame size); the scrollbar
// occupies the last 2 columns (space + character) so content uses availWidth-2.
func renderViewportWithScrollbar(v viewport.Model, raw string, availWidth int) string {
	total := v.TotalLineCount()
	height := v.Height()
	if total <= height || height <= 0 {
		return raw
	}

	allLines := strings.Split(raw, "\n")
	offset := min(max(v.YOffset(), 0), max(len(allLines)-height, 0))
	lines := make([]string, 0, height)
	for i := 0; i < height; i++ {
		idx := offset + i
		if idx >= 0 && idx < len(allLines) {
			lines = append(lines, allLines[idx])
		} else {
			lines = append(lines, "")
		}
	}

	thumbHeight := max(1, (height*height)/max(total, 1))
	maxOffset := max(total-height, 1)
	thumbTop := 0
	if height > thumbHeight {
		thumbTop = (offset * (height - thumbHeight)) / maxOffset
	}

	rows := make([]string, height)
	contentWidth := max(1, availWidth-2)
	for i := 0; i < height; i++ {
		bar := subtleStyle.Render("│")
		if i >= thumbTop && i < thumbTop+thumbHeight {
			bar = accentStyle.Render("█")
		}
		padded := lipgloss.NewStyle().Width(contentWidth).Render(clipLine(lines[i], contentWidth))
		rows[i] = padded + " " + bar
	}
	return strings.Join(rows, "\n")
}

func clipLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width])
}

func reloadStateCmd() tea.Cmd {
	return func() tea.Msg {
		cfg, err := config.Load()
		if err != nil {
			return stateReloadedMsg{Err: err}
		}
		current, err := config.Current()
		if err != nil {
			return stateReloadedMsg{Err: err}
		}
		return stateReloadedMsg{
			Config:         cfg,
			Plugins:        plugin.DiscoverInstalled(),
			CurrentContext: current,
		}
	}
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
	if len(args) >= 2 && args[0] == "docker" && args[1] == "compose" {
		args = append([]string{"compose"}, args[2:]...)
	} else if args[0] == "docker-compose" {
		args = append([]string{"compose"}, args[1:]...)
	}
	if !containsContextArg(args) && strings.TrimSpace(contextName) != "" && contextName != "-" {
		args = append([]string{"--context", contextName}, args...)
	}
	return "sitectl " + shellquote.Join(args...), args, nil
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
	args = stripSitectlGlobalFlags(args)
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "port-forward", "sequelace":
		return true
	case "compose":
		composeArgs := composeSubcommandArgs(args[1:])
		if len(composeArgs) == 0 {
			return false
		}
		switch composeArgs[0] {
		case "attach", "watch":
			return true
		case "exec", "run":
			return composeExecRunNeedsTerminal(composeArgs[1:])
		}
	}
	return false
}

func stripSitectlGlobalFlags(args []string) []string {
	for len(args) > 0 {
		switch {
		case args[0] == "--context" && len(args) > 1:
			args = args[2:]
		case strings.HasPrefix(args[0], "--context="):
			args = args[1:]
		case args[0] == "--log-level" && len(args) > 1:
			args = args[2:]
		case strings.HasPrefix(args[0], "--log-level="):
			args = args[1:]
		default:
			return args
		}
	}
	return args
}

func composeSubcommandArgs(args []string) []string {
	offset := composeSubcommandOffset(args)
	if offset < 0 {
		return nil
	}
	return args[offset:]
}

func composeSubcommandOffset(args []string) int {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 >= len(args) {
				return -1
			}
			return i + 1
		}
		if arg == "--context" || arg == "--log-level" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--context=") || strings.HasPrefix(arg, "--log-level=") {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return i
		}
		if composeFlagTakesValue(arg) && !strings.Contains(arg, "=") {
			i++
		}
	}
	return -1
}

func composeFlagTakesValue(arg string) bool {
	switch arg {
	case "-f", "--file", "--env-file", "-p", "--project-name", "--project-directory", "--profile", "--parallel":
		return true
	}
	return false
}

func hasComposeNoTTYFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-T" || arg == "--no-TTY" || arg == "--no-tty" {
			return true
		}
	}
	return false
}

func hasComposeDetachFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-d" || arg == "--detach" {
			return true
		}
	}
	return false
}

func composeExecRunNeedsTerminal(args []string) bool {
	flags, command := composeExecRunInvocation(args)
	if flags.noTTY || flags.detached {
		return false
	}
	if flags.explicitInteractive {
		return true
	}
	return containerCommandNeedsTerminal(command)
}

type composeExecRunFlags struct {
	noTTY               bool
	detached            bool
	explicitInteractive bool
}

func composeExecRunInvocation(args []string) (composeExecRunFlags, []string) {
	var flags composeExecRunFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if i+1 >= len(args) {
				return flags, nil
			}
			args = args[i+1:]
			i = -1
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if i+1 >= len(args) {
				return flags, nil
			}
			return flags, args[i+1:]
		}

		updateComposeExecRunFlags(&flags, arg)
		if composeExecRunFlagTakesValue(arg) && !strings.Contains(arg, "=") {
			i++
		}
	}
	return flags, nil
}

func updateComposeExecRunFlags(flags *composeExecRunFlags, arg string) {
	switch {
	case arg == "-T" || arg == "--no-TTY" || arg == "--no-tty":
		flags.noTTY = true
	case arg == "-d" || arg == "--detach":
		flags.detached = true
	case arg == "-i" || arg == "--interactive" || arg == "-t" || arg == "--tty":
		flags.explicitInteractive = true
	case strings.HasPrefix(arg, "--interactive="):
		flags.explicitInteractive = !strings.HasSuffix(arg, "=false")
	case strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && !strings.Contains(arg, "="):
		for _, r := range strings.TrimPrefix(arg, "-") {
			switch r {
			case 'T':
				flags.noTTY = true
			case 'd':
				flags.detached = true
			case 'i', 't':
				flags.explicitInteractive = true
			}
		}
	}
}

func composeExecRunFlagTakesValue(arg string) bool {
	switch arg {
	case "-e", "--env", "--env-file", "--index", "-u", "--user", "-w", "--workdir",
		"--entrypoint", "--name", "-p", "--publish", "--pull", "-l", "--label", "-v", "--volume":
		return true
	}
	return false
}

func containerCommandNeedsTerminal(command []string) bool {
	if len(command) == 0 {
		return true
	}
	name := path.Base(command[0])
	switch name {
	case "ash", "bash", "csh", "dash", "fish", "ksh", "sh", "tcsh", "zsh":
		return shellCommandNeedsTerminal(command[1:])
	case "less", "more", "nano", "vi", "vim", "nvim":
		return true
	case "mysql", "mariadb", "psql", "redis-cli", "valkey-cli":
		return !hasNonInteractiveClientQuery(command[1:])
	case "node", "python", "python2", "python3", "ipython", "irb", "php":
		return replCommandNeedsTerminal(name, command[1:])
	case "drush":
		return drushCommandNeedsTerminal(command[1:])
	}
	return false
}

func shellCommandNeedsTerminal(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, arg := range args {
		if arg == "-c" || arg == "--command" {
			return false
		}
		if strings.HasPrefix(arg, "-") {
			flags := strings.TrimLeft(arg, "-")
			if strings.Contains(flags, "i") {
				return true
			}
			if strings.Contains(flags, "c") {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func replCommandNeedsTerminal(name string, args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, arg := range args {
		switch arg {
		case "-i", "--interactive", "-a":
			return true
		case "-c", "-m", "-e", "--eval", "-r":
			return false
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return false
	}
	return name != "php"
}

func drushCommandNeedsTerminal(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "sql:cli", "sql-cli", "php:cli", "php-cli":
		return true
	}
	return false
}

func hasNonInteractiveClientQuery(args []string) bool {
	for _, arg := range args {
		if arg == "-e" || arg == "--execute" || strings.HasPrefix(arg, "-e") || strings.HasPrefix(arg, "--execute=") {
			return true
		}
	}
	return false
}

func streamSafeSitectlArgs(args []string) []string {
	subcommandIndex, ok := composeSubcommandIndex(args)
	if !ok {
		return args
	}
	switch args[subcommandIndex] {
	case "exec", "run":
		if hasComposeNoTTYFlag(args[subcommandIndex+1:]) || hasComposeDetachFlag(args[subcommandIndex+1:]) {
			return args
		}
		streamArgs := make([]string, 0, len(args)+1)
		streamArgs = append(streamArgs, args[:subcommandIndex+1]...)
		streamArgs = append(streamArgs, "-T")
		streamArgs = append(streamArgs, args[subcommandIndex+1:]...)
		return streamArgs
	default:
		return args
	}
}

func composeSubcommandIndex(args []string) (int, bool) {
	commandOffset := sitectlCommandOffset(args)
	if commandOffset < 0 || args[commandOffset] != "compose" {
		return 0, false
	}
	subcommandOffset := composeSubcommandOffset(args[commandOffset+1:])
	if subcommandOffset < 0 {
		return 0, false
	}
	return commandOffset + 1 + subcommandOffset, true
}

func sitectlCommandOffset(args []string) int {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--context" && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--context="):
		case args[i] == "--log-level" && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--log-level="):
		default:
			return i
		}
	}
	return -1
}

func runSitectlStreamCmd(ctx context.Context, id int, display string, args []string) tea.Cmd {
	events := make(chan commandStreamEvent, 32)
	return func() tea.Msg {
		go streamSitectlProcess(ctx, id, display, args, events)
		return commandStreamStartedMsg{ID: id, Command: display, Events: events}
	}
}

func (m *dashboardModel) appendCommandOutput(chunk string) {
	if chunk == "" {
		return
	}
	if !m.commandOutput {
		m.logsBody = ""
		m.commandOutput = true
	}
	m.logsBody = trimCommandOutput(m.logsBody + chunk)
	m.logs.SetContent(m.logsBody)
	m.logs.GotoBottom()
}

func trimCommandOutput(value string) string {
	if len(value) <= maxCommandOutputBytes {
		return value
	}
	tail := value[len(value)-maxCommandOutputBytes:]
	if newline := strings.IndexByte(tail, '\n'); newline >= 0 && newline+1 < len(tail) {
		tail = tail[newline+1:]
	}
	return "[output truncated; showing latest output]\n" + tail
}

func waitForCommandStream(id int, events <-chan commandStreamEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return commandStreamEventMsg{Event: commandStreamEvent{ID: id, Done: true}, Events: events}
		}
		return commandStreamEventMsg{Event: event, Events: events}
	}
}

func streamSitectlProcess(ctx context.Context, id int, display string, args []string, events chan<- commandStreamEvent) {
	defer close(events)

	exe, err := os.Executable()
	if err != nil {
		sendCommandStreamEvent(ctx, events, commandStreamEvent{ID: id, Command: display, Err: err, Done: true})
		return
	}
	cmd := exec.CommandContext(ctx, exe, args...) // #nosec G204 -- sitectl intentionally re-executes itself with internally constructed arguments.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sendCommandStreamEvent(ctx, events, commandStreamEvent{ID: id, Command: display, Err: err, Done: true})
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		sendCommandStreamEvent(ctx, events, commandStreamEvent{ID: id, Command: display, Err: err, Done: true})
		return
	}
	if err := cmd.Start(); err != nil {
		sendCommandStreamEvent(ctx, events, commandStreamEvent{ID: id, Command: display, Err: err, Done: true})
		return
	}

	var readers sync.WaitGroup
	readOutput := func(r io.Reader) {
		defer readers.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 {
				sendCommandStreamEvent(ctx, events, commandStreamEvent{ID: id, Command: display, Output: string(buf[:n])})
			}
			if readErr != nil {
				return
			}
		}
	}
	readers.Add(2)
	go readOutput(stdout)
	go readOutput(stderr)

	waitErr := cmd.Wait()
	readers.Wait()
	if ctx.Err() != nil {
		sendCommandStreamEvent(context.Background(), events, commandStreamEvent{ID: id, Command: display, Done: true, Canceled: true})
		return
	}
	sendCommandStreamEvent(ctx, events, commandStreamEvent{ID: id, Command: display, Err: waitErr, Done: true})
}

func sendCommandStreamEvent(ctx context.Context, events chan<- commandStreamEvent, event commandStreamEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}

func runSitectlInteractiveCmd(display string, args []string) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return commandExecFinishedMsg{Command: display, Err: err} }
	}
	cmd := exec.Command(exe, args...) // #nosec G204 -- sitectl intentionally re-executes itself with internally constructed arguments.
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return commandExecFinishedMsg{Command: display, Err: err}
	})
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

func severityColor(value, yellow, red float64) string {
	switch {
	case value >= red:
		return "#E76F51"
	case value >= yellow:
		return "#E9C46A"
	default:
		return "#2A9D8F"
	}
}

func reverseSeverityColor(value, green, yellow float64) string {
	switch {
	case value <= yellow:
		return "#E76F51"
	case value <= green:
		return "#E9C46A"
	default:
		return "#2A9D8F"
	}
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func truncateMetricText(value string, width int) string {
	value = strings.TrimSpace(value)
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(value)
	if len(runes) > width-1 {
		runes = runes[:width-1]
	}
	return string(runes) + "…"
}

func containerZoneID(name string) string {
	if strings.TrimSpace(name) == "" {
		return "container:-"
	}
	return "container:" + name
}

func containerMemorySummary(service docker.ServiceSummary) string {
	if service.MemoryLimitBytes == 0 {
		return humanBytes(service.MemoryBytes)
	}
	return fmt.Sprintf("%s/%s", humanBytes(service.MemoryBytes), humanBytes(service.MemoryLimitBytes))
}

func renderContainerHeader(summary docker.ProjectSummary, containerName string) string {
	for _, service := range summary.Services {
		if service.Name != containerName {
			continue
		}
		return fmt.Sprintf(
			"Container: %s | CPU %.1f%% | Mem %s | %s",
			helpers.FirstNonEmpty(service.Name, service.Service),
			service.CPUPercent,
			containerMemorySummary(service),
			helpers.FirstNonEmpty(service.Status, service.State),
		)
	}
	return "Container: " + containerName
}

func fetchContainerLogs(ctx config.Context, containerName string) (string, error) {
	args := []string{"logs", "--tail", "20", containerName}
	if ctx.DockerHostType == config.ContextLocal {
		cmd := exec.Command("docker", args...) // #nosec G204 -- docker arguments are assembled by sitectl from selected container state without a shell.
		cmd.Dir = ctx.ProjectDir
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	return ctx.RunQuietCommand(exec.Command("docker", args...)) // #nosec G204 -- docker arguments are assembled by sitectl from selected container state without a shell.
}
