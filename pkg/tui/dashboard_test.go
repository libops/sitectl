package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
	"github.com/libops/sitectl/pkg/plugin"
	zone "github.com/lrstanley/bubblezone/v2"
)

func TestGroupContextsBySite(t *testing.T) {
	cfg := &config.Config{
		CurrentContext: "museum-dev",
		Contexts: []config.Context{
			{Name: "museum-prod", Site: "museum", Environment: "prod"},
			{Name: "museum-dev", Site: "museum", Environment: "dev"},
			{Name: "archive-local", Site: "archive", Environment: "local"},
		},
	}

	sites := groupContexts(cfg)
	if len(sites) != 2 {
		t.Fatalf("expected 2 sites, got %d", len(sites))
	}
	if sites[0].Name != "archive" {
		t.Fatalf("expected archive to sort first, got %q", sites[0].Name)
	}
	if sites[1].Contexts[0].Name != "museum-dev" {
		t.Fatalf("expected dev env to sort before prod, got %q", sites[1].Contexts[0].Name)
	}
}

func TestDefaultSelectionUsesCurrentContext(t *testing.T) {
	sites := []siteGroup{
		{Name: "archive", Contexts: []config.Context{{Name: "archive-local"}}},
		{Name: "museum", Contexts: []config.Context{{Name: "museum-dev"}, {Name: "museum-prod"}}},
	}

	siteIndex, envIndex := defaultSelection(sites, "museum-prod")
	if siteIndex != 1 || envIndex != 1 {
		t.Fatalf("expected museum-prod selection at 1,1 got %d,%d", siteIndex, envIndex)
	}
}

func TestSummaryRefreshContextsLoadsActiveRemoteAndBatchesLocals(t *testing.T) {
	sites := []siteGroup{{
		Name: "museum",
		Contexts: []config.Context{
			{Name: "local-a", DockerHostType: config.ContextLocal},
			{Name: "local-b", DockerHostType: config.ContextLocal},
			{Name: "local-c", DockerHostType: config.ContextLocal},
			{Name: "local-d", DockerHostType: config.ContextLocal},
			{Name: "stage", DockerHostType: config.ContextRemote},
			{Name: "prod", DockerHostType: config.ContextRemote},
		},
	}}

	got, cursor := summaryRefreshContexts(sites, sites[0].Contexts[4], 0, 3)
	if names := contextNames(got); strings.Join(names, ",") != "stage,local-a,local-b,local-c" {
		t.Fatalf("summary contexts = %v, want active remote plus bounded local batch", names)
	}
	if cursor != 3 {
		t.Fatalf("next local cursor = %d, want 3", cursor)
	}

	got, cursor = summaryRefreshContexts(sites, sites[0].Contexts[4], cursor, 3)
	if names := contextNames(got); strings.Join(names, ",") != "stage,local-d,local-a,local-b" {
		t.Fatalf("wrapped summary contexts = %v, want next local batch", names)
	}
	if cursor != 2 {
		t.Fatalf("wrapped local cursor = %d, want 2", cursor)
	}
}

func TestSummaryRefreshContextsDoesNotPollInactiveRemotes(t *testing.T) {
	sites := []siteGroup{{
		Name: "museum",
		Contexts: []config.Context{
			{Name: "local", DockerHostType: config.ContextLocal},
			{Name: "stage", DockerHostType: config.ContextRemote},
			{Name: "prod", DockerHostType: config.ContextRemote},
		},
	}}
	selected := sites[0].Contexts[1]

	got, _ := summaryRefreshContexts(sites, selected, 0, localSummaryBatchSize)
	if names := contextNames(got); strings.Join(names, ",") != "stage,local" {
		t.Fatalf("refresh contexts = %v, want active remote and local context only", names)
	}
}

