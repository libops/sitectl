package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	corejob "github.com/libops/sitectl/pkg/job"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

type siteBackupManifest struct {
	Version         int               `yaml:"version"`
	CreatedAt       string            `yaml:"createdAt"`
	Database        string            `yaml:"database"`
	DatabaseName    string            `yaml:"databaseName"`
	DatabaseVolume  string            `yaml:"databaseVolume"`
	Volumes         map[string]string `yaml:"volumes"`
	ExternalVolumes []string          `yaml:"externalVolumes,omitempty"`
	ExcludedStorage []string          `yaml:"excludedStorage,omitempty"`
	Checksums       map[string]string `yaml:"sha256"`
}

const siteBackupManifestVersion = 2
const supportedMariaDBDataTarget = "/var/lib/mysql"

type siteVolumeRestore struct {
	logical        string
	actual         string
	archive        string
	checksum       string
	stageVolume    string
	recoveryVolume string
	originalExists bool
}

type siteVolumeBackupTarget struct {
	logical string
	actual  string
	archive string
}

type siteDatabaseRestore struct {
	service        string
	databaseName   string
	logicalVolume  string
	actualVolume   string
	archive        string
	checksum       string
	recoveryVolume string
	originalExists bool
}

type siteRestorePlan struct {
	image      string
	backupDir  string
	database   siteDatabaseRestore
	volumes    []siteVolumeRestore
	identifier string
}

var backupVolumeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var sha256HexPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var restoreIdentifierPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var numericHostIDPattern = regexp.MustCompile(`^[0-9]+$`)

func init() { RootCmd.AddCommand(backupCommand()) }

func backupCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create or restore a full-site database and file-volume backup", GroupID: "ops"}
	cmd.AddCommand(backupCreateCommand(), backupRestoreCommand())
	return cmd
}

func backupCreateCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Back up MariaDB and application file volumes",
		Args:  cobra.NoArgs,
		Long: `Create a compressed logical MariaDB dump plus tar archives for named
Compose volumes not mounted as MariaDB data. sitectl briefly stops the running
application services while leaving MariaDB available, then resumes exactly the
services that were running even when backup creation fails. Full-site backup is
currently supported from Linux operator hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireFullSiteBackupHostSupport(runtime.GOOS); err != nil {
				return err
			}
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			if output == "" {
				output = filepath.Join("backups", time.Now().UTC().Format("20060102T150405Z"))
			}
			return withProjectMutationLock(cmd, ctx, func(lockedCmd *cobra.Command) error {
				return runSiteBackupCreate(lockedCmd, ctx, output)
			})
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Project-relative backup directory; defaults to backups/YYYYMMDDTHHMMSSZ.")
	return cmd
}

func runSiteBackupCreate(cmd *cobra.Command, ctx *config.Context, output string) (resultErr error) {
	candidate, err := safeProjectRelativePath(ctx, output)
	if err != nil {
		return err
	}
	exists, err := corejob.PathExistsOnContext(ctx, candidate)
	if err != nil {
		return fmt.Errorf("inspect backup output path: %w", err)
	}
	if exists {
		return fmt.Errorf("refusing to overwrite existing backup directory %q", candidate)
	}
	backupDir, err := resolveProjectBackupPath(cmd.Context(), ctx, output, false)
	if err != nil {
		return err
	}

	compose, err := inspectComposeSecrets(cmd, ctx)
	if err != nil {
		return err
	}
	image := strings.TrimSpace(compose.Services["init"].Image)
	if image == "" {
		return fmt.Errorf("compose init service must declare an image for volume backup tooling")
	}
	if _, _, err := validateBackupContainerInputs(image, backupDir); err != nil {
		return err
	}
	databaseService := firstNonBlank(ctx.DatabaseService, defaultMariaDBService)
	if _, ok := compose.Services[databaseService]; !ok {
		return fmt.Errorf("MariaDB service %q is not declared by current Compose config", databaseService)
	}

	databaseName := strings.TrimSpace(ctx.DatabaseName)
	if databaseName == "" {
		return fmt.Errorf("active context must declare database-name for a full-site backup")
	}
	if err := validateMariaDBDatabaseName(databaseName); err != nil {
		return fmt.Errorf("validate full-site database name: %w", err)
	}
	databaseSources, err := databaseDataVolumeSources(compose, databaseService)
	if err != nil {
		return err
	}
	var databaseLogical string
	for source := range databaseSources {
		databaseLogical = source
	}
	databaseDeclaration, ok := compose.Volumes[databaseLogical]
	if !ok {
		return fmt.Errorf("MariaDB data volume %q is not declared by current Compose config", databaseLogical)
	}
	if databaseDeclaration.External {
		return fmt.Errorf("full-site backup requires an owned MariaDB data volume; %q is external", databaseLogical)
	}
	if err := validateComposeOwnedVolume(databaseLogical, databaseDeclaration); err != nil {
		return err
	}

	runtime := newSiteBackupRuntime(cmd, ctx)
	databaseActual, err := actualComposeVolumeName(compose, databaseLogical)
	if err != nil {
		return err
	}
	databaseExists, err := runtime.volumeExists(cmd.Context(), databaseActual)
	if err != nil {
		return fmt.Errorf("inspect MariaDB data volume: %w", err)
	}
	if !databaseExists {
		return fmt.Errorf("owned MariaDB data volume %q does not exist; start and initialize the site before backup", databaseActual)
	}
	if err := runtime.validateOwnedVolume(cmd.Context(), databaseLogical, databaseActual); err != nil {
		return fmt.Errorf("validate MariaDB data volume ownership: %w", err)
	}

	volumeTargets := []siteVolumeBackupTarget{}
	externalVolumes := []string{}
	excludedStorage, err := fullSiteExcludedStorage(compose)
	if err != nil {
		return err
	}
	archiveNames := map[string]string{}
	for _, logical := range sortedComposeVolumeNames(compose.Volumes) {
		if logical == databaseLogical {
			continue
		}
		declaration := compose.Volumes[logical]
		if declaration.External {
			descriptor, err := externalVolumeDescriptor(logical, declaration)
			if err != nil {
				return err
			}
			externalVolumes = append(externalVolumes, descriptor)
			continue
		}
		if err := validateComposeOwnedVolume(logical, declaration); err != nil {
			return err
		}
		actual, err := actualComposeVolumeName(compose, logical)
		if err != nil {
			return err
		}
		exists, err := runtime.volumeExists(cmd.Context(), actual)
		if err != nil {
			return fmt.Errorf("inspect backup volume %s: %w", logical, err)
		}
		if !exists {
			return fmt.Errorf("owned Compose volume %q (%s) does not exist; refusing to create a mixed-state backup", logical, actual)
		}
		if err := runtime.validateOwnedVolume(cmd.Context(), logical, actual); err != nil {
			return fmt.Errorf("validate backup volume %s ownership: %w", logical, err)
		}
		archive := backupVolumeArchiveName(logical)
		if previous, duplicate := archiveNames[archive]; duplicate {
			return fmt.Errorf("backup archive collision between Compose volumes %q and %q", previous, logical)
		}
		archiveNames[archive] = logical
		volumeTargets = append(volumeTargets, siteVolumeBackupTarget{logical: logical, actual: actual, archive: archive})
	}
	if err := createPrivateBackupDirectory(cmd.Context(), ctx, backupDir); err != nil {
		return err
	}
	backupDir, err = resolveProjectBackupPath(cmd.Context(), ctx, output, true)
	if err != nil {
		return err
	}

	resultErr = withQuiescedSiteWriters(cmd.Context(), runtime, databaseService, func() error {
		if err := runtime.ensureVolumeAttachments(cmd.Context(), databaseActual, databaseService); err != nil {
			return fmt.Errorf("verify MariaDB backup boundary: %w", err)
		}
		for _, target := range volumeTargets {
			if err := runtime.ensureVolumeAttachments(cmd.Context(), target.actual, ""); err != nil {
				return fmt.Errorf("verify backup boundary for volume %s: %w", target.logical, err)
			}
		}
		databasePath := filepath.Join(backupDir, "mariadb.sql.gz")
		if err := runtime.backupDatabase(cmd.Context(), databaseService, databaseName, databasePath); err != nil {
			return err
		}
		if err := secureContextFile(cmd.Context(), ctx, databasePath); err != nil {
			return fmt.Errorf("secure MariaDB backup: %w", err)
		}
		manifest := siteBackupManifest{
			Version:         siteBackupManifestVersion,
			CreatedAt:       time.Now().UTC().Format(time.RFC3339),
			Database:        "mariadb.sql.gz",
			DatabaseName:    databaseName,
			DatabaseVolume:  databaseLogical,
			Volumes:         map[string]string{},
			ExternalVolumes: externalVolumes,
			ExcludedStorage: excludedStorage,
			Checksums:       map[string]string{},
		}
		databaseChecksum, err := contextFileSHA256(cmd.Context(), ctx, databasePath)
		if err != nil {
			return fmt.Errorf("checksum MariaDB backup: %w", err)
		}
		manifest.Checksums[manifest.Database] = databaseChecksum
		if err := runtime.validateArtifact(cmd.Context(), backupDir, manifest.Database, databaseChecksum, false); err != nil {
			return fmt.Errorf("validate created MariaDB backup: %w", err)
		}
		databaseCheckArgs, err := backupArchiveCheckDockerArgs(image, backupDir, manifest.Database, true)
		if err != nil {
			return err
		}
		if err := runtime.runDockerHelper(cmd.Context(), databaseCheckArgs...); err != nil {
			return fmt.Errorf("validate created MariaDB gzip backup: %w", err)
		}
		for _, target := range volumeTargets {
			if err := runtime.backupVolume(cmd.Context(), image, target.actual, backupDir, target.archive); err != nil {
				return err
			}
			archivePath := filepath.Join(backupDir, target.archive)
			if err := secureContextFile(cmd.Context(), ctx, archivePath); err != nil {
				return fmt.Errorf("secure backup archive for volume %s: %w", target.logical, err)
			}
			checksum, err := contextFileSHA256(cmd.Context(), ctx, archivePath)
			if err != nil {
				return fmt.Errorf("checksum backup archive for volume %s: %w", target.logical, err)
			}
			if err := runtime.validateArtifact(cmd.Context(), backupDir, target.archive, checksum, true); err != nil {
				return fmt.Errorf("validate created backup archive for volume %s: %w", target.logical, err)
			}
			manifest.Volumes[target.logical] = target.archive
			manifest.Checksums[target.archive] = checksum
		}
		encoded, err := yaml.Marshal(manifest)
		if err != nil {
			return fmt.Errorf("encode backup manifest: %w", err)
		}
		if err := ctx.WriteFile(filepath.Join(backupDir, "manifest.yaml"), encoded); err != nil {
			return fmt.Errorf("write backup manifest: %w", err)
		}
		if err := secureContextFile(cmd.Context(), ctx, filepath.Join(backupDir, "manifest.yaml")); err != nil {
			return fmt.Errorf("secure backup manifest: %w", err)
		}
		return nil
	})
	if resultErr != nil {
		return resultErr
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Full-site backup created at %s\n", backupDir)
	if err == nil && (len(externalVolumes) > 0 || len(excludedStorage) > 0) {
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Excluded externally managed storage: volumes=%s mounts=%s\n", strings.Join(externalVolumes, ","), strings.Join(excludedStorage, ","))
	}
	return err
}

func backupRestoreCommand() *cobra.Command {
	var yolo bool
	cmd := &cobra.Command{
		Use:   "restore DIRECTORY",
		Short: "Transactionally replace the site's database and file volumes from a full-site backup",
		Args:  cobra.ExactArgs(1),
		Long: `Validate and stage the complete backup before stopping the site, retain
