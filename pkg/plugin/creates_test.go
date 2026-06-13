package plugin

import (
	"path/filepath"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestEnsureComposeCreateContextUsesDatabaseDefaults(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sdk := NewSDK(Metadata{Name: "archivesspace"})
	ctx, err := sdk.EnsureComposeCreateContext(ComposeCreateRequest{
		TargetType: config.ContextLocal,
		Path:       filepath.Join(tempHome, "archivesspace"),
	}, ComposeCreateContextOptions{
		DefaultName:                   "archivesspace-local",
		DefaultSite:                   "archivesspace",
		DefaultPlugin:                 "archivesspace",
		DefaultProjectName:            "archivesspace",
		DefaultDatabaseService:        "mysql",
		DefaultDatabaseUser:           "as",
		DefaultDatabasePasswordSecret: "ARCHIVESSPACE_DB_PASSWORD",
		DefaultDatabaseName:           "archivesspace",
		Input: func(question ...string) (string, error) {
			t.Fatal("did not expect prompt")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("EnsureComposeCreateContext() error = %v", err)
	}

	if ctx.DatabaseService != "mysql" {
		t.Fatalf("expected database service mysql, got %q", ctx.DatabaseService)
	}
	if ctx.DatabaseUser != "as" {
		t.Fatalf("expected database user as, got %q", ctx.DatabaseUser)
	}
	if ctx.DatabasePasswordSecret != "ARCHIVESSPACE_DB_PASSWORD" {
		t.Fatalf("expected database password secret ARCHIVESSPACE_DB_PASSWORD, got %q", ctx.DatabasePasswordSecret)
	}
	if ctx.DatabaseName != "archivesspace" {
		t.Fatalf("expected database name archivesspace, got %q", ctx.DatabaseName)
	}
}
