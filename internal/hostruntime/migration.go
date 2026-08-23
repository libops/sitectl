package hostruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type legacyUnit struct {
	name       string
	signatures []string
}

var legacyUnits = []legacyUnit{
	{name: "cron.service", signatures: []string{"Description=cron", "ExecStart=/bin/bash /home/cloud-compose/cron.sh"}},
	{name: "cron.timer", signatures: []string{"Description=cron", "OnBootSec=10m", "OnUnitInactiveSec=24h", "WakeSystem=true"}},
	{name: "vault-agent.service", signatures: []string{"ConditionPathExists=/etc/vault-agent.d/cloud-compose.hcl", "ExecStart=/usr/local/bin/vault agent -config=/etc/vault-agent.d/cloud-compose.hcl"}},
	{name: "internal-services.service", signatures: []string{"Description=Internal Services (Ping, Metrics, Power Management)", "WorkingDirectory=/mnt/disks/data/libops-internal"}},
	{name: "internal-services.timer", signatures: []string{"Description=Delay Internal Services until 20m after initial boot", "OnBootSec=20min", "Unit=internal-services.service"}},
}

// MigrateLegacyUnits removes only exact generic units previously shipped by Cloud Compose.
func MigrateLegacyUnits(ctx context.Context, directory string, stdout, stderr io.Writer) error {
	if directory == "" {
		directory = "/etc/systemd/system"
	}
	if !safeAbsolutePath(directory) {
		return fmt.Errorf("unsafe systemd unit directory: %s", directory)
	}
	for _, unit := range legacyUnits {
		path := filepath.Join(directory, unit.name)
		contents, err := readSingleLinkFile(path, 1<<20)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		matches := true
		for _, signature := range unit.signatures {
			matches = matches && strings.Contains(string(contents), signature)
		}
		if !matches {
			continue
		}
		command := exec.CommandContext(ctx, "systemctl", "disable", "--now", unit.name)
		command.Stdout, command.Stderr = stdout, stderr
		_ = command.Run()
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