recovery snapshots of current file and MariaDB volumes, then replace the site.
If a commit step fails, sitectl restores every retained snapshot and restarts the
stack. External Compose volumes are never modified. This destructive operation
requires --yolo after the operator verifies the selected backup and is currently
supported from Linux operator hosts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireFullSiteBackupHostSupport(runtime.GOOS); err != nil {
				return err
			}
			if !yolo {
				return fmt.Errorf("backup restore replaces current site data; rerun with --yolo after verifying the backup")
			}
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			return withProjectMutationLock(cmd, ctx, func(lockedCmd *cobra.Command) error {
				return runSiteBackupRestore(lockedCmd, ctx, args[0])
			})
		},
	}
	cmd.Flags().BoolVar(&yolo, "yolo", false, "Confirm replacement of current database and file-volume contents.")
	return cmd
}

func requireFullSiteBackupHostSupport(goos string) error {
	if goos != "linux" {
		return fmt.Errorf("full-site backup and restore currently require a Linux operator host; %s is not supported", goos)
	}
	return nil
}

func withProjectMutationLock(cmd *cobra.Command, ctx *config.Context, operation func(*cobra.Command) error) (resultErr error) {
	lock, err := ctx.AcquireProjectMutationLock(cmd.Context())
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release project mutation lock: %w", releaseErr))
		}
	}()
	lockedCmd := *cmd
	lockedCmd.SetContext(lock.Context())
	return operation(&lockedCmd)
}

func runSiteBackupRestore(cmd *cobra.Command, ctx *config.Context, directory string) error {
	backupDir, err := resolveProjectBackupPath(cmd.Context(), ctx, directory, true)
	if err != nil {
		return err
	}
	manifestPath, err := resolveContainedBackupFile(cmd.Context(), ctx, backupDir, "manifest.yaml")
	if err != nil {
		return fmt.Errorf("validate backup manifest path: %w", err)
	}
	manifest, err := readSiteBackupManifest(ctx, manifestPath)
	if err != nil {
		return err
	}
	compose, err := inspectComposeSecrets(cmd, ctx)
	if err != nil {
		return err
	}
	identifier, err := newRestoreIdentifier()
	if err != nil {
		return fmt.Errorf("create restore transaction identifier: %w", err)
	}
	plan, err := buildSiteRestorePlan(ctx, compose, manifest, backupDir, identifier)
	if err != nil {
		return err
	}
	return executeSiteRestore(cmd, newSiteBackupRuntime(cmd, ctx), plan)
}

