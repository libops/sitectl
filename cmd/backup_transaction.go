package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	containerderrdefs "github.com/containerd/errdefs"
	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/libops/sitectl/pkg/config"
	coredocker "github.com/libops/sitectl/pkg/docker"
	"github.com/spf13/cobra"
)

const (
	siteRestoreComposeRecoveryTimeout = 20 * time.Minute
	siteRestoreVolumeRecoveryTimeout  = 2 * time.Hour
	siteRestoreCleanupTimeout         = 30 * time.Minute
	siteBackupHelperCleanupTimeout    = 2 * time.Minute
)

type siteBackupOperations interface {
	runningServices(context.Context) ([]string, error)
	compose(context.Context, ...string) error
	backupDatabase(context.Context, string, string, string) error
	restoreDatabase(context.Context, string, string, string, string) error
	backupVolume(context.Context, string, string, string, string) error
	validateArtifact(context.Context, string, string, string, bool) error
	volumeExists(context.Context, string) (bool, error)
	validateOwnedVolume(context.Context, string, string) error
	createOwnedVolume(context.Context, string, string) error
	ensureVolumeAttachments(context.Context, string, string) error
	createTemporaryVolume(context.Context, string) error
	removeVolume(context.Context, string) error
	stageArtifact(context.Context, string, string, string, string, string, bool) error
	snapshotVolume(context.Context, string, string, string) error
	clearVolume(context.Context, string, string) error
	extractStagedVolume(context.Context, string, string, string, string) error
	restoreSnapshot(context.Context, string, string, string) error
	waitForDatabase(context.Context, string) error
	prepareRestoreWorkspace(context.Context, string, []string, string) (string, error)
	removeWorkDirectory(context.Context, string) error
}

type siteBackupRuntime struct {
	cmd *cobra.Command
	ctx *config.Context
}

func newSiteBackupRuntime(cmd *cobra.Command, ctx *config.Context) *siteBackupRuntime {
	return &siteBackupRuntime{cmd: cmd, ctx: ctx}
}

func withQuiescedSiteWriters(runCtx context.Context, operations siteBackupOperations, databaseService string, backup func() error) (resultErr error) {
	running, err := operations.runningServices(runCtx)
	if err != nil {
		return fmt.Errorf("inspect running services before backup: %w", err)
	}
	if !containsString(running, databaseService) {
		return fmt.Errorf("MariaDB service %q must be running before a coherent site backup", databaseService)
	}
	writers := stringsExcept(running, databaseService)
	if len(writers) > 0 {
		defer func() {
			resumeCtx, cancel := context.WithTimeout(context.Background(), siteRestoreComposeRecoveryTimeout)
			defer cancel()
			if resumeErr := operations.compose(resumeCtx, append([]string{"start"}, writers...)...); resumeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("resume application services after backup: %w", resumeErr))
			}
		}()
		if err := operations.compose(runCtx, append([]string{"stop", "--timeout", "120"}, writers...)...); err != nil {
			return fmt.Errorf("quiesce application services before backup: %w", err)
		}
	}
	return backup()
}