func TestSummaryLoadedCachesInactiveContextWithoutChangingSelection(t *testing.T) {
	m := &dashboardModel{
		sites: []siteGroup{{
			Name: "museum",
			Contexts: []config.Context{
				{Name: "local", DockerHostType: config.ContextLocal},
				{Name: "prod", DockerHostType: config.ContextRemote},
			},
		}},
		envIndex:         1,
		contextSummaries: map[string]contextSummaryState{},
	}
	summary := docker.ProjectSummary{Status: "running", Running: 2, Total: 2, CollectedAt: time.Now()}

	model, _ := m.Update(summaryLoadedMsg{ContextName: "local", Summary: summary})
	got := model.(*dashboardModel)
	if !got.contextSummaries["local"].Loaded {
		t.Fatal("expected inactive local summary to be cached")
	}
	if got.summary.Status != "" {
		t.Fatalf("selected summary changed to inactive context: %+v", got.summary)
	}
	if got.selectedContextName() != "prod" {
		t.Fatalf("selected context changed to %q", got.selectedContextName())
	}
}

func TestUnknownContextCardUsesGrayFuzzyMetrics(t *testing.T) {
	resetGlobalZones(t)
	m := &dashboardModel{
		currentContext:   "local",
		contextSummaries: map[string]contextSummaryState{},
	}
	card := m.renderContextCard(contextPosition{
		SiteName: "museum",
		Context: config.Context{
			Name:           "prod",
			Environment:    "prod",
			DockerHostType: config.ContextRemote,
		},
	}, false, 38)

	for _, want := range []string{"unknown", "░▒", "select this context to load stats"} {
		if !strings.Contains(card, want) {
			t.Fatalf("unknown context card missing %q:\n%s", want, card)
		}
	}
}

func TestContextCardGridIncludesAddContextCard(t *testing.T) {
	resetGlobalZones(t)
	m := &dashboardModel{
		sites: []siteGroup{{
			Name: "museum",
			Contexts: []config.Context{
				{Name: "local"},
				{Name: "prod"},
			},
		}},
		width:            120,
		contextSummaries: map[string]contextSummaryState{},
		contextActions:   map[string]string{},
	}

	got := m.renderContextCards(114)
	for _, want := range []string{"Add a new context", "───┼───", "click or press n", "2 saved"} {
		if !strings.Contains(got, want) {
			t.Fatalf("context grid missing %q:\n%s", want, got)
		}
	}
}

func TestContextCreationPromptsForUniqueNameBeforeSetContext(t *testing.T) {
	m := &dashboardModel{
		sites: []siteGroup{{Name: "museum", Contexts: []config.Context{{Name: "local"}}}},
	}
	m.contextNameInput = textinput.New()
	m.commandInput = textinput.New()

	model, _ := m.beginContextCreation()
	got := model.(*dashboardModel)
	if !got.creatingContext || !got.contextNameInput.Focused() {
		t.Fatal("add context did not focus the context name prompt")
	}
	got.contextNameInput.SetValue("LOCAL")
	model, cmd := got.handleContextNameKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	got = model.(*dashboardModel)
	if cmd != nil {
		t.Fatal("duplicate context name unexpectedly launched context creation")
	}
	if !got.creatingContext || !strings.Contains(got.lastMessage, "already exists") {
		t.Fatalf("duplicate context state = creating %t, message %q", got.creatingContext, got.lastMessage)
	}

	got.contextNameInput.SetValue("stage")
	model, cmd = got.handleContextNameKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	got = model.(*dashboardModel)
	if cmd == nil || got.creatingContext || !got.commandRunning || got.pendingContextSelection != "stage" {
		t.Fatalf("unique context did not hand off to set-context: cmd nil=%t creating=%t running=%t pending=%q", cmd == nil, got.creatingContext, got.commandRunning, got.pendingContextSelection)
	}
}

