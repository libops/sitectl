package tui

import (
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
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

func TestChooserItemsForEmptyStateIncludesSetupAndCreatePlugins(t *testing.T) {
	items := chooserItems(nil, []plugin.InstalledPlugin{
		{Name: "drupal"},
		{Name: "isle", CanCreate: true, TemplateRepo: "https://example.com/isle"},
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
	if items[2].action != "plugin:isle" {
		t.Fatalf("expected third item to launch isle create, got %q", items[2].action)
	}
}