func executeSiteRestore(cmd *cobra.Command, operations siteBackupOperations, plan siteRestorePlan) error {
	runCtx := cmd.Context()
	temporaryVolumes := []string{}
	recoveryVolumes := []string{}
	archives := []string{plan.database.archive}
	for _, volume := range plan.volumes {
		archives = append(archives, volume.archive)
	}
	workDir, err := operations.prepareRestoreWorkspace(runCtx, plan.backupDir, archives, plan.identifier)
	if err != nil {
		return finishRestoreBeforeOutage(operations, temporaryVolumes, workDir, fmt.Errorf("freeze restore artifacts in private workspace: %w", err))
	}
	for _, volume := range plan.volumes {
		if err := operations.validateArtifact(runCtx, workDir, volume.archive, volume.checksum, true); err != nil {
			return finishRestoreBeforeOutage(operations, temporaryVolumes, workDir, fmt.Errorf("validate backup for volume %s before staging: %w", volume.logical, err))
		}
	}
	if err := operations.validateArtifact(runCtx, workDir, plan.database.archive, plan.database.checksum, false); err != nil {
		return finishRestoreBeforeOutage(operations, temporaryVolumes, workDir, fmt.Errorf("validate MariaDB backup before outage: %w", err))
	}

	stage := func(sourceDir, volume, archive, checksum string, database bool) error {
		if err := operations.createTemporaryVolume(runCtx, volume); err != nil {
			return err
		}
		temporaryVolumes = append(temporaryVolumes, volume)
		if err := operations.stageArtifact(runCtx, plan.image, sourceDir, archive, checksum, volume, database); err != nil {
			return err
		}
		return nil
	}
	for _, volume := range plan.volumes {
		if err := stage(workDir, volume.stageVolume, volume.archive, volume.checksum, false); err != nil {
			return finishRestoreBeforeOutage(operations, temporaryVolumes, workDir, fmt.Errorf("stage backup for volume %s: %w", volume.logical, err))
		}
	}

	if err := operations.compose(runCtx, "down", "--remove-orphans", "--timeout", "120"); err != nil {
		restartCtx, cancel := context.WithTimeout(context.Background(), siteRestoreComposeRecoveryTimeout)
		defer cancel()
		restartErr := operations.compose(restartCtx, "up", "--remove-orphans", "--wait", "--wait-timeout", "600", "-d")
		if restartErr != nil {
			return retainedRestoreError(errors.Join(fmt.Errorf("stop site for transactional restore: %w", err), fmt.Errorf("restart site after failed stop: %w", restartErr)), recoveryVolumes, temporaryVolumes, workDir)
		}
		return finishRestoreBeforeOutage(operations, temporaryVolumes, workDir, fmt.Errorf("stop site for transactional restore: %w", err))
	}
	for _, volume := range plan.volumes {
		if err := operations.ensureVolumeAttachments(runCtx, volume.actual, ""); err != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("verify volume %s is detached before restore: %w", volume.logical, err))
		}
	}
	if err := operations.ensureVolumeAttachments(runCtx, plan.database.actualVolume, ""); err != nil {
		return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("verify MariaDB volume is detached before restore: %w", err))
	}

	for index := range plan.volumes {
		volume := &plan.volumes[index]
		exists, inspectErr := operations.volumeExists(runCtx, volume.actual)
		if inspectErr != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("inspect current volume %s after stopping site: %w", volume.logical, inspectErr))
		}
		volume.originalExists = exists
		if !exists {
			continue
		}
		if validateErr := operations.validateOwnedVolume(runCtx, volume.logical, volume.actual); validateErr != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("validate current volume %s ownership: %w", volume.logical, validateErr))
		}
		if createErr := operations.createTemporaryVolume(runCtx, volume.recoveryVolume); createErr != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("create recovery volume for %s: %w", volume.logical, createErr))
		}
		recoveryVolumes = append(recoveryVolumes, volume.recoveryVolume)
		if snapshotErr := operations.snapshotVolume(runCtx, plan.image, volume.actual, volume.recoveryVolume); snapshotErr != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("snapshot current volume %s: %w", volume.logical, snapshotErr))
		}
	}

	databaseExists, err := operations.volumeExists(runCtx, plan.database.actualVolume)
	if err != nil {
		return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("inspect current MariaDB data volume: %w", err))
	}
	plan.database.originalExists = databaseExists
	if databaseExists {
		if err := operations.validateOwnedVolume(runCtx, plan.database.logicalVolume, plan.database.actualVolume); err != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("validate current MariaDB volume ownership: %w", err))
		}
		if err := operations.createTemporaryVolume(runCtx, plan.database.recoveryVolume); err != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("create MariaDB recovery volume: %w", err))
		}
		recoveryVolumes = append(recoveryVolumes, plan.database.recoveryVolume)
		if err := operations.snapshotVolume(runCtx, plan.image, plan.database.actualVolume, plan.database.recoveryVolume); err != nil {
			return restartSiteBeforeRestoreCommit(operations, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("snapshot current MariaDB data volume: %w", err))
		}
	}

	for _, volume := range plan.volumes {
		if !volume.originalExists {
			if err := operations.createOwnedVolume(runCtx, volume.logical, volume.actual); err != nil {
				return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("create missing owned volume %s during restore commit: %w", volume.logical, err))
			}
		}
		if err := operations.clearVolume(runCtx, plan.image, volume.actual); err != nil {
			return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("clear volume %s during restore commit: %w", volume.logical, err))
		}
		if err := operations.extractStagedVolume(runCtx, plan.image, volume.stageVolume, volume.archive, volume.actual); err != nil {
			return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("populate volume %s during restore commit: %w", volume.logical, err))
		}
	}
	if !plan.database.originalExists {
		if err := operations.createOwnedVolume(runCtx, plan.database.logicalVolume, plan.database.actualVolume); err != nil {
			return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("create missing MariaDB data volume during restore commit: %w", err))
		}
	}
	if err := operations.clearVolume(runCtx, plan.image, plan.database.actualVolume); err != nil {
		return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("clear MariaDB data volume during restore commit: %w", err))
	}
	if err := operations.waitForDatabase(runCtx, plan.database.service); err != nil {
		return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, err)
	}
	if err := operations.validateArtifact(runCtx, workDir, plan.database.archive, plan.database.checksum, false); err != nil {
		return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("revalidate frozen MariaDB backup before import: %w", err))
	}
	databaseInput := filepath.Join(workDir, plan.database.archive)
	if err := operations.restoreDatabase(runCtx, plan.database.service, plan.database.databaseName, databaseInput, plan.database.checksum); err != nil {
		return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("import checksum-verified MariaDB backup: %w", err))
	}
	if err := operations.compose(runCtx, "up", "--remove-orphans", "--wait", "--wait-timeout", "600", "-d"); err != nil {
		return rollbackSiteRestore(operations, plan, temporaryVolumes, recoveryVolumes, workDir, fmt.Errorf("start restored site: %w", err))
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), siteRestoreCleanupTimeout)
	defer cancel()
	cleanupErr := cleanupRestoreArtifacts(cleanupCtx, operations, append(temporaryVolumes, recoveryVolumes...), workDir)
	if cleanupErr != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Restore completed, but temporary artifacts need manual cleanup: %v\n", cleanupErr)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "Full-site restore completed transactionally.")
	return err
}

func finishRestoreBeforeOutage(operations siteBackupOperations, temporaryVolumes []string, workDir string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), siteRestoreCleanupTimeout)
	defer cancel()
	if cleanupErr := cleanupRestoreArtifacts(cleanupCtx, operations, temporaryVolumes, workDir); cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("clean up staged restore artifacts: %w", cleanupErr))
	}
	return cause
}