func readSiteBackupManifest(ctx *config.Context, manifestPath string) (siteBackupManifest, error) {
	data, err := ctx.ReadFile(manifestPath)
	if err != nil {
		return siteBackupManifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest siteBackupManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return siteBackupManifest{}, fmt.Errorf("parse backup manifest: %w", err)
	}
	if manifest.Version != siteBackupManifestVersion {
		return siteBackupManifest{}, fmt.Errorf("backup manifest version %d is unsupported; create a new version %d full-site backup before restore", manifest.Version, siteBackupManifestVersion)
	}
	if err := validateBackupArchiveName(manifest.Database); err != nil {
		return siteBackupManifest{}, fmt.Errorf("backup manifest contains an unsafe database archive path: %w", err)
	}
	if strings.TrimSpace(manifest.DatabaseName) == "" {
		return siteBackupManifest{}, fmt.Errorf("backup manifest does not declare an application database name")
	}
	if err := validateMariaDBDatabaseName(manifest.DatabaseName); err != nil {
		return siteBackupManifest{}, fmt.Errorf("backup manifest contains an unsafe database name: %w", err)
	}
	if !backupVolumeNamePattern.MatchString(manifest.DatabaseVolume) {
		return siteBackupManifest{}, fmt.Errorf("backup manifest does not declare a safe MariaDB data volume")
	}
	if manifest.Volumes == nil {
		manifest.Volumes = map[string]string{}
	}
	if manifest.Checksums == nil {
		return siteBackupManifest{}, fmt.Errorf("backup manifest does not contain artifact checksums")
	}
	archives := []string{manifest.Database}
	for _, archive := range manifest.Volumes {
		archives = append(archives, archive)
	}
	for _, archive := range archives {
		checksum := strings.ToLower(strings.TrimSpace(manifest.Checksums[archive]))
		if !sha256HexPattern.MatchString(checksum) {
			return siteBackupManifest{}, fmt.Errorf("backup manifest does not contain a valid SHA-256 checksum for %q", archive)
		}
		manifest.Checksums[archive] = checksum
	}
	sort.Strings(manifest.ExternalVolumes)
	sort.Strings(manifest.ExcludedStorage)
	return manifest, nil
}

