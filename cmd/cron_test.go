package cmd

import (
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestRenderCronSystemdUnits(t *testing.T) {
	spec := config.CronSpec{
		Name:       "nightly",
		Context:    "prod",
		Schedule:   "daily",
		OutputDir:  "/opt/islandora/backups",
		Components: []string{"drupal-db-backup", "fcrepo-db-backup"},
	}

	service, timer, err := renderCronSystemdUnits(spec)
	if err != nil {
		t.Fatalf("renderCronSystemdUnits() error = %v", err)
	}

	if !strings.Contains(service, "cron run nightly") {
		t.Fatalf("service unit missing cron run command: %q", service)
	}
	if !strings.Contains(timer, "Unit=sitectl-nightly.service") {
		t.Fatalf("timer unit missing sitectl unit target: %q", timer)
	}
	if !strings.Contains(timer, "OnCalendar=daily") {
		t.Fatalf("timer unit missing schedule: %q", timer)
	}
}

func TestRenderCronSystemdInstructions(t *testing.T) {
	spec := config.CronSpec{Name: "nightly", Context: "prod", Schedule: "daily"}
	ctx := config.Context{Name: "prod", DockerHostType: config.ContextRemote}

	serviceUnit, timerUnit, err := renderCronSystemdUnits(spec)
	if err != nil {
		t.Fatalf("renderCronSystemdUnits() error = %v", err)
	}

	instructions := renderCronSystemdInstructions(spec, ctx, "sitectl-nightly.service", "sitectl-nightly.timer", serviceUnit, timerUnit)
	wantParts := []string{
		"Remote manual setup is required for now.",
		"https://github.com/libops/sitectl/issues",
		"/etc/systemd/system/sitectl-nightly.service",
		"sudo systemctl enable --now sitectl-nightly.timer",
	}
	for _, part := range wantParts {
		if !strings.Contains(instructions, part) {
			t.Fatalf("renderCronSystemdInstructions() missing %q in %q", part, instructions)
		}
	}
}