func TestContextLifecycleActionAndCardButtonFollowLoadedState(t *testing.T) {
	tests := []struct {
		name       string
		state      contextSummaryState
		wantAction string
		wantOK     bool
	}{
		{name: "unknown", state: contextSummaryState{}, wantOK: false},
		{name: "stale", state: contextSummaryState{Loaded: true, Err: errors.New("stale")}, wantOK: false},
		{name: "not created", state: contextSummaryState{Loaded: true, Summary: docker.ProjectSummary{}}, wantAction: "start", wantOK: true},
		{name: "stopped", state: contextSummaryState{Loaded: true, Summary: docker.ProjectSummary{Total: 2, Stopped: 2}}, wantAction: "start", wantOK: true},
		{name: "running", state: contextSummaryState{Loaded: true, Summary: docker.ProjectSummary{Total: 2, Running: 2}}, wantAction: "stop", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, ok := contextLifecycleAction(tt.state)
			if action != tt.wantAction || ok != tt.wantOK {
				t.Fatalf("contextLifecycleAction() = %q, %t; want %q, %t", action, ok, tt.wantAction, tt.wantOK)
			}
		})
	}

	resetGlobalZones(t)
	m := &dashboardModel{
		contextSummaries: map[string]contextSummaryState{
			"local": {Loaded: true, Summary: docker.ProjectSummary{Running: 1, Total: 1}},
		},
		contextActions: map[string]string{},
	}
	card := m.renderContextCard(contextPosition{SiteName: "museum", Context: config.Context{Name: "local"}}, true, 38)
	if !strings.Contains(card, "[stop]") {
		t.Fatalf("running context card missing stop button:\n%s", card)
	}
}

func TestClickingContextBlursCommandInputSoDeleteKeyLaunchesDeletion(t *testing.T) {
	resetGlobalZones(t)
	cfg := &config.Config{
		CurrentContext: "local",
		Contexts: []config.Context{
			{Name: "local", Site: "museum", Environment: "local", DockerHostType: config.ContextLocal},
			{Name: "prod", Site: "museum", Environment: "prod", DockerHostType: config.ContextRemote},
		},
	}
	m := newDashboardModel(cfg, nil)
	m.commandInput.Focus()
	_ = m.View()

	card := waitForZone(t, "context:prod")
	model, _ := m.Update(tea.MouseClickMsg(tea.Mouse{
		X:      card.StartX + 1,
		Y:      card.StartY + 1,
		Button: tea.MouseLeft,
	}))
	got := model.(*dashboardModel)
	if got.selectedContextName() != "prod" {
		t.Fatalf("clicked context = %q, want prod", got.selectedContextName())
	}
	if got.commandInput.Focused() {
		t.Fatal("context click did not move focus out of the command input")
	}

	model, cmd := got.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete}))
	got = model.(*dashboardModel)
	if cmd == nil || !got.commandRunning {
		t.Fatalf("delete key after click did not launch deletion flow: cmd nil=%t running=%t", cmd == nil, got.commandRunning)
	}
}

func TestRightClickContextShowsInformationAndDeleteAction(t *testing.T) {
	resetGlobalZones(t)
	cfg := &config.Config{
		CurrentContext: "local",
		Contexts: []config.Context{
			{Name: "local", Site: "museum", Environment: "local", DockerHostType: config.ContextLocal},
			{
				Name:               "prod",
				Site:               "museum",
				Environment:        "prod",
				Plugin:             "drupal",
				DockerHostType:     config.ContextRemote,
				ProjectDir:         "/srv/museum",
				ComposeProjectName: "museum-prod",
				SSHUser:            "deploy",
				SSHHostname:        "prod.example.org",
				SSHPort:            2222,
			},
		},
	}
	m := newDashboardModel(cfg, nil)
	_ = m.View()
	card := waitForZone(t, "context:prod")

	model, _ := m.Update(tea.MouseClickMsg(tea.Mouse{
		X:      card.StartX + 1,
		Y:      card.StartY + 1,
		Button: tea.MouseRight,
	}))
	got := model.(*dashboardModel)
	if got.selectedContextName() != "prod" || got.contextMenuName != "prod" {
		t.Fatalf("right-click state = selected %q menu %q; want prod", got.selectedContextName(), got.contextMenuName)
	}

	view := got.View()
	for _, want := range []string{"Context information", "deploy@prod.example.org:2222", "/srv/museum", "museum-prod", "[Delete context]"} {
		if !strings.Contains(view.Content, want) {
			t.Fatalf("context menu missing %q:\n%s", want, view.Content)
		}
	}
	deleteButton := waitForZone(t, "context-menu:delete")
	model, cmd := got.Update(tea.MouseClickMsg(tea.Mouse{
		X:      deleteButton.StartX + 1,
		Y:      deleteButton.StartY,
		Button: tea.MouseLeft,
	}))
	got = model.(*dashboardModel)
	if cmd == nil || !got.commandRunning {
		t.Fatalf("context menu delete did not launch deletion flow: cmd nil=%t running=%t", cmd == nil, got.commandRunning)
	}
}

