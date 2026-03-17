package plugin

import (
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestGetContextAllowsIncludedPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "isle",
		DockerHostType: config.ContextLocal,
		Environment:    "local",
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "museum",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "drupal"})
	sdk.Config.Context = "museum"

	got, err := sdk.GetContext()
	if err != nil {
		t.Fatalf("GetContext() error = %v", err)
	}
	if got.Plugin != "isle" {
		t.Fatalf("expected context plugin isle, got %q", got.Plugin)
	}
}

func TestGetContextRejectsUnsupportedPlugin(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	ctx := config.Context{
		Name:           "museum",
		Site:           "museum",
		Plugin:         "drupal",
		DockerHostType: config.ContextLocal,
		Environment:    "local",
		DockerSocket:   "/var/run/docker.sock",
		ProjectName:    "museum",
		ProjectDir:     tempHome,
	}
	if err := config.SaveContext(&ctx, true); err != nil {
		t.Fatalf("SaveContext() error = %v", err)
	}

	sdk := NewSDK(Metadata{Name: "isle"})
	sdk.Config.Context = "museum"

	if _, err := sdk.GetContext(); err == nil {
		t.Fatal("expected plugin compatibility error")
	}
}