func restartSiteBeforeRestoreCommit(operations siteBackupOperations, temporaryVolumes, recoveryVolumes []string, workDir string, cause error) error {
	restartErr := runRestoreRecoveryStep(siteRestoreComposeRecoveryTimeout, func(stepCtx context.Context) error {
		return operations.compose(stepCtx, "up", "--remove-orphans", "--wait", "--wait-timeout", "600", "-d")
	})
	if restartErr != nil {
		return retainedRestoreError(errors.Join(cause, fmt.Errorf("restart original site before restore commit: %w", restartErr)), recoveryVolumes, temporaryVolumes, workDir)
	}
	cleanupErr := runRestoreRecoveryStep(siteRestoreCleanupTimeout, func(stepCtx context.Context) error {
		return cleanupRestoreArtifacts(stepCtx, operations, append(temporaryVolumes, recoveryVolumes...), workDir)
	})
	if cleanupErr != nil {
		return errors.Join(fmt.Errorf("restore stopped before commit and the original site was restarted: %w", cause), fmt.Errorf("clean up staged and recovery artifacts: %w", cleanupErr))
	}
	return fmt.Errorf("restore stopped before commit and the original site was restarted: %w", cause)
}

func rollbackSiteRestore(operations siteBackupOperations, plan siteRestorePlan, temporaryVolumes, recoveryVolumes []string, workDir string, cause error) error {
	errs := []error{cause}
	if downErr := runRestoreRecoveryStep(siteRestoreComposeRecoveryTimeout, func(stepCtx context.Context) error {
		return operations.compose(stepCtx, "down", "--remove-orphans", "--timeout", "120")
	}); downErr != nil {
		stopErr := runRestoreRecoveryStep(siteRestoreComposeRecoveryTimeout, func(stepCtx context.Context) error {
			return operations.compose(stepCtx, "stop", "--timeout", "120")
		})
		if stopErr != nil {
			return retainedRestoreError(errors.Join(
				cause,
				fmt.Errorf("stop partial restore before rollback: %w", downErr),
				fmt.Errorf("quiesce partial restore after Compose down failed: %w", stopErr),
			), recoveryVolumes, temporaryVolumes, workDir)
		}
	}
	if attachmentErr := runRestoreRecoveryStep(siteRestoreComposeRecoveryTimeout, func(stepCtx context.Context) error {
		for _, volume := range plan.volumes {
			if err := operations.ensureVolumeAttachments(stepCtx, volume.actual, ""); err != nil {
				return fmt.Errorf("volume %s remains attached: %w", volume.logical, err)
			}
		}
		return operations.ensureVolumeAttachments(stepCtx, plan.database.actualVolume, "")
	}); attachmentErr != nil {
		return retainedRestoreError(errors.Join(
			cause,
			fmt.Errorf("verify partial restore is detached before rollback: %w", attachmentErr),
			errors.New("the site remains stopped to prevent concurrent writes during manual recovery"),
		), recoveryVolumes, temporaryVolumes, workDir)
	}

	for _, volume := range plan.volumes {
		if volume.originalExists {
			if err := runRestoreRecoveryStep(siteRestoreVolumeRecoveryTimeout, func(stepCtx context.Context) error {
				return operations.clearVolume(stepCtx, plan.image, volume.actual)
			}); err != nil {
				errs = append(errs, fmt.Errorf("clear volume %s for rollback: %w", volume.logical, err))
				continue
			}
			if err := runRestoreRecoveryStep(siteRestoreVolumeRecoveryTimeout, func(stepCtx context.Context) error {
				return operations.restoreSnapshot(stepCtx, plan.image, volume.recoveryVolume, volume.actual)
			}); err != nil {
				errs = append(errs, fmt.Errorf("restore recovery snapshot for volume %s: %w", volume.logical, err))
			}
			continue
		}
		if err := runRestoreRecoveryStep(siteRestoreCleanupTimeout, func(stepCtx context.Context) error {
			return removeVolumeIfPresent(stepCtx, operations, volume.actual)
		}); err != nil {
			errs = append(errs, fmt.Errorf("remove newly created volume %s during rollback: %w", volume.logical, err))
		}
	}
	if plan.database.originalExists {
		if err := runRestoreRecoveryStep(siteRestoreVolumeRecoveryTimeout, func(stepCtx context.Context) error {
			return operations.clearVolume(stepCtx, plan.image, plan.database.actualVolume)
		}); err != nil {
			errs = append(errs, fmt.Errorf("clear MariaDB volume for rollback: %w", err))
		} else if err := runRestoreRecoveryStep(siteRestoreVolumeRecoveryTimeout, func(stepCtx context.Context) error {
			return operations.restoreSnapshot(stepCtx, plan.image, plan.database.recoveryVolume, plan.database.actualVolume)
		}); err != nil {
			errs = append(errs, fmt.Errorf("restore MariaDB recovery snapshot: %w", err))
		}
	} else if err := runRestoreRecoveryStep(siteRestoreCleanupTimeout, func(stepCtx context.Context) error {
		return removeVolumeIfPresent(stepCtx, operations, plan.database.actualVolume)
	}); err != nil {
		errs = append(errs, fmt.Errorf("remove newly created MariaDB volume during rollback: %w", err))
	}
	if len(errs) > 1 {
		errs = append(errs, errors.New("the site remains stopped because at least one original volume was not recovered exactly"))
		return retainedRestoreError(errors.Join(errs...), recoveryVolumes, temporaryVolumes, workDir)
	}
	if err := runRestoreRecoveryStep(siteRestoreComposeRecoveryTimeout, func(stepCtx context.Context) error {
		return operations.compose(stepCtx, "up", "--remove-orphans", "--wait", "--wait-timeout", "600", "-d")
	}); err != nil {
		return retainedRestoreError(errors.Join(cause, fmt.Errorf("restart site after rollback: %w", err)), recoveryVolumes, temporaryVolumes, workDir)
	}
	cleanupErr := runRestoreRecoveryStep(siteRestoreCleanupTimeout, func(stepCtx context.Context) error {
		return cleanupRestoreArtifacts(stepCtx, operations, append(temporaryVolumes, recoveryVolumes...), workDir)
	})
	if cleanupErr != nil {
		return errors.Join(cause, fmt.Errorf("rollback succeeded but cleanup failed: %w", cleanupErr))
	}
	return fmt.Errorf("restore failed and the original site was rolled back and restarted: %w", cause)
}