func TestEscapeClosesContextInformation(t *testing.T) {
	m := &dashboardModel{
		sites:           []siteGroup{{Name: "museum", Contexts: []config.Context{{Name: "local"}}}},
		keys:            defaultKeyMap(),
		contextMenuName: "local",
	}
	m.commandInput = textinput.New()
	m.contextNameInput = textinput.New()

	model, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	got := model.(*dashboardModel)
	if cmd != nil || got.contextMenuName != "" {
		t.Fatalf("escape did not close context information: cmd nil=%t menu=%q", cmd == nil, got.contextMenuName)
	}
}

func TestDirectoryContainsDetectsDashboardInsideDeletedProject(t *testing.T) {
	tests := []struct {
		parent string
		child  string
		want   bool
	}{
		{parent: "/srv/museum", child: "/srv/museum", want: true},
		{parent: "/srv/museum", child: "/srv/museum/web", want: true},
		{parent: "/srv/museum", child: "/srv/archive", want: false},
	}
	for _, tt := range tests {
		if got := directoryContains(tt.parent, tt.child); got != tt.want {
			t.Fatalf("directoryContains(%q, %q) = %t, want %t", tt.parent, tt.child, got, tt.want)
		}
	}
}

func TestDeleteFlowDefersLeavingProjectDirectoryUntilDeletionSucceeds(t *testing.T) {
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(t.TempDir(), "museum")
	workingDir := filepath.Join(projectDir, "web")
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(originalDir); chdirErr != nil {
			t.Fatalf("restore test working directory: %v", chdirErr)
		}
	})

	m := &dashboardModel{
		sites: []siteGroup{{Name: "museum", Contexts: []config.Context{{
			Name:           "local",
			DockerHostType: config.ContextLocal,
			ProjectDir:     projectDir,
		}}}},
	}
	model, cmd := m.deleteSelectedContext()
	got := model.(*dashboardModel)
	if cmd == nil || got.pendingWorkingDir != filepath.Dir(projectDir) {
		t.Fatalf("delete handoff = cmd nil %t, pending dir %q; want parent %q", cmd == nil, got.pendingWorkingDir, filepath.Dir(projectDir))
	}
	currentDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if currentDir != workingDir {
		t.Fatalf("working directory changed before deletion confirmation: got %q want %q", currentDir, workingDir)
	}
}

func TestContextSelectionPagesThroughCardGrid(t *testing.T) {
	contexts := make([]config.Context, 0, 7)
	for i := 0; i < 7; i++ {
		contexts = append(contexts, config.Context{Name: string(rune('a' + i)), DockerHostType: config.ContextLocal})
	}
	m := &dashboardModel{
		sites:            []siteGroup{{Name: "museum", Contexts: contexts}},
		width:            120,
		contextSummaries: map[string]contextSummaryState{},
	}

	if got := m.contextsPerPage(); got != 3 {
		t.Fatalf("contexts per page = %d, want 3", got)
	}
	if !m.moveContextSelection(3) {
		t.Fatal("expected selection to move to the next row")
	}
	if m.envIndex != 3 || m.contextPage != 1 {
		t.Fatalf("selection = env %d page %d, want env 3 page 1", m.envIndex, m.contextPage)
	}
	m.changeContextPage(10)
	if m.contextPage != 2 {
		t.Fatalf("last context page = %d, want 2", m.contextPage)
	}
}

func TestContextCardPagerOnlyRendersCurrentPage(t *testing.T) {
	resetGlobalZones(t)
	contexts := []config.Context{
		{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "four"},
	}
	m := &dashboardModel{
		sites:            []siteGroup{{Name: "museum", Contexts: contexts}},
		width:            120,
		contextPage:      1,
		contextSummaries: map[string]contextSummaryState{},
	}

	got := m.renderContextCards(114)
	if !strings.Contains(got, "four") {
		t.Fatalf("second page did not include fourth context:\n%s", got)
	}
	for _, hidden := range []string{"one", "two", "three"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("second page unexpectedly included %q:\n%s", hidden, got)
		}
	}
}

