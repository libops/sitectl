package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

func TestTextPromptUsesDashboardFrameForContextCreation(t *testing.T) {
	model := newTextPromptModel(TextPromptOptions{
		Sections: []string{"Choose the project directory."},
		Prompt:   "project dir:",
	})
	model.width = 80
	model.height = 24
	model.syncLayout()

	view := model.View()
	if !view.AltScreen {
		t.Fatal("context setup prompt did not stay in the alternate-screen TUI frame")
	}
	for _, want := range []string{"Sitectl | Interactive", "Choose the project directory.", "Guided workflow"} {
		if !strings.Contains(view.Content, want) {
			t.Fatalf("framed text prompt missing %q:\n%s", want, view.Content)
		}
	}
	if got := lipgloss.Width(view.Content); got > model.width {
		t.Fatalf("framed text prompt width = %d, terminal width = %d", got, model.width)
	}
	if got := lipgloss.Height(view.Content); got > model.height {
		t.Fatalf("framed text prompt height = %d, terminal height = %d", got, model.height)
	}
}

func TestChoicePromptUsesDashboardSelectionColorsForContextCreation(t *testing.T) {
	model := newChoicePromptModel(ChoicePromptOptions{
		Name: "target machine",
		Choices: []Choice{
			{Value: "local", Label: "local", Help: "Use Docker on this machine."},
			{Value: "remote", Label: "remote", Help: "Connect over SSH."},
			{Value: "custom", Label: "custom", Help: "Enter another target.", AllowCustomInput: true},
		},
		DefaultValue: "local",
	})
	model.width = 80
	model.height = 24
	model.syncLayout()

	view := model.View()
	if !view.AltScreen || !strings.Contains(view.Content, "Sitectl | Interactive") {
		t.Fatalf("choice prompt did not use context setup frame:\n%s", view.Content)
	}
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("choice prompt mouse mode = %v, want cell motion", view.MouseMode)
	}
	if !strings.Contains(view.Content, "local") || !strings.Contains(view.Content, "Use Docker on this machine.") {
		t.Fatalf("choice prompt content was lost in the context setup frame:\n%s", view.Content)
	}
	if got := lipgloss.Height(view.Content); got > model.height {
		t.Fatalf("framed choice prompt height = %d, terminal height = %d", got, model.height)
	}

	model.list.Select(2)
	model.syncInputFocus()
	model.syncLayout()
	if got := lipgloss.Height(model.View().Content); got > model.height {
		t.Fatalf("framed custom choice prompt height = %d, terminal height = %d", got, model.height)
	}
}

func TestChoicePromptSupportsClickSelectionAndWheelNavigation(t *testing.T) {
	resetPromptZones(t)
	opts := ChoicePromptOptions{
		Name: "application",
		Choices: []Choice{
			{Value: "drupal", Label: "Drupal"},
			{Value: "wp", Label: "WordPress"},
			{Value: "isle", Label: "ISLE"},
		},
		DefaultValue: "drupal",
	}
	model := newChoicePromptModel(opts)
	model.width = 80
	model.height = 24
	model.syncLayout()
	_ = model.View()

	wpZone := waitForPromptZone(t, choiceItemZoneID(1))
	updated, cmd := model.Update(tea.MouseClickMsg(tea.Mouse{
		X:      wpZone.StartX + 1,
		Y:      wpZone.StartY,
		Button: tea.MouseLeft,
	}))
	model = updated.(*choicePromptModel)
	if got := model.list.Index(); got != 1 {
		t.Fatalf("clicked choice index = %d, want 1", got)
	}
	if model.value != "wp" || cmd == nil {
		t.Fatalf("clicked choice value = %q, cmd nil = %t; want wp and quit command", model.value, cmd == nil)
	}

	model = newChoicePromptModel(opts)
	model.width = 80
	model.height = 24
	model.syncLayout()
	_ = model.View()
	listZone := waitForPromptZone(t, choiceListZoneID)
	updated, _ = model.Update(tea.MouseWheelMsg(tea.Mouse{
		X:      listZone.StartX + 1,
		Y:      listZone.StartY + 1,
		Button: tea.MouseWheelDown,
	}))
	model = updated.(*choicePromptModel)
	if got := model.list.Index(); got != 1 {
		t.Fatalf("wheel-selected choice index = %d, want 1", got)
	}
}

func TestPromptAlwaysUsesSitectlFrame(t *testing.T) {
	model := newTextPromptModel(TextPromptOptions{Prompt: "value:"})
	view := model.View()
	if !view.AltScreen {
		t.Fatal("interactive prompt did not enable the sitectl alternate-screen frame")
	}
	if !strings.Contains(view.Content, "Sitectl | Interactive") {
		t.Fatal("interactive prompt did not use sitectl framing")
	}
}

func TestPromptOutputCanMoveToStderrForRPC(t *testing.T) {
	t.Setenv(PromptOutputEnvironment, "")
	if got := promptOutputFile(); got != os.Stdout {
		t.Fatalf("default prompt output = %v, want stdout", got)
	}

	t.Setenv(PromptOutputEnvironment, PromptOutputStderr)
	if got := promptOutputFile(); got != os.Stderr {
		t.Fatalf("RPC prompt output = %v, want stderr", got)
	}
}

func resetPromptZones(t *testing.T) {
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

func waitForPromptZone(t *testing.T, id string) *zone.ZoneInfo {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info := zone.Get(id); info != nil {
			return info
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("prompt zone %q was not rendered", id)
	return nil
}