func runRestoreRecoveryStep(timeout time.Duration, operation func(context.Context) error) error {
	stepCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return operation(stepCtx)
}

func retainedRestoreError(cause error, recoveryVolumes, stagedVolumes []string, workDir string) error {
	recovery := "none"
	if len(recoveryVolumes) > 0 {
		recovery = strings.Join(recoveryVolumes, ", ")
	}
	staged := "none"
	if len(stagedVolumes) > 0 {
		staged = strings.Join(stagedVolumes, ", ")
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = "none"
	}
	return fmt.Errorf("restore and automatic recovery did not complete; retained recovery Docker volumes: %s; retained staged Docker volumes: %s; retained database input directory: %s: %w", recovery, staged, workDir, cause)
}

func cleanupRestoreArtifacts(runCtx context.Context, operations siteBackupOperations, volumes []string, workDir string) error {
	errs := []error{}
	for _, volume := range uniqueStrings(volumes) {
		if err := removeVolumeIfPresent(runCtx, operations, volume); err != nil {
			errs = append(errs, fmt.Errorf("remove temporary Docker volume %s: %w", volume, err))
		}
	}
	if strings.TrimSpace(workDir) != "" {
		if err := operations.removeWorkDirectory(runCtx, workDir); err != nil {
			errs = append(errs, fmt.Errorf("remove restore work directory %s: %w", workDir, err))
		}
	}
	return errors.Join(errs...)
}