func TestDashboardFitsMinimumTerminal(t *testing.T) {
	resetGlobalZones(t)
	cfg := &config.Config{
		CurrentContext: "local",
		Contexts: []config.Context{
			{Name: "local", Site: "museum", Environment: "local", DockerHostType: config.ContextLocal},
			{Name: "stage", Site: "museum", Environment: "stage", DockerHostType: config.ContextRemote},
			{Name: "prod", Site: "museum", Environment: "prod", DockerHostType: config.ContextRemote},
		},
	}
	m := newDashboardModel(cfg, nil)
	m.currentContext = "local"
	m.siteIndex, m.envIndex = defaultSelection(m.sites, "local")
	m.width = 100
	m.height = 28
	m.syncLayout()

	for _, name := range []string{"dashboard", "context information"} {
		t.Run(name, func(t *testing.T) {
			m.contextMenuName = ""
			if name == "context information" {
				m.contextMenuName = "local"
			}
			rendered := m.render()
			if got := lipgloss.Width(rendered); got > m.width {
				t.Fatalf("minimum %s width = %d, terminal width = %d", name, got, m.width)
			}
			if got := lipgloss.Height(rendered); got > m.height {
				t.Fatalf("minimum %s height = %d, terminal height = %d", name, got, m.height)
			}
		})
	}
}

func contextNames(contexts []config.Context) []string {
	names := make([]string, 0, len(contexts))
	for _, ctx := range contexts {
		names = append(names, ctx.Name)
	}
	return names
}

func resetGlobalZones(t *testing.T) {
	t.Helper()
	if zone.DefaultManager != nil {
		zone.Close()
	}
	zone.DefaultManager = zone.New()
	t.Cleanup(func() {
		zone.Close()
		zone.DefaultManager = nil
	})
}