func buildSiteRestorePlan(ctx *config.Context, compose composeSecretConfig, manifest siteBackupManifest, backupDir, identifier string) (siteRestorePlan, error) {
	image := strings.TrimSpace(compose.Services["init"].Image)
	if image == "" {
		return siteRestorePlan{}, fmt.Errorf("compose init service must declare an image for restore tooling")
	}
	if _, _, err := validateBackupContainerInputs(image, backupDir); err != nil {
		return siteRestorePlan{}, err
	}
	databaseService := firstNonBlank(ctx.DatabaseService, defaultMariaDBService)
	databaseSources, err := databaseDataVolumeSources(compose, databaseService)
	if err != nil {
		return siteRestorePlan{}, err
	}
	if len(databaseSources) != 1 {
		return siteRestorePlan{}, fmt.Errorf("MariaDB service %q must have exactly one writable named data volume for transactional restore", databaseService)
	}
	var databaseLogical string
	for source := range databaseSources {
		databaseLogical = source
	}
	databaseDeclaration, ok := compose.Volumes[databaseLogical]
	if !ok {
		return siteRestorePlan{}, fmt.Errorf("MariaDB data volume %q is not declared by current Compose config", databaseLogical)
	}
	if databaseDeclaration.External {
		return siteRestorePlan{}, fmt.Errorf("refusing destructive restore of external MariaDB volume %q", databaseLogical)
	}
	if err := validateComposeOwnedVolume(databaseLogical, databaseDeclaration); err != nil {
		return siteRestorePlan{}, err
	}
	if manifest.DatabaseVolume != databaseLogical {
		return siteRestorePlan{}, fmt.Errorf("MariaDB Compose volume changed since backup (backup: %q; current: %q)", manifest.DatabaseVolume, databaseLogical)
	}
	if currentDatabase := strings.TrimSpace(ctx.DatabaseName); currentDatabase == "" || currentDatabase != strings.TrimSpace(manifest.DatabaseName) {
		return siteRestorePlan{}, fmt.Errorf("backup database %q does not match active context database-name %q", manifest.DatabaseName, currentDatabase)
	}
	databaseActual, err := actualComposeVolumeName(compose, databaseLogical)
	if err != nil {
		return siteRestorePlan{}, err
	}

	plan := siteRestorePlan{
		image:      image,
		backupDir:  backupDir,
		identifier: identifier,
		database: siteDatabaseRestore{
			service:        databaseService,
			databaseName:   strings.TrimSpace(manifest.DatabaseName),
			logicalVolume:  databaseLogical,
			actualVolume:   databaseActual,
			archive:        manifest.Database,
			checksum:       manifest.Checksums[manifest.Database],
			recoveryVolume: restoreVolumeName(identifier, "database-recovery", 0),
		},
	}
	currentOwned := []string{}
	currentExternal := []string{}
	for _, logical := range sortedComposeVolumeNames(compose.Volumes) {
		if logical == databaseLogical {
			continue
		}
		if compose.Volumes[logical].External {
			descriptor, err := externalVolumeDescriptor(logical, compose.Volumes[logical])
			if err != nil {
				return siteRestorePlan{}, err
			}
			currentExternal = append(currentExternal, descriptor)
		} else {
			if err := validateComposeOwnedVolume(logical, compose.Volumes[logical]); err != nil {
				return siteRestorePlan{}, err
			}
			currentOwned = append(currentOwned, logical)
		}
	}
	manifestOwned := make([]string, 0, len(manifest.Volumes))
	for logical := range manifest.Volumes {
		if declaration, ok := compose.Volumes[logical]; ok && declaration.External {
			return siteRestorePlan{}, fmt.Errorf("refusing destructive restore of external Compose volume %q", logical)
		}
		manifestOwned = append(manifestOwned, logical)
	}
	sort.Strings(manifestOwned)
	manifestExternal := append([]string{}, manifest.ExternalVolumes...)
	sort.Strings(manifestExternal)
	if !equalStrings(currentOwned, manifestOwned) {
		return siteRestorePlan{}, fmt.Errorf("owned Compose volume set changed since backup (backup: %s; current: %s); refusing a mixed-state restore", strings.Join(manifestOwned, ", "), strings.Join(currentOwned, ", "))
	}
	if !equalStrings(currentExternal, manifestExternal) {
		return siteRestorePlan{}, fmt.Errorf("externally managed Compose volume set changed since backup (backup: %s; current: %s)", strings.Join(manifestExternal, ", "), strings.Join(currentExternal, ", "))
	}
	currentExcluded, err := fullSiteExcludedStorage(compose)
	if err != nil {
		return siteRestorePlan{}, err
	}
	manifestExcluded := append([]string{}, manifest.ExcludedStorage...)
	sort.Strings(manifestExcluded)
	if !equalStrings(currentExcluded, manifestExcluded) {
		return siteRestorePlan{}, fmt.Errorf("externally managed bind/storage mounts changed since backup (backup: %s; current: %s)", strings.Join(manifestExcluded, ", "), strings.Join(currentExcluded, ", "))
	}

	seenActual := map[string]string{databaseActual: databaseLogical}
	seenArchives := map[string]string{manifest.Database: "database"}
	logicalNames := make([]string, 0, len(manifest.Volumes))
	for logical := range manifest.Volumes {
		logicalNames = append(logicalNames, logical)
	}
	sort.Strings(logicalNames)
	for index, logical := range logicalNames {
		archive := manifest.Volumes[logical]
		if err := validateBackupArchiveName(archive); err != nil {
			return siteRestorePlan{}, fmt.Errorf("backup manifest contains unsafe archive path for volume %s: %w", logical, err)
		}
		if previous, duplicate := seenArchives[archive]; duplicate {
			return siteRestorePlan{}, fmt.Errorf("backup archive %q is reused by %s and volume %s", archive, previous, logical)
		}
		seenArchives[archive] = "volume " + logical
		volume, ok := compose.Volumes[logical]
		if !ok {
			return siteRestorePlan{}, fmt.Errorf("backup volume %s is not declared by current Compose config", logical)
		}
		if volume.External {
			return siteRestorePlan{}, fmt.Errorf("refusing destructive restore of external Compose volume %q", logical)
		}
		actual, err := actualComposeVolumeName(compose, logical)
		if err != nil {
			return siteRestorePlan{}, err
		}
		if previous, duplicate := seenActual[actual]; duplicate {
			return siteRestorePlan{}, fmt.Errorf("compose volumes %q and %q resolve to the same destructive restore target %q", previous, logical, actual)
		}
		seenActual[actual] = logical
		plan.volumes = append(plan.volumes, siteVolumeRestore{
			logical:        logical,
			actual:         actual,
			archive:        archive,
			checksum:       manifest.Checksums[archive],
			stageVolume:    restoreVolumeName(identifier, "stage", index+1),
			recoveryVolume: restoreVolumeName(identifier, "recovery", index+1),
		})
	}
	return plan, nil
}