func removeVolumeIfPresent(runCtx context.Context, operations siteBackupOperations, volume string) error {
	exists, err := operations.volumeExists(runCtx, volume)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return operations.removeVolume(runCtx, volume)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (r *siteBackupRuntime) command(runCtx context.Context) *cobra.Command {
	clone := *r.cmd
	clone.SetContext(runCtx)
	return &clone
}

func (r *siteBackupRuntime) runningServices(runCtx context.Context) ([]string, error) {
	output, err := r.composeOutput(runCtx, "ps", "--services", "--status", "running")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	services := []string{}
	for _, line := range strings.Split(output, "\n") {
		service := strings.TrimSpace(line)
		if service == "" {
			continue
		}
		if !backupVolumeNamePattern.MatchString(service) {
			return nil, fmt.Errorf("docker compose returned unsafe service name %q", service)
		}
		if !seen[service] {
			seen[service] = true
			services = append(services, service)
		}
	}
	sort.Strings(services)
	return services, nil
}

func (r *siteBackupRuntime) compose(runCtx context.Context, args ...string) error {
	return runContextCompose(r.command(runCtx), *r.ctx, args)
}

func (r *siteBackupRuntime) composeOutput(runCtx context.Context, args ...string) (string, error) {
	commandName := ""
	if len(args) > 0 {
		commandName = args[0]
	}
	dockerArgs := []string{"compose"}
	dockerArgs = append(dockerArgs, r.ctx.DockerComposeGlobalArgsForCommand(commandName)...)
	dockerArgs = append(dockerArgs, r.ctx.DockerComposeSubcommandArgs(args)...)
	command := exec.Command("docker", dockerArgs...)
	command.Dir = r.ctx.ProjectDir
	return r.ctx.RunQuietCommandContext(runCtx, command)
}

func (r *siteBackupRuntime) backupDatabase(runCtx context.Context, service, database, output string) error {
	return runMariaDBBackup(r.command(runCtx), r.ctx, mariaDBBackupOptions{service: service, output: output, database: database, compress: true})
}

func (r *siteBackupRuntime) restoreDatabase(runCtx context.Context, service, database, input, expectedChecksum string) error {
	return runMariaDBImport(r.command(runCtx), r.ctx, mariaDBImportOptions{
		service:          service,
		input:            input,
		database:         database,
		expectedChecksum: expectedChecksum,
		yolo:             true,
	})
}

func (r *siteBackupRuntime) validateArtifact(runCtx context.Context, backupDir, archive, checksum string, tarArchive bool) error {
	return validateContextBackupArtifact(runCtx, r.ctx, backupDir, archive, checksum, tarArchive)
}

func (r *siteBackupRuntime) backupVolume(runCtx context.Context, image, volume, backupDir, archive string) error {
	commands, err := backupContainerDockerArgs(image, volume, backupDir, archive, false)
	if err != nil {
		return err
	}
	for _, args := range commands {
		if err := r.runDockerHelper(runCtx, args...); err != nil {
			return fmt.Errorf("backup volume %s: %w", volume, err)
		}
	}
	uid, err := r.hostIdentity(runCtx, "-u")
	if err != nil {
		return fmt.Errorf("resolve backup owner uid: %w", err)
	}
	gid, err := r.hostIdentity(runCtx, "-g")
	if err != nil {
		return fmt.Errorf("resolve backup owner gid: %w", err)
	}
	secureCommands, err := secureCreatedArtifactDockerArgs(image, backupDir, archive, uid, gid)
	if err != nil {
		return err
	}
	for _, args := range secureCommands {
		if err := r.runDockerHelper(runCtx, args...); err != nil {
			return fmt.Errorf("secure backup volume archive %s: %w", archive, err)
		}
	}
	return nil
}

func (r *siteBackupRuntime) hostIdentity(runCtx context.Context, flag string) (string, error) {
	output, err := r.ctx.RunQuietCommandContext(runCtx, exec.Command("id", flag))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(output)
	if !numericHostIDPattern.MatchString(value) {
		return "", fmt.Errorf("id %s returned unsafe value %q", flag, value)
	}
	return value, nil
}

func (r *siteBackupRuntime) volumeExists(runCtx context.Context, volume string) (bool, error) {
	if !backupVolumeNamePattern.MatchString(volume) {
		return false, fmt.Errorf("unsafe Docker volume name %q", volume)
	}
	output, err := r.dockerOutput(runCtx, "volume", "ls", "--quiet", "--filter", "name="+volume)
	if err != nil {
		return false, err
	}
	for _, candidate := range strings.Split(output, "\n") {
		if strings.TrimSpace(candidate) == volume {
			return true, nil
		}
	}
	return false, nil
}

type siteDockerVolumeInspect struct {
	Name    string            `json:"Name"`
	Driver  string            `json:"Driver"`
	Scope   string            `json:"Scope"`
	Labels  map[string]string `json:"Labels"`
	Options map[string]string `json:"Options"`
}

func (r *siteBackupRuntime) validateOwnedVolume(runCtx context.Context, logical, actual string) error {
	if !backupVolumeNamePattern.MatchString(logical) || !backupVolumeNamePattern.MatchString(actual) {
		return fmt.Errorf("unsafe owned Docker volume identity %q (%q)", logical, actual)
	}
	output, err := r.dockerOutput(runCtx, "volume", "inspect", actual)
	if err != nil {
		return err
	}
	var inspected []siteDockerVolumeInspect
	if err := json.Unmarshal([]byte(output), &inspected); err != nil {
		return fmt.Errorf("decode Docker volume inspection for %q: %w", actual, err)
	}
	if len(inspected) != 1 || inspected[0].Name != actual {
		return fmt.Errorf("Docker returned an ambiguous inspection result for owned volume %q", actual)
	}
	volume := inspected[0]
	if volume.Driver != "local" || (volume.Scope != "" && volume.Scope != "local") || len(volume.Options) > 0 {
		return fmt.Errorf("owned volume %q is not an option-free local Docker volume (driver=%q scope=%q)", actual, volume.Driver, volume.Scope)
	}
	expectedProject := strings.TrimSpace(r.ctx.EffectiveComposeProjectName())
	if expectedProject == "" || strings.ContainsAny(expectedProject, "\x00\r\n") {
		return fmt.Errorf("active context has an unsafe empty Compose project identity")
	}
	if volume.Labels["com.docker.compose.project"] != expectedProject || volume.Labels["com.docker.compose.volume"] != logical {
		return fmt.Errorf("volume %q is not owned by Compose project %q as logical volume %q", actual, expectedProject, logical)
	}
	return nil
}

func (r *siteBackupRuntime) createOwnedVolume(runCtx context.Context, logical, actual string) error {
	if !backupVolumeNamePattern.MatchString(logical) || !backupVolumeNamePattern.MatchString(actual) {
		return fmt.Errorf("unsafe owned Docker volume identity %q (%q)", logical, actual)
	}
	project := strings.TrimSpace(r.ctx.EffectiveComposeProjectName())
	if project == "" || strings.ContainsAny(project, "\x00\r\n") {
		return fmt.Errorf("active context has an unsafe empty Compose project identity")
	}
	if err := r.docker(runCtx,
		"volume", "create", "--driver", "local",
		"--label", "com.docker.compose.project="+project,
		"--label", "com.docker.compose.volume="+logical,
		actual,
	); err != nil {
		return err
	}
	return r.validateOwnedVolume(runCtx, logical, actual)
}

func (r *siteBackupRuntime) ensureVolumeAttachments(runCtx context.Context, volume, allowedService string) error {
	if !backupVolumeNamePattern.MatchString(volume) {
		return fmt.Errorf("unsafe Docker volume name %q", volume)
	}
	if allowedService != "" && !backupVolumeNamePattern.MatchString(allowedService) {
		return fmt.Errorf("unsafe allowed Compose service %q", allowedService)
	}
	cli, err := coredocker.GetDockerCli(r.ctx)
	if err != nil {
		return err
	}
	defer cli.Close()
	filterArgs := filters.NewArgs()
	filterArgs.Add("volume", volume)
	containers, err := cli.CLI.ContainerList(runCtx, dockercontainer.ListOptions{Filters: filterArgs})
	if err != nil {
		return err
	}
	if allowedService == "" {
		if len(containers) != 0 {
			return fmt.Errorf("volume %q remains mounted by %d running container(s)", volume, len(containers))
		}
		return nil
	}
	if len(containers) != 1 {
		return fmt.Errorf("volume %q must be mounted by exactly one running %s service container; found %d", volume, allowedService, len(containers))
	}
	labels := containers[0].Labels
	if labels["com.docker.compose.project"] != r.ctx.EffectiveComposeProjectName() ||
		labels["com.docker.compose.service"] != allowedService ||
		strings.EqualFold(labels["com.docker.compose.oneoff"], "true") {
		return fmt.Errorf("volume %q is mounted by an unexpected or one-off running container", volume)
	}
	return nil
}

func (r *siteBackupRuntime) createTemporaryVolume(runCtx context.Context, volume string) error {
	exists, err := r.volumeExists(runCtx, volume)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("refusing to reuse existing restore volume %q", volume)
	}
	return r.docker(runCtx, "volume", "create", "--label", "io.libops.sitectl.restore=true", volume)
}