func waitForZone(t *testing.T, id string) *zone.ZoneInfo {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info := zone.Get(id); info != nil {
			return info
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zone %q was not rendered", id)
	return nil
}

func TestChooserItemsForEmptyStateIncludesSetupAndCreatePlugins(t *testing.T) {
	items := chooserItems(nil, []plugin.InstalledPlugin{
		{Name: "drupal"},
		{Name: "wp", CanCreate: true, CreateDefinitions: []plugin.CreateSpec{{
			Name:              "default",
			Default:           true,
			Description:       "Create a WordPress stack.",
			DockerComposeRepo: "https://example.com/wp",
		}}},
	})

	if len(items) != 3 {
		t.Fatalf("expected tour option, setup option, plus one create plugin, got %d items", len(items))
	}
	if items[0].action != "tour" {
		t.Fatalf("expected first item to launch tour, got %q", items[0].action)
	}
	if items[1].action != "config-create" {
		t.Fatalf("expected second item to launch config create, got %q", items[1].action)
	}
	if items[2].action != "create:wp/default" {
		t.Fatalf("expected third item to launch top-level wp/default create, got %q", items[2].action)
	}
	if !strings.Contains(items[2].title, "wp") || items[2].desc != "Create a WordPress stack." {
		t.Fatalf("unexpected create menu item: %+v", items[2])
	}
}

func TestOnboardingChoicesSupportWheelAndClickLaunch(t *testing.T) {
	resetGlobalZones(t)
	m := newDashboardModel(&config.Config{}, []plugin.InstalledPlugin{{
		Name:      "wp",
		CanCreate: true,
		CreateDefinitions: []plugin.CreateSpec{{
			Name:        "default",
			Default:     true,
			Description: "Create a WordPress stack.",
		}},
	}})
	m.width = 120
	m.height = 36
	m.syncLayout()
	_ = m.View()

	listZone := waitForZone(t, onboardingListZoneID)
	updated, _ := m.Update(tea.MouseWheelMsg(tea.Mouse{
		X:      listZone.StartX + 1,
		Y:      listZone.StartY + 1,
		Button: tea.MouseWheelDown,
	}))
	m = updated.(*dashboardModel)
	if got := m.chooser.Index(); got != 1 {
		t.Fatalf("wheel-selected onboarding index = %d, want 1", got)
	}

	_ = m.View()
	wpZone := waitForZone(t, onboardingItemZoneID(2))
	updated, cmd := m.Update(tea.MouseClickMsg(tea.Mouse{
		X:      wpZone.StartX + 1,
		Y:      wpZone.StartY,
		Button: tea.MouseLeft,
	}))
	m = updated.(*dashboardModel)
	if got := m.chooser.Index(); got != 2 {
		t.Fatalf("clicked onboarding index = %d, want 2", got)
	}
	if !m.commandRunning || cmd == nil {
		t.Fatal("clicking WordPress did not launch its create workflow")
	}
}

func TestCreateChooserActionUsesTopLevelCreateCommand(t *testing.T) {
	display, args, ok := createCommandForChooserAction("create:wp/default")
	if !ok {
		t.Fatal("valid create chooser action was rejected")
	}
	if display != "sitectl create wp/default" {
		t.Fatalf("create display = %q, want top-level create command", display)
	}
	want := []string{"create", "wp/default"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("create args = %#v, want %#v", args, want)
	}

	for _, invalid := range []string{"plugin:wp", "create:", "create:wp", "create:-bad/default"} {
		if _, _, valid := createCommandForChooserAction(invalid); valid {
			t.Fatalf("invalid create chooser action %q was accepted", invalid)
		}
	}
}

func TestNormalizeSitectlCommandMapsDockerCompose(t *testing.T) {
	display, args, err := normalizeSitectlCommand("docker compose logs -f drupal", "stage")
	if err != nil {
		t.Fatalf("normalizeSitectlCommand() error = %v", err)
	}
	if display != "sitectl --context stage compose logs -f drupal" {
		t.Fatalf("unexpected display %q", display)
	}
	want := []string{"--context", "stage", "compose", "logs", "-f", "drupal"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected args %#v", args)
	}
}

func TestIsInteractiveArgsClassifiesComposeCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "follow logs streams in dashboard",
			args: []string{"--context", "stage", "compose", "logs", "-f", "drupal"},
			want: false,
		},
		{
			name: "exec with tty runs in terminal",
			args: []string{"--context", "stage", "compose", "exec", "-it", "drupal", "bash"},
			want: true,
		},
		{
			name: "exec shell without tty flag runs in terminal",
			args: []string{"compose", "exec", "drupal", "bash"},
			want: true,
		},
		{
			name: "exec shell command streams in dashboard",
			args: []string{"compose", "exec", "drupal", "sh", "-lc", "drush uli"},
			want: false,
		},
		{
			name: "exec drush uli streams in dashboard",
			args: []string{"compose", "exec", "drupal", "drush", "uli"},
			want: false,
		},
		{
			name: "exec without tty streams in dashboard",
			args: []string{"compose", "exec", "-T", "drupal", "drush", "status"},
			want: false,
		},
		{
			name: "drush sql cli runs in terminal",
			args: []string{"compose", "exec", "drupal", "drush", "sql:cli"},
			want: true,
		},
		{
			name: "context flag inside compose args",
			args: []string{"compose", "--context", "stage", "logs", "-f"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInteractiveArgs(tt.args); got != tt.want {
				t.Fatalf("isInteractiveArgs(%#v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestStreamSafeSitectlArgsAddsNoTTYForComposeExecRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "exec gets no tty flag",
			args: []string{"--context", "stage", "compose", "exec", "drupal", "drush", "uli"},
			want: []string{"--context", "stage", "compose", "exec", "-T", "drupal", "drush", "uli"},
		},
		{
			name: "run gets no tty flag",
			args: []string{"compose", "run", "--rm", "drupal", "drush", "status"},
			want: []string{"compose", "run", "-T", "--rm", "drupal", "drush", "status"},
		},
		{
			name: "existing no tty flag is preserved",
			args: []string{"compose", "exec", "-T", "drupal", "drush", "uli"},
			want: []string{"compose", "exec", "-T", "drupal", "drush", "uli"},
		},
		{
			name: "compose flags stay before subcommand",
			args: []string{"--context", "stage", "compose", "-f", "compose.yml", "exec", "drupal", "drush", "uli"},
			want: []string{"--context", "stage", "compose", "-f", "compose.yml", "exec", "-T", "drupal", "drush", "uli"},
		},
		{
			name: "non compose command is unchanged",
			args: []string{"config", "current-context"},
			want: []string{"config", "current-context"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := streamSafeSitectlArgs(tt.args)
			if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("streamSafeSitectlArgs(%#v) = %#v, want %#v", tt.args, got, tt.want)
			}
		})
	}
}