func databaseDataVolumeSources(compose composeSecretConfig, service string) (map[string]bool, error) {
	declared, ok := compose.Services[service]
	if !ok {
		return nil, fmt.Errorf("MariaDB service %q is not declared by current Compose config", service)
	}
	named := make([]composeServiceVolume, 0)
	for _, volume := range declared.Volumes {
		if volume.ReadOnly {
			continue
		}
		target := path.Clean(strings.TrimSpace(volume.Target))
		switch volume.Type {
		case "tmpfs":
			continue
		case "volume":
			if strings.TrimSpace(volume.Source) == "" {
				return nil, fmt.Errorf("MariaDB service %q uses an unsupported writable anonymous volume at %q", service, volume.Target)
			}
			if target != supportedMariaDBDataTarget {
				return nil, fmt.Errorf("MariaDB service %q writable named volume %q targets %q; full-site restore supports only the data target %q", service, volume.Source, volume.Target, supportedMariaDBDataTarget)
			}
			named = append(named, volume)
		case "bind":
			return nil, fmt.Errorf("MariaDB service %q uses unsupported writable bind storage at %q; transactional rollback requires an owned named data volume", service, volume.Target)
		case "":
			return nil, fmt.Errorf("MariaDB service %q has writable storage at %q without a modeled mount type", service, volume.Target)
		default:
			return nil, fmt.Errorf("MariaDB service %q uses unsupported writable storage type %q at %q", service, volume.Type, volume.Target)
		}
	}
	if len(named) != 1 {
		return nil, fmt.Errorf("cannot safely identify one writable named MariaDB data volume for service %q", service)
	}
	return map[string]bool{named[0].Source: true}, nil
}

func validateComposeOwnedVolume(logical string, volume composeVolume) error {
	if !backupVolumeNamePattern.MatchString(logical) {
		return fmt.Errorf("owned Compose volume name %q is unsafe", logical)
	}
	if volume.External {
		return fmt.Errorf("compose volume %q is externally managed", logical)
	}
	driver := strings.TrimSpace(volume.Driver)
	if driver != "" && driver != "local" {
		return fmt.Errorf("owned Compose volume %q uses unsupported driver %q; full-site backup supports Docker-managed local volumes only", logical, driver)
	}
	if len(volume.DriverOpts) > 0 {
		return fmt.Errorf("owned Compose volume %q uses driver options and may be bind-backed or shared; refusing full-site backup", logical)
	}
	return nil
}

func fullSiteExcludedStorage(compose composeSecretConfig) ([]string, error) {
	services := make([]string, 0, len(compose.Services))
	for service := range compose.Services {
		services = append(services, service)
	}
	sort.Strings(services)
	excluded := []string{}
	for _, service := range services {
		if !backupVolumeNamePattern.MatchString(service) {
			return nil, fmt.Errorf("compose service name %q is unsafe for full-site backup", service)
		}
		for _, volume := range compose.Services[service].Volumes {
			if volume.ReadOnly {
				continue
			}
			target := strings.TrimSpace(volume.Target)
			if target == "" || !path.IsAbs(target) || strings.ContainsAny(target, "\x00\r\n") {
				return nil, fmt.Errorf("service %q declares unsafe writable storage target %q", service, target)
			}
			switch volume.Type {
			case "volume":
				if strings.TrimSpace(volume.Source) == "" {
					return nil, fmt.Errorf("service %q uses writable anonymous volume at %q; full-site backup requires named modeled storage", service, target)
				}
				if _, ok := compose.Volumes[volume.Source]; !ok {
					return nil, fmt.Errorf("service %q uses unmodeled named volume %q at %q", service, volume.Source, target)
				}
			case "bind":
				source := strings.TrimSpace(volume.Source)
				if source == "" || strings.ContainsAny(source, "\x00\r\n") {
					return nil, fmt.Errorf("service %q has writable bind storage at %q without a safe source", service, target)
				}
				excluded = append(excluded, "bind:"+service+":"+target+":source-sha256:"+storageIdentityDigest(source))
			case "tmpfs":
				// tmpfs state is intentionally ephemeral and cannot survive a site restart.
			case "":
				return nil, fmt.Errorf("service %q has writable storage at %q without a modeled mount type", service, target)
			default:
				if strings.TrimSpace(volume.Type) != "" {
					excluded = append(excluded, volume.Type+":"+service+":"+target+":source-sha256:"+storageIdentityDigest(strings.TrimSpace(volume.Source)))
				}
			}
		}
	}
	sort.Strings(excluded)
	return uniqueStrings(excluded), nil
}