func (r *siteBackupRuntime) removeVolume(runCtx context.Context, volume string) error {
	if !backupVolumeNamePattern.MatchString(volume) {
		return fmt.Errorf("unsafe Docker volume name %q", volume)
	}
	return r.docker(runCtx, "volume", "rm", volume)
}

func (r *siteBackupRuntime) stageArtifact(runCtx context.Context, image, backupDir, archive, checksum, stageVolume string, database bool) error {
	checksum = strings.ToLower(strings.TrimSpace(checksum))
	if !sha256HexPattern.MatchString(checksum) {
		return fmt.Errorf("validated SHA-256 checksum for %q is invalid", archive)
	}
	copyArgs, checkArgs, err := stagedArtifactDockerArgs(image, backupDir, archive, stageVolume, database)
	if err != nil {
		return err
	}
	if err := r.runDockerHelper(runCtx, copyArgs...); err != nil {
		return fmt.Errorf("copy backup archive into staging volume: %w", err)
	}
	digestArgs, err := stagedArtifactChecksumDockerArgs(image, archive, stageVolume)
	if err != nil {
		return err
	}
	output, err := r.runDockerHelperOutput(runCtx, digestArgs...)
	if err != nil {
		return fmt.Errorf("checksum staged backup archive: %w", err)
	}
	fields := strings.Fields(output)
	if len(fields) < 1 || !sha256HexPattern.MatchString(strings.ToLower(fields[0])) || strings.ToLower(fields[0]) != checksum {
		return fmt.Errorf("staged backup archive %q does not match its validated SHA-256 checksum", archive)
	}
	if err := r.runDockerHelper(runCtx, checkArgs...); err != nil {
		return fmt.Errorf("validate staged backup archive: %w", err)
	}
	return nil
}

func (r *siteBackupRuntime) snapshotVolume(runCtx context.Context, image, source, recovery string) error {
	args, err := snapshotVolumeDockerArgs(image, source, recovery)
	if err != nil {
		return err
	}
	return r.runDockerHelper(runCtx, args...)
}

func (r *siteBackupRuntime) clearVolume(runCtx context.Context, image, volume string) error {
	args, err := clearVolumeDockerArgs(image, volume)
	if err != nil {
		return err
	}
	return r.runDockerHelper(runCtx, args...)
}

func (r *siteBackupRuntime) extractStagedVolume(runCtx context.Context, image, stageVolume, archive, target string) error {
	args, err := extractStagedVolumeDockerArgs(image, stageVolume, archive, target)
	if err != nil {
		return err
	}
	return r.runDockerHelper(runCtx, args...)
}

func (r *siteBackupRuntime) restoreSnapshot(runCtx context.Context, image, recovery, target string) error {
	args, err := restoreSnapshotDockerArgs(image, recovery, target)
	if err != nil {
		return err
	}
	return r.runDockerHelper(runCtx, args...)
}

func (r *siteBackupRuntime) waitForDatabase(runCtx context.Context, service string) error {
	waitArgs, argsErr := mariaDBRestoreWaitComposeArgs(service)
	if argsErr != nil {
		return argsErr
	}
	if err := r.compose(runCtx, waitArgs...); err == nil {
		return nil
	} else {
		waitErr := fmt.Errorf("MariaDB service %q did not become ready within 600 seconds before import: %w", service, err)
		diagnosticErrs := []error{waitErr}
		if diagnosticErr := r.compose(runCtx, "ps", service); diagnosticErr != nil {
			diagnosticErrs = append(diagnosticErrs, fmt.Errorf("inspect MariaDB service after readiness failure: %w", diagnosticErr))
		}
		if diagnosticErr := r.compose(runCtx, "logs", "--no-color", "--tail", "200", service); diagnosticErr != nil {
			diagnosticErrs = append(diagnosticErrs, fmt.Errorf("read MariaDB diagnostics after readiness failure: %w", diagnosticErr))
		}
		return errors.Join(diagnosticErrs...)
	}
}

func (r *siteBackupRuntime) prepareRestoreWorkspace(runCtx context.Context, backupDir string, archives []string, identifier string) (string, error) {
	if _, _, err := validateBackupContainerInputs("restore-input", backupDir); err != nil {
		return "", err
	}
	if len(archives) == 0 {
		return "", fmt.Errorf("restore workspace requires at least one artifact")
	}
	if !restoreIdentifierPattern.MatchString(identifier) {
		return "", fmt.Errorf("restore transaction identifier is invalid")
	}
	relative := filepath.Join(".sitectl", "restore-work-"+identifier)
	workDir, err := resolveProjectBackupPath(runCtx, r.ctx, relative, false)
	if err != nil {
		return "", err
	}
	if _, err := r.ctx.RunCommandContext(runCtx, exec.Command("mkdir", "-m", "0700", "-p", "--", filepath.Dir(workDir))); err != nil { // #nosec G204 -- the parent is a canonical project-contained path.
		return "", err
	}
	if _, err := r.ctx.RunCommandContext(runCtx, exec.Command("mkdir", "-m", "0700", "--", workDir)); err != nil { // #nosec G204 -- exclusive mkdir prevents reusing another transaction's private workspace.
		return "", err
	}
	if _, err := r.ctx.RunCommandContext(runCtx, exec.Command("chmod", "0700", "--", workDir)); err != nil { // #nosec G204 -- workDir is a canonical project-contained path.
		return workDir, err
	}
	workDir, err = resolveProjectBackupPath(runCtx, r.ctx, relative, true)
	if err != nil {
		return workDir, err
	}
	seen := map[string]bool{}
	for _, archive := range archives {
		if err := validateBackupArchiveName(archive); err != nil {
			return workDir, err
		}
		if seen[archive] {
			return workDir, fmt.Errorf("restore artifact %q is duplicated", archive)
		}
		seen[archive] = true
		source, err := resolveContainedBackupFile(runCtx, r.ctx, backupDir, archive)
		if err != nil {
			return workDir, err
		}
		destination := filepath.Join(workDir, archive)
		if _, err := r.ctx.RunCommandContext(runCtx, exec.Command("cp", "--", source, destination)); err != nil { // #nosec G204 -- both canonical, project-contained paths are distinct arguments to fixed cp.
			return workDir, err
		}
		if _, err := r.ctx.RunCommandContext(runCtx, exec.Command("chmod", "0600", "--", destination)); err != nil { // #nosec G204 -- destination is a canonical project-contained path.
			return workDir, err
		}
	}
	return workDir, nil
}