func TestStateReloadPreservesCommandOutputPane(t *testing.T) {
	cfg := &config.Config{
		CurrentContext: "stage",
		Contexts: []config.Context{{
			Name:        "stage",
			Site:        "site",
			Environment: "stage",
		}},
	}
	m := newDashboardModel(cfg, nil)
	m.screen = screenLogs
	m.logsTitle = "Command Output"
	m.logsBody = "https://example.test/user/reset"
	m.logs.SetContent(m.logsBody)
	m.logTarget = ""

	model, _ := m.Update(stateReloadedMsg{
		Config:         cfg,
		CurrentContext: "stage",
	})
	got := model.(*dashboardModel)
	if got.screen != screenLogs {
		t.Fatalf("expected command output pane to stay open, got screen %v", got.screen)
	}
	if got.logsTitle != "Command Output" {
		t.Fatalf("expected command output title to be preserved, got %q", got.logsTitle)
	}
	if got.logsBody != "https://example.test/user/reset" {
		t.Fatalf("expected command output body to be preserved, got %q", got.logsBody)
	}
}

func TestStateReloadSelectsNewlyCreatedContext(t *testing.T) {
	cfg := &config.Config{
		CurrentContext: "local",
		Contexts: []config.Context{
			{Name: "local", Site: "museum", Environment: "local", DockerHostType: config.ContextLocal},
			{Name: "stage", Site: "museum", Environment: "stage", DockerHostType: config.ContextRemote},
		},
	}
	m := newDashboardModel(cfg, nil)
	m.pendingContextSelection = "stage"

	model, _ := m.Update(stateReloadedMsg{Config: cfg, CurrentContext: "local"})
	got := model.(*dashboardModel)
	if got.selectedContextName() != "stage" {
		t.Fatalf("selected context after creation = %q, want stage", got.selectedContextName())
	}
	if got.currentContext != "local" {
		t.Fatalf("persisted current context marker changed to %q", got.currentContext)
	}
	if got.pendingContextSelection != "" {
		t.Fatalf("pending context selection was not cleared: %q", got.pendingContextSelection)
	}
}

func TestCommandHistoryNavigation(t *testing.T) {
	m := &dashboardModel{commandHistoryAt: commandHistoryBrowseNone}
	m.commandInput = textinput.New()
	m.addCommandHistory("compose ps")
	m.addCommandHistory("compose exec drupal drush uli")
	m.addCommandHistory("compose exec drupal drush uli")
	if len(m.commandHistory) != 2 {
		t.Fatalf("expected consecutive duplicate command to be skipped, got %#v", m.commandHistory)
	}

	m.commandInput.SetValue("draft")
	if !m.previousCommandHistory() {
		t.Fatal("expected previous history entry")
	}
	if got := m.commandInput.Value(); got != "compose exec drupal drush uli" {
		t.Fatalf("first previous command = %q", got)
	}
	if !m.previousCommandHistory() {
		t.Fatal("expected older history entry")
	}
	if got := m.commandInput.Value(); got != "compose ps" {
		t.Fatalf("second previous command = %q", got)
	}
	if !m.nextCommandHistory() {
		t.Fatal("expected newer history entry")
	}
	if got := m.commandInput.Value(); got != "compose exec drupal drush uli" {
		t.Fatalf("next command = %q", got)
	}
	if !m.nextCommandHistory() {
		t.Fatal("expected draft restoration")
	}
	if got := m.commandInput.Value(); got != "draft" {
		t.Fatalf("restored draft = %q", got)
	}
	if m.nextCommandHistory() {
		t.Fatal("expected no next entry after restoring draft")
	}
}

func TestTrimCommandOutputKeepsLatestOutput(t *testing.T) {
	value := strings.Repeat("a", maxCommandOutputBytes) + "\nlatest"
	got := trimCommandOutput(value)
	if !strings.HasPrefix(got, "[output truncated; showing latest output]\n") {
		t.Fatalf("expected truncation notice, got %q", got[:min(len(got), 40)])
	}
	if !strings.HasSuffix(got, "latest") {
		t.Fatalf("expected latest output to be retained")
	}
}