func externalVolumeDescriptor(logical string, volume composeVolume) (string, error) {
	if !backupVolumeNamePattern.MatchString(logical) {
		return "", fmt.Errorf("external Compose volume name %q is unsafe", logical)
	}
	actual := strings.TrimSpace(volume.Name)
	if actual == "" {
		actual = logical
	}
	if !backupVolumeNamePattern.MatchString(actual) {
		return "", fmt.Errorf("external Compose volume %q resolves to unsafe Docker volume name %q", logical, actual)
	}
	return "volume:" + logical + ":name-sha256:" + storageIdentityDigest(actual), nil
}

func storageIdentityDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func actualComposeVolumeName(compose composeSecretConfig, logical string) (string, error) {
	volume, ok := compose.Volumes[logical]
	if !ok {
		return "", fmt.Errorf("compose volume %q is not declared", logical)
	}
	actual := strings.TrimSpace(volume.Name)
	if actual == "" {
		actual = strings.TrimSpace(compose.Name) + "_" + logical
	}
	if !backupVolumeNamePattern.MatchString(actual) {
		return "", fmt.Errorf("compose volume %q resolves to unsafe Docker volume name %q", logical, actual)
	}
	return actual, nil
}

func sortedComposeVolumeNames(volumes map[string]composeVolume) []string {
	names := make([]string, 0, len(volumes))
	for name := range volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newRestoreIdentifier() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func restoreVolumeName(identifier, role string, index int) string {
	return fmt.Sprintf("sitectl-restore-%s-%s-%03d", identifier, role, index)
}

func backupVolumeArchiveName(logical string) string {
	digest := sha256.Sum256([]byte(logical))
	return "volume-" + sanitizeArtifactPart(logical) + "-" + hex.EncodeToString(digest[:16]) + ".tar.gz"
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func safeProjectRelativePath(ctx *config.Context, value string) (string, error) {
	if strings.ContainsAny(value, ":\x00\r\n") {
		return "", fmt.Errorf("backup path cannot contain a colon or control characters")
	}
	clean := filepath.Clean(strings.TrimSpace(value))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backup path must be a project-relative directory")
	}
	return filepath.Join(ctx.ProjectDir, clean), nil
}

func createPrivateBackupDirectory(runCtx context.Context, ctx *config.Context, backupDir string) error {
	parent := filepath.Dir(backupDir)
	if _, err := ctx.RunCommandContext(runCtx, exec.Command("mkdir", "-m", "0700", "-p", "--", parent)); err != nil { // #nosec G204 -- parent is a canonical, project-contained path passed to fixed mkdir.
		return fmt.Errorf("create backup parent directory: %w", err)
	}
	if _, err := ctx.RunCommandContext(runCtx, exec.Command("mkdir", "-m", "0700", "--", backupDir)); err != nil { // #nosec G204 -- exclusive mkdir prevents reusing a raced or preexisting transaction directory.
		return fmt.Errorf("create private backup directory: %w", err)
	}
	if _, err := ctx.RunCommandContext(runCtx, exec.Command("chmod", "0700", "--", backupDir)); err != nil { // #nosec G204 -- backupDir is a canonical, project-contained path.
		return fmt.Errorf("secure backup directory: %w", err)
	}
	return nil
}

func resolveProjectBackupPath(runCtx context.Context, ctx *config.Context, value string, mustExist bool) (string, error) {
	candidate, err := safeProjectRelativePath(ctx, value)
	if err != nil {
		return "", err
	}
	project, err := contextRealpath(runCtx, ctx, true, ctx.ProjectDir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	resolved, err := contextRealpath(runCtx, ctx, mustExist, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve backup directory: %w", err)
	}
	relative, err := filepath.Rel(project, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("backup directory %q resolves outside the project", value)
	}
	return resolved, nil
}

func contextRealpath(runCtx context.Context, ctx *config.Context, mustExist bool, value string) (string, error) {
	mode := "-m"
	if mustExist {
		mode = "-e"
	}
	output, err := ctx.RunQuietCommandContext(runCtx, exec.Command("realpath", mode, "--", value)) // #nosec G204 -- the path is a distinct argument to fixed realpath.
	if err != nil {
		return "", err
	}
	resolved := strings.TrimSpace(output)
	if resolved == "" || !filepath.IsAbs(resolved) || strings.ContainsAny(resolved, "\x00\r\n") {
		return "", fmt.Errorf("realpath returned an invalid path")
	}
	return filepath.Clean(resolved), nil
}

func resolveContainedBackupFile(runCtx context.Context, ctx *config.Context, backupDir, filename string) (string, error) {
	if strings.TrimSpace(filename) == "" || strings.ContainsAny(filename, "\x00\r\n") {
		return "", fmt.Errorf("backup filename is unsafe")
	}
	resolved, err := contextRealpath(runCtx, ctx, true, filepath.Join(backupDir, filename))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(backupDir, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("backup file %q resolves outside backup directory", filename)
	}
	if _, err := ctx.RunQuietCommandContext(runCtx, exec.Command("test", "-f", resolved)); err != nil { // #nosec G204 -- resolved is a canonical absolute path passed to fixed test.
		return "", fmt.Errorf("backup artifact %q is not a regular file: %w", filename, err)
	}
	return resolved, nil
}

func secureContextFile(runCtx context.Context, ctx *config.Context, filename string) error {
	_, err := ctx.RunCommandContext(runCtx, exec.Command("chmod", "0600", "--", filename)) // #nosec G204 -- filename is a canonical project-contained path passed to fixed chmod.
	return err
}

func contextFileSHA256(runCtx context.Context, ctx *config.Context, filename string) (string, error) {
	output, err := ctx.RunQuietCommandContext(runCtx, exec.Command("sha256sum", "--", filename)) // #nosec G204 -- filename is a canonical project-contained path passed to fixed sha256sum.
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) < 1 {
		return "", fmt.Errorf("sha256sum returned no checksum")
	}
	checksum := strings.ToLower(strings.TrimSpace(fields[0]))
	if !sha256HexPattern.MatchString(checksum) {
		return "", fmt.Errorf("sha256sum returned invalid checksum %q", checksum)
	}
	return checksum, nil
}

func backupContainerDockerArgs(image, volume, backupDir, archive string, restore bool) ([][]string, error) {
	image, backupDir, err := validateBackupContainerInputs(image, backupDir)
	if err != nil {
		return nil, err
	}
	if !backupVolumeNamePattern.MatchString(volume) {
		return nil, fmt.Errorf("backup volume %q must be a named Docker volume", volume)
	}
	if err := validateBackupArchiveName(archive); err != nil {
		return nil, err
	}
	archivePath := path.Join("/backup", archive)
	if !restore {
		return [][]string{{
			"run", "--rm", "--entrypoint", "tar",
			"-v", volume + ":/source:ro",
			"-v", backupDir + ":/backup:rw",
			image, "--hard-dereference", "-C", "/source", "-czf", archivePath, ".",
		}}, nil
	}
	return [][]string{
		{
			"run", "--rm", "--entrypoint", "find",
			"-v", volume + ":/source:rw",
			"-v", backupDir + ":/backup:ro",
			image, "/source", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "--", "{}", "+",
		},
		{
			"run", "--rm", "--entrypoint", "tar",
			"-v", volume + ":/source:rw",
			"-v", backupDir + ":/backup:ro",
			image, "--extract", "--gzip", "--file", archivePath, "--directory", "/source", "--numeric-owner", "--delay-directory-restore",
		},
	}, nil
}

func backupArchiveCheckDockerArgs(image, backupDir, archive string, database bool) ([]string, error) {
	image, backupDir, err := validateBackupContainerInputs(image, backupDir)
	if err != nil {
		return nil, err
	}
	if err := validateBackupArchiveName(archive); err != nil {
		return nil, err
	}
	entrypoint := "tar"
	command := []string{"-tzf", path.Join("/backup", archive)}
	if database {
		entrypoint = "gzip"
		command = []string{"-t", path.Join("/backup", archive)}
	}
	args := []string{"run", "--rm", "--entrypoint", entrypoint, "-v", backupDir + ":/backup:ro", image}
	return append(args, command...), nil
}

func validateBackupContainerInputs(image, backupDir string) (string, string, error) {
	image = strings.TrimSpace(image)
	if image == "" || strings.HasPrefix(image, "-") || strings.ContainsAny(image, "\x00\r\n") {
		return "", "", fmt.Errorf("backup image must be a non-empty safe value")
	}
	backupDir = strings.TrimSpace(backupDir)
	if backupDir == "" || !filepath.IsAbs(backupDir) || strings.ContainsAny(backupDir, ":\x00\r\n") {
		return "", "", fmt.Errorf("backup directory must be a non-empty absolute safe path")
	}
	return image, backupDir, nil
}

func validateBackupArchiveName(archive string) error {
	if archive == "" || archive == "." || archive == ".." || strings.HasPrefix(archive, "-") || strings.ContainsAny(archive, "/\\\x00\r\n") {
		return fmt.Errorf("backup archive must be a safe filename")
	}
	return nil
}

func stringsExcept(values []string, excluded string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != excluded {
			result = append(result, value)
		}
	}
	return result
}