func (r *siteBackupRuntime) removeWorkDirectory(runCtx context.Context, workDir string) error {
	if strings.TrimSpace(workDir) == "" || !filepath.IsAbs(workDir) {
		return fmt.Errorf("refusing to remove unsafe restore work directory %q", workDir)
	}
	project, err := contextRealpath(runCtx, r.ctx, true, r.ctx.ProjectDir)
	if err != nil {
		return err
	}
	resolved, err := contextRealpath(runCtx, r.ctx, true, workDir)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(project, resolved)
	base := filepath.Base(relative)
	identifier := strings.TrimPrefix(base, "restore-work-")
	if err != nil || filepath.Dir(relative) != ".sitectl" || base == identifier || !restoreIdentifierPattern.MatchString(identifier) || filepath.IsAbs(relative) {
		return fmt.Errorf("refusing to remove restore work directory outside project")
	}
	_, err = r.ctx.RunCommandContext(runCtx, exec.Command("rm", "-rf", "--", resolved)) // #nosec G204 -- resolved was re-canonicalized and confined to the project immediately before removal.
	return err
}

const siteBackupHelperLabel = "io.libops.sitectl.backup-helper"

type backupContainerRemover interface {
	ContainerRemove(context.Context, string, dockercontainer.RemoveOptions) error
}

func (r *siteBackupRuntime) runDockerHelper(runCtx context.Context, args ...string) error {
	identifier, err := newRestoreIdentifier()
	if err != nil {
		return fmt.Errorf("create Docker helper identifier: %w", err)
	}
	name := "sitectl-backup-helper-" + identifier
	namedArgs, err := namedBackupHelperArgs(args, name, identifier)
	if err != nil {
		return err
	}
	runErr := r.docker(runCtx, namedArgs...)
	cleanupErr := r.removeDockerHelper(name, identifier)
	if cleanupErr != nil {
		return errors.Join(runErr, fmt.Errorf("stop and remove Docker helper %s: %w", name, cleanupErr))
	}
	return runErr
}

func (r *siteBackupRuntime) runDockerHelperOutput(runCtx context.Context, args ...string) (string, error) {
	identifier, err := newRestoreIdentifier()
	if err != nil {
		return "", fmt.Errorf("create Docker helper identifier: %w", err)
	}
	name := "sitectl-backup-helper-" + identifier
	namedArgs, err := namedBackupHelperArgs(args, name, identifier)
	if err != nil {
		return "", err
	}
	output, runErr := r.dockerOutput(runCtx, namedArgs...)
	cleanupErr := r.removeDockerHelper(name, identifier)
	if cleanupErr != nil {
		return output, errors.Join(runErr, fmt.Errorf("stop and remove Docker helper %s: %w", name, cleanupErr))
	}
	return output, runErr
}

func namedBackupHelperArgs(args []string, name, identifier string) ([]string, error) {
	if len(args) == 0 || args[0] != "run" {
		return nil, fmt.Errorf("Docker helper command must be a docker run invocation")
	}
	if !backupVolumeNamePattern.MatchString(name) || !restoreIdentifierPattern.MatchString(identifier) {
		return nil, fmt.Errorf("Docker helper identity is unsafe")
	}
	result := []string{"run", "--name", name, "--label", siteBackupHelperLabel + "=" + identifier}
	for _, arg := range args[1:] {
		if arg == "--rm" {
			continue
		}
		if arg == "--name" {
			return nil, fmt.Errorf("Docker helper command already declares a container name")
		}
		result = append(result, arg)
	}
	return result, nil
}

func (r *siteBackupRuntime) removeDockerHelper(name, identifier string) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), siteBackupHelperCleanupTimeout)
	defer cancel()
	cli, err := coredocker.GetDockerCli(r.ctx)
	if err != nil {
		return err
	}
	defer cli.Close()
	inspected, err := cli.CLI.ContainerInspect(cleanupCtx, name)
	if containerderrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if inspected.Config == nil || inspected.Config.Labels[siteBackupHelperLabel] != identifier {
		return fmt.Errorf("refusing to remove container %q without the current helper ownership label", name)
	}
	if inspected.ContainerJSONBase == nil || strings.TrimSpace(inspected.ID) == "" {
		return fmt.Errorf("refusing to remove Docker helper %q without an inspected container ID", name)
	}
	remover, ok := cli.CLI.(backupContainerRemover)
	if !ok {
		return fmt.Errorf("Docker client does not support forced helper removal")
	}
	if err := remover.ContainerRemove(cleanupCtx, inspected.ID, dockercontainer.RemoveOptions{Force: true}); err != nil && !containerderrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *siteBackupRuntime) docker(runCtx context.Context, args ...string) error {
	_, err := r.ctx.RunCommandContext(runCtx, exec.Command("docker", args...)) // #nosec G204 -- Docker is fixed and all dynamic values remain separate validated arguments.
	return err
}

func (r *siteBackupRuntime) dockerOutput(runCtx context.Context, args ...string) (string, error) {
	return r.ctx.RunQuietCommandContext(runCtx, exec.Command("docker", args...)) // #nosec G204 -- Docker is fixed and all dynamic values remain separate validated arguments.
}

func stagedArtifactDockerArgs(image, backupDir, archive, stageVolume string, database bool) ([]string, []string, error) {
	image, backupDir, err := validateBackupContainerInputs(image, backupDir)
	if err != nil {
		return nil, nil, err
	}
	if err := validateBackupArchiveName(archive); err != nil {
		return nil, nil, err
	}
	if !backupVolumeNamePattern.MatchString(stageVolume) {
		return nil, nil, fmt.Errorf("unsafe staging volume name %q", stageVolume)
	}
	copyArgs := []string{
		"run", "--rm", "--entrypoint", "cp",
		"-v", backupDir + ":/backup:ro",
		"-v", stageVolume + ":/staged:rw",
		image, path.Join("/backup", archive), path.Join("/staged", archive),
	}
	entrypoint := "tar"
	check := []string{"-tzf", path.Join("/staged", archive)}
	if database {
		entrypoint = "gzip"
		check = []string{"-t", path.Join("/staged", archive)}
	}
	checkArgs := []string{
		"run", "--rm", "--entrypoint", entrypoint,
		"-v", stageVolume + ":/staged:ro",
		image,
	}
	return copyArgs, append(checkArgs, check...), nil
}

func stagedArtifactChecksumDockerArgs(image, archive, stageVolume string) ([]string, error) {
	image = strings.TrimSpace(image)
	if image == "" || strings.HasPrefix(image, "-") || strings.ContainsAny(image, "\x00\r\n") {
		return nil, fmt.Errorf("backup image must be a non-empty safe value")
	}
	if err := validateBackupArchiveName(archive); err != nil {
		return nil, err
	}
	if !backupVolumeNamePattern.MatchString(stageVolume) {
		return nil, fmt.Errorf("unsafe staging volume name %q", stageVolume)
	}
	return []string{
		"run", "--rm", "--entrypoint", "sha256sum",
		"-v", stageVolume + ":/staged:ro",
		image, path.Join("/staged", archive),
	}, nil
}

func secureCreatedArtifactDockerArgs(image, backupDir, archive, uid, gid string) ([][]string, error) {
	image, backupDir, err := validateBackupContainerInputs(image, backupDir)
	if err != nil {
		return nil, err
	}
	if err := validateBackupArchiveName(archive); err != nil {
		return nil, err
	}
	if !numericHostIDPattern.MatchString(uid) || !numericHostIDPattern.MatchString(gid) {
		return nil, fmt.Errorf("backup owner must use numeric uid and gid")
	}
	artifact := path.Join("/backup", archive)
	return [][]string{
		{"run", "--rm", "--entrypoint", "chown", "-v", backupDir + ":/backup:rw", image, uid + ":" + gid, artifact},
		{"run", "--rm", "--entrypoint", "chmod", "-v", backupDir + ":/backup:rw", image, "0600", artifact},
	}, nil
}

func snapshotVolumeDockerArgs(image, source, recovery string) ([]string, error) {
	if err := validateBackupImageAndVolumes(image, source, recovery); err != nil {
		return nil, err
	}
	return []string{
		"run", "--rm", "--entrypoint", "tar",
		"-v", source + ":/source:ro",
		"-v", recovery + ":/recovery:rw",
		image, "-C", "/source", "-cf", "/recovery/contents.tar", ".",
	}, nil
}

func clearVolumeDockerArgs(image, volume string) ([]string, error) {
	if err := validateBackupImageAndVolumes(image, volume); err != nil {
		return nil, err
	}
	return []string{
		"run", "--rm", "--entrypoint", "find",
		"-v", volume + ":/target:rw",
		image, "/target", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "--", "{}", "+",
	}, nil
}

func extractStagedVolumeDockerArgs(image, stageVolume, archive, target string) ([]string, error) {
	if err := validateBackupImageAndVolumes(image, stageVolume, target); err != nil {
		return nil, err
	}
	if err := validateBackupArchiveName(archive); err != nil {
		return nil, err
	}
	return []string{
		"run", "--rm", "--entrypoint", "tar",
		"-v", stageVolume + ":/staged:ro",
		"-v", target + ":/target:rw",
		image, "--extract", "--gzip", "--file", path.Join("/staged", archive), "--directory", "/target", "--numeric-owner", "--delay-directory-restore",
	}, nil
}

func restoreSnapshotDockerArgs(image, recovery, target string) ([]string, error) {
	if err := validateBackupImageAndVolumes(image, recovery, target); err != nil {
		return nil, err
	}
	return []string{
		"run", "--rm", "--entrypoint", "tar",
		"-v", recovery + ":/recovery:ro",
		"-v", target + ":/target:rw",
		image, "-C", "/target", "--extract", "--file", "/recovery/contents.tar", "--numeric-owner", "--delay-directory-restore",
	}, nil
}

func validateBackupImageAndVolumes(image string, volumes ...string) error {
	image = strings.TrimSpace(image)
	if image == "" || strings.HasPrefix(image, "-") || strings.ContainsAny(image, "\x00\r\n") {
		return fmt.Errorf("backup image must be a non-empty safe value")
	}
	for _, volume := range volumes {
		if !backupVolumeNamePattern.MatchString(volume) {
			return fmt.Errorf("unsafe Docker volume name %q", volume)
		}
	}
	return nil
}
