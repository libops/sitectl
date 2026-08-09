package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

func TestBackupContainerDockerArgsUsesDirectTarInvocation(t *testing.T) {
	t.Parallel()

	archive := "volume-files;touch-not-executed.tar.gz"
	got, err := backupContainerDockerArgs("example/init:1", "site_files", "/srv/site/backups/one", archive, false)
	if err != nil {
		t.Fatalf("backupContainerDockerArgs() error = %v", err)
	}
	want := [][]string{{
		"run", "--rm", "--entrypoint", "tar",
		"-v", "site_files:/source:ro",
		"-v", "/srv/site/backups/one:/backup:rw",
		"example/init:1", "--hard-dereference", "-C", "/source", "-czf", "/backup/" + archive, ".",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backup container args = %#v, want %#v", got, want)
	}
}

func TestBackupContainerDockerArgsRestoresInOrderedDirectSteps(t *testing.T) {
	t.Parallel()

	got, err := backupContainerDockerArgs("example/init:1", "site_files", "/srv/site/backups/one", "volume-files.tar.gz", true)
	if err != nil {
		t.Fatalf("backupContainerDockerArgs() error = %v", err)
	}
	want := [][]string{
		{
			"run", "--rm", "--entrypoint", "find",
			"-v", "site_files:/source:rw",
			"-v", "/srv/site/backups/one:/backup:ro",
			"example/init:1", "/source", "-mindepth", "1", "-maxdepth", "1", "-exec", "rm", "-rf", "--", "{}", "+",
		},
		{
			"run", "--rm", "--entrypoint", "tar",
			"-v", "site_files:/source:rw",
			"-v", "/srv/site/backups/one:/backup:ro",
			"example/init:1", "--extract", "--gzip", "--file", "/backup/volume-files.tar.gz", "--directory", "/source", "--numeric-owner", "--delay-directory-restore",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restore container args = %#v, want %#v", got, want)
	}
	for _, args := range got {
		for _, arg := range args {
			if arg == "sh" || arg == "-c" || strings.Contains(arg, "&&") {
				t.Fatalf("restore command contains a shell program: %#v", args)
			}
		}
	}
}

func TestBackupContainerDockerArgsRejectsUnsafeArchiveNames(t *testing.T) {
	t.Parallel()

	for _, archive := range []string{"", ".", "..", "../volume.tar.gz", "/tmp/volume.tar.gz", "-checkpoint-action=exec=sh", "volume\n.tar.gz", "volume\x00.tar.gz"} {
		t.Run(archive, func(t *testing.T) {
			if _, err := backupContainerDockerArgs("example/init:1", "site_files", "/srv/site/backups/one", archive, true); err == nil {
				t.Fatalf("backupContainerDockerArgs() accepted unsafe archive %q", archive)
			}
		})
	}
}

func TestBackupContainerDockerArgsRejectsBindMountVolumeSources(t *testing.T) {
	t.Parallel()

	for _, volume := range []string{"", "/", "/srv/site/files", "./files", "files:/host", "files;touch-not-executed"} {
		t.Run(volume, func(t *testing.T) {
			if _, err := backupContainerDockerArgs("example/init:1", volume, "/srv/site/backups/one", "volume.tar.gz", false); err == nil {
				t.Fatalf("backupContainerDockerArgs() accepted unsafe volume source %q", volume)
			}
		})
	}
}

func TestBackupArchiveCheckDockerArgsValidatesBeforeDestructiveRestore(t *testing.T) {
	t.Parallel()

	database, err := backupArchiveCheckDockerArgs("example/init:1", "/srv/site/backups/one", "mariadb.sql.gz", true)
	if err != nil {
		t.Fatalf("backupArchiveCheckDockerArgs(database) error = %v", err)
	}
	wantDatabase := []string{
		"run", "--rm", "--entrypoint", "gzip", "-v", "/srv/site/backups/one:/backup:ro", "example/init:1",
		"-t", "/backup/mariadb.sql.gz",
	}
	if !reflect.DeepEqual(database, wantDatabase) {
		t.Fatalf("database archive check = %v, want %v", database, wantDatabase)
	}

	volume, err := backupArchiveCheckDockerArgs("example/init:1", "/srv/site/backups/one", "volume-files.tar.gz", false)
	if err != nil {
		t.Fatalf("backupArchiveCheckDockerArgs(volume) error = %v", err)
	}
	wantVolume := []string{
		"run", "--rm", "--entrypoint", "tar", "-v", "/srv/site/backups/one:/backup:ro", "example/init:1",
		"-tzf", "/backup/volume-files.tar.gz",
	}
	if !reflect.DeepEqual(volume, wantVolume) {
		t.Fatalf("volume archive check = %v, want %v", volume, wantVolume)
	}
}

func TestBackupContainerInputsRejectControlCharacters(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		image     string
		backupDir string
	}{
		{name: "image newline", image: "example/init:1\n--privileged", backupDir: "/srv/site/backups/one"},
		{name: "directory newline", image: "example/init:1", backupDir: "/srv/site/backups/one\n/etc"},
		{name: "directory volume separator", image: "example/init:1", backupDir: "/srv/site/backups/one:ro"},
		{name: "option image", image: "--privileged", backupDir: "/srv/site/backups/one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := validateBackupContainerInputs(tc.image, tc.backupDir); err == nil {
				t.Fatal("expected unsafe container input to fail")
			}
		})
	}
}

func TestFullSiteBackupHostCapabilityGateIsExplicit(t *testing.T) {
	t.Parallel()

	if err := requireFullSiteBackupHostSupport("linux"); err != nil {
		t.Fatalf("Linux capability gate error = %v", err)
	}
	if err := requireFullSiteBackupHostSupport("windows"); err == nil || !strings.Contains(err.Error(), "Linux operator host") {
		t.Fatalf("Windows capability gate error = %v, want explicit Linux requirement", err)
	}
}

func TestStagedArtifactDockerArgsUseReadOnlyBackupAndFixedEntrypoints(t *testing.T) {
	t.Parallel()

	copyArgs, checkArgs, err := stagedArtifactDockerArgs(
		"example/init:1",
		"/srv/site/backups/one",
		"volume-files.tar.gz",
		"sitectl-stage-001",
		false,
	)
	if err != nil {
		t.Fatalf("stagedArtifactDockerArgs() error = %v", err)
	}
	for name, args := range map[string][]string{"copy": copyArgs, "check": checkArgs} {
		if !containsArgument(args, "/srv/site/backups/one:/backup:ro") {
			t.Fatalf("%s args do not mount /backup read-only: %v", name, args)
		}
		if containsArgument(args, "/srv/site/backups/one:/backup:rw") {
			t.Fatalf("%s args mount /backup writable: %v", name, args)
		}
		if entrypoint := argumentAfter(args, "--entrypoint"); entrypoint == "" {
			t.Fatalf("%s args do not override the image entrypoint: %v", name, args)
		}
	}
	if got := argumentAfter(copyArgs, "--entrypoint"); got != "cp" {
		t.Fatalf("copy entrypoint = %q, want cp", got)
	}
	if got := argumentAfter(checkArgs, "--entrypoint"); got != "tar" {
		t.Fatalf("check entrypoint = %q, want tar", got)
	}
}

func TestStagedArtifactChecksumDockerArgsVerifyFrozenBytes(t *testing.T) {
	t.Parallel()

	args, err := stagedArtifactChecksumDockerArgs("example/init:1", "volume-files.tar.gz", "sitectl-stage-001")
	if err != nil {
		t.Fatalf("stagedArtifactChecksumDockerArgs() error = %v", err)
	}
	if got := argumentAfter(args, "--entrypoint"); got != "sha256sum" {
		t.Fatalf("staged checksum entrypoint = %q, want sha256sum", got)
	}
	if !containsArgument(args, "sitectl-stage-001:/staged:ro") || containsArgument(args, "sitectl-stage-001:/staged:rw") {
		t.Fatalf("staged checksum does not use a read-only frozen volume: %v", args)
	}
}

func TestNamedBackupHelperArgsKeepsCancelableContainerForOwnedCleanup(t *testing.T) {
	t.Parallel()

	identifier := strings.Repeat("a", 32)
	args, err := namedBackupHelperArgs(
		[]string{"run", "--rm", "--entrypoint", "tar", "example/init:1", "-tf", "/staged/files.tar"},
		"sitectl-backup-helper-"+identifier,
		identifier,
	)
	if err != nil {
		t.Fatalf("namedBackupHelperArgs() error = %v", err)
	}
	if containsArgument(args, "--rm") {
		t.Fatalf("named helper would disappear before cancellation cleanup: %v", args)
	}
	if argumentAfter(args, "--name") != "sitectl-backup-helper-"+identifier {
		t.Fatalf("named helper args = %v", args)
	}
	if argumentAfter(args, "--label") != siteBackupHelperLabel+"="+identifier {
		t.Fatalf("helper ownership label missing from %v", args)
	}
}

func TestBuildSiteRestorePlanRejectsExternalFileVolume(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	volume := compose.Volumes["files"]
	volume.External = true
	compose.Volumes["files"] = volume
	_, err := buildSiteRestorePlan(testRestoreContext(), compose, testRestoreManifest(), "/srv/site/backups/one", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "external Compose volume") {
		t.Fatalf("buildSiteRestorePlan() error = %v, want external volume refusal", err)
	}
}

func TestBuildSiteRestorePlanRejectsExternalMariaDBVolume(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	volume := compose.Volumes["database"]
	volume.External = true
	compose.Volumes["database"] = volume
	_, err := buildSiteRestorePlan(testRestoreContext(), compose, testRestoreManifest(), "/srv/site/backups/one", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "external MariaDB volume") {
		t.Fatalf("buildSiteRestorePlan() error = %v, want external MariaDB refusal", err)
	}
}

func TestDatabaseDataVolumeSourcesRequiresUnambiguousWritableDataVolume(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["mariadb"] = composeService{Volumes: []composeServiceVolume{
		{Type: "volume", Source: "database_one", Target: "/data/one"},
		{Type: "volume", Source: "database_two", Target: "/data/two"},
	}}
	if _, err := databaseDataVolumeSources(compose, "mariadb"); err == nil {
		t.Fatal("databaseDataVolumeSources() accepted ambiguous database volumes")
	}
}

func TestDatabaseDataVolumeSourcesRejectsExtraWritableNamedVolume(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["mariadb"] = composeService{Volumes: []composeServiceVolume{
		{Type: "volume", Source: "database", Target: "/var/lib/mysql"},
		{Type: "volume", Source: "database_logs", Target: "/var/log/mysql"},
	}}
	if _, err := databaseDataVolumeSources(compose, "mariadb"); err == nil {
		t.Fatal("databaseDataVolumeSources() accepted an extra writable MariaDB volume")
	}
}

func TestDatabaseDataVolumeSourcesRejectsBindBackedDatabaseData(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["mariadb"] = composeService{Volumes: []composeServiceVolume{
		{Type: "volume", Source: "database_logs", Target: supportedMariaDBDataTarget},
		{Type: "bind", Source: "/srv/customer/mysql", Target: supportedMariaDBDataTarget},
	}}
	if _, err := databaseDataVolumeSources(compose, "mariadb"); err == nil || !strings.Contains(err.Error(), "writable bind") {
		t.Fatalf("databaseDataVolumeSources() error = %v, want bind-backed database refusal", err)
	}
}

func TestValidateComposeOwnedVolumeRejectsCustomStorageDrivers(t *testing.T) {
	t.Parallel()

	for _, volume := range []composeVolume{
		{Driver: "nfs"},
		{Driver: "local", DriverOpts: map[string]string{"type": "none", "device": "/srv/customer", "o": "bind"}},
	} {
		if err := validateComposeOwnedVolume("files", volume); err == nil {
			t.Fatalf("validateComposeOwnedVolume(%#v) accepted shared or bind-backed storage", volume)
		}
	}
	if err := validateComposeOwnedVolume("files", composeVolume{Driver: "local"}); err != nil {
		t.Fatalf("validateComposeOwnedVolume(local) error = %v", err)
	}
}

func TestFullSiteExcludedStorageRejectsWritableAnonymousVolume(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["web"] = composeService{Volumes: []composeServiceVolume{{
		Type: "volume", Target: "/var/lib/app", ReadOnly: false,
	}}}
	if _, err := fullSiteExcludedStorage(compose); err == nil || !strings.Contains(err.Error(), "anonymous volume") {
		t.Fatalf("fullSiteExcludedStorage() error = %v, want anonymous volume refusal", err)
	}
}

func TestFullSiteExcludedStorageRecordsWritableBindWithoutSourcePath(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["web"] = composeService{Volumes: []composeServiceVolume{{
		Type: "bind", Source: "/institution/private/path", Target: "/var/www/site/files",
	}}}
	got, err := fullSiteExcludedStorage(compose)
	if err != nil {
		t.Fatalf("fullSiteExcludedStorage() error = %v", err)
	}
	want := []string{"bind:web:/var/www/site/files:source-sha256:" + storageIdentityDigest("/institution/private/path")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fullSiteExcludedStorage() = %v, want %v", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "/institution/private/path") {
		t.Fatalf("excluded storage metadata exposed host source path: %v", got)
	}
}

func TestFullSiteExcludedStorageDetectsChangedBindSourceWithoutExposingIt(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["web"] = composeService{Volumes: []composeServiceVolume{{
		Type: "bind", Source: "/institution/first", Target: "/var/www/site/files",
	}}}
	first, err := fullSiteExcludedStorage(compose)
	if err != nil {
		t.Fatal(err)
	}
	service := compose.Services["web"]
	service.Volumes[0].Source = "/institution/second"
	compose.Services["web"] = service
	second, err := fullSiteExcludedStorage(compose)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(first, second) {
		t.Fatalf("changed bind source produced the same descriptor: %v", first)
	}
	if strings.Contains(strings.Join(append(first, second...), " "), "/institution/") {
		t.Fatalf("excluded storage descriptors exposed source paths: %v %v", first, second)
	}
}

func TestFullSiteExcludedStorageRejectsUnmodeledWritableMountType(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["web"] = composeService{Volumes: []composeServiceVolume{{Target: "/var/lib/app"}}}
	if _, err := fullSiteExcludedStorage(compose); err == nil || !strings.Contains(err.Error(), "without a modeled mount type") {
		t.Fatalf("fullSiteExcludedStorage() error = %v, want unmodeled mount refusal", err)
	}
}

func TestBuildSiteRestorePlanRejectsBindSourceIdentityDrift(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["web"] = composeService{Volumes: []composeServiceVolume{{
		Type: "bind", Source: "/institution/first", Target: "/var/www/site/files",
	}}}
	manifest := testRestoreManifest()
	excluded, err := fullSiteExcludedStorage(compose)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ExcludedStorage = excluded
	service := compose.Services["web"]
	service.Volumes[0].Source = "/institution/second"
	compose.Services["web"] = service
	_, err = buildSiteRestorePlan(testRestoreContext(), compose, manifest, "/srv/site/backups/one", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "bind/storage mounts changed") {
		t.Fatalf("buildSiteRestorePlan() error = %v, want bind source identity drift refusal", err)
	}
}

func TestWithQuiescedSiteWritersResumesExactServicesAfterFailure(t *testing.T) {
	t.Parallel()

	operations := newFakeSiteBackupOperations()
	operations.running = []string{"worker", "mariadb", "web"}
	backupErr := errors.New("archive failed")
	err := withQuiescedSiteWriters(context.Background(), operations, "mariadb", func() error {
		operations.events = append(operations.events, "backup-body")
		return backupErr
	})
	if !errors.Is(err, backupErr) {
		t.Fatalf("withQuiescedSiteWriters() error = %v, want backup failure", err)
	}
	want := []string{
		"running-services",
		"compose:stop --timeout 120 worker web",
		"backup-body",
		"compose:start worker web",
	}
	if !reflect.DeepEqual(operations.events, want) {
		t.Fatalf("quiesce/resume events = %v, want %v", operations.events, want)
	}
}

func TestWithQuiescedSiteWritersSurfacesResumeFailure(t *testing.T) {
	t.Parallel()

	operations := newFakeSiteBackupOperations()
	operations.running = []string{"mariadb", "web"}
	operations.failOnce["compose:start web"] = 1
	err := withQuiescedSiteWriters(context.Background(), operations, "mariadb", func() error {
		return errors.New("backup failed")
	})
	if err == nil || !strings.Contains(err.Error(), "resume application services") || !strings.Contains(err.Error(), "backup failed") {
		t.Fatalf("withQuiescedSiteWriters() error = %v, want backup and resume failures", err)
	}
}

func TestWithQuiescedSiteWritersResumesAfterPartialStopFailure(t *testing.T) {
	t.Parallel()

	operations := newFakeSiteBackupOperations()
	operations.running = []string{"mariadb", "web", "worker"}
	operations.failOnce["compose:stop --timeout 120 web worker"] = 1
	err := withQuiescedSiteWriters(context.Background(), operations, "mariadb", func() error {
		t.Fatal("backup callback ran after stop failure")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "quiesce") {
		t.Fatalf("withQuiescedSiteWriters() error = %v, want stop failure", err)
	}
	if eventIndex(operations.events, "compose:start web worker") < 0 {
		t.Fatalf("partial stop failure did not resume recorded writers: %v", operations.events)
	}
}

func TestBackupVolumeArchiveNameCannotCollideAfterSanitizing(t *testing.T) {
	t.Parallel()

	first := backupVolumeArchiveName("foo.bar")
	second := backupVolumeArchiveName("foo-bar")
	if first == second {
		t.Fatalf("collision-resistant archive names collided: %q", first)
	}
	if first != backupVolumeArchiveName("foo.bar") {
		t.Fatalf("backupVolumeArchiveName() is not deterministic: %q", first)
	}
}

func TestBuildSiteRestorePlanRejectsOwnedVolumeSetDrift(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Volumes["new_cache"] = composeVolume{Name: "site_new_cache"}
	_, err := buildSiteRestorePlan(testRestoreContext(), compose, testRestoreManifest(), "/srv/site/backups/one", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "owned Compose volume set changed") {
		t.Fatalf("buildSiteRestorePlan() error = %v, want owned volume drift refusal", err)
	}
}

func TestBuildSiteRestorePlanRejectsMariaDBVolumeIdentityDrift(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	compose.Services["mariadb"] = composeService{Volumes: []composeServiceVolume{{
		Type: "volume", Source: "replacement_database", Target: "/var/lib/mysql",
	}}}
	delete(compose.Volumes, "database")
	compose.Volumes["replacement_database"] = composeVolume{Name: "site_replacement_database"}
	_, err := buildSiteRestorePlan(testRestoreContext(), compose, testRestoreManifest(), "/srv/site/backups/one", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "MariaDB Compose volume changed") {
		t.Fatalf("buildSiteRestorePlan() error = %v, want MariaDB volume drift refusal", err)
	}
}

func TestExternalVolumeDescriptorDetectsResolvedIdentityDrift(t *testing.T) {
	t.Parallel()

	first, err := externalVolumeDescriptor("shared_files", composeVolume{Name: "institution-files-one", External: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := externalVolumeDescriptor("shared_files", composeVolume{Name: "institution-files-two", External: true})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("different external Docker volumes produced the same descriptor: %q", first)
	}
	if strings.Contains(first+second, "institution-files") {
		t.Fatalf("external storage descriptor exposed resolved volume names: %q %q", first, second)
	}
}

func TestBuildSiteRestorePlanRejectsExternalVolumeIdentityDrift(t *testing.T) {
	t.Parallel()

	compose := testRestoreComposeConfig()
	files := compose.Volumes["files"]
	files.External = true
	files.Name = "institution-files-one"
	compose.Volumes["files"] = files
	manifest := testRestoreManifest()
	delete(manifest.Volumes, "files")
	delete(manifest.Checksums, "volume-files.tar.gz")
	descriptor, err := externalVolumeDescriptor("files", files)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ExternalVolumes = []string{descriptor}

	files.Name = "institution-files-two"
	compose.Volumes["files"] = files
	_, err = buildSiteRestorePlan(testRestoreContext(), compose, manifest, "/srv/site/backups/one", "abcdef")
	if err == nil || !strings.Contains(err.Error(), "externally managed Compose volume set changed") {
		t.Fatalf("buildSiteRestorePlan() error = %v, want external volume identity drift refusal", err)
	}
}

func TestSecureCreatedArtifactDockerArgsUseFixedEntrypoints(t *testing.T) {
	t.Parallel()

	commands, err := secureCreatedArtifactDockerArgs("example/init:1", "/srv/site/backups/one", "files.tar.gz", "1000", "1000")
	if err != nil {
		t.Fatalf("secureCreatedArtifactDockerArgs() error = %v", err)
	}
	if len(commands) != 2 || argumentAfter(commands[0], "--entrypoint") != "chown" || argumentAfter(commands[1], "--entrypoint") != "chmod" {
		t.Fatalf("secure artifact commands do not use fixed entrypoints: %v", commands)
	}
	for _, args := range commands {
		if !containsArgument(args, "/srv/site/backups/one:/backup:rw") {
			t.Fatalf("creation permission command did not mount backup directory rw: %v", args)
		}
	}
}

func TestExtractStagedVolumeUsesReadOnlyValidatedSourceAndSafeFlags(t *testing.T) {
	t.Parallel()

	args, err := extractStagedVolumeDockerArgs("example/init:1", "files-stage", "files.tar.gz", "site_files")
	if err != nil {
		t.Fatalf("extractStagedVolumeDockerArgs() error = %v", err)
	}
	for _, wanted := range []string{"files-stage:/staged:ro", "site_files:/target:rw", "--numeric-owner", "--delay-directory-restore"} {
		if !containsArgument(args, wanted) {
			t.Fatalf("safe extraction args %v do not contain %q", args, wanted)
		}
	}
	if containsArgument(args, "--no-overwrite-dir") {
		t.Fatalf("restore must apply archived volume-root metadata: %v", args)
	}
	if got := argumentAfter(args, "--entrypoint"); got != "tar" {
		t.Fatalf("safe extraction entrypoint = %q, want tar", got)
	}
}

func TestRestoreSnapshotUsesReadOnlyRecoveryAndNumericOwnership(t *testing.T) {
	t.Parallel()

	args, err := restoreSnapshotDockerArgs("example/init:1", "files-recovery", "site_files")
	if err != nil {
		t.Fatalf("restoreSnapshotDockerArgs() error = %v", err)
	}
	for _, wanted := range []string{"files-recovery:/recovery:ro", "site_files:/target:rw", "--numeric-owner", "--delay-directory-restore"} {
		if !containsArgument(args, wanted) {
			t.Fatalf("safe recovery extraction args %v do not contain %q", args, wanted)
		}
	}
	if containsArgument(args, "--no-overwrite-dir") {
		t.Fatalf("rollback must apply snapshotted volume-root metadata: %v", args)
	}
	if got := argumentAfter(args, "--entrypoint"); got != "tar" {
		t.Fatalf("recovery extraction entrypoint = %q, want tar", got)
	}
}

func TestExecuteSiteRestoreStagesAndSnapshotsBeforeDestructiveCommit(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	cmd := testBackupCommand()
	if err := executeSiteRestore(cmd, operations, testSiteRestorePlan()); err != nil {
		t.Fatalf("executeSiteRestore() error = %v", err)
	}

	down := eventIndex(operations.events, "compose:down --remove-orphans --timeout 120")
	stageFiles := eventIndex(operations.events, "stage:files-stage")
	snapshotFiles := eventIndex(operations.events, "snapshot:site_files->files-recovery")
	snapshotDatabase := eventIndex(operations.events, "snapshot:site_database->database-recovery")
	clearFiles := eventIndex(operations.events, "clear:site_files")
	clearDatabase := eventIndex(operations.events, "clear:site_database")
	waitDatabase := eventIndex(operations.events, "wait-database:mariadb")
	revalidateDatabase := lastEventIndex(operations.events, "validate-artifact:mariadb.sql.gz")
	importDatabase := eventIndex(operations.events, "restore-database:mariadb")
	for name, index := range map[string]int{
		"down": down, "stage files": stageFiles,
		"snapshot files": snapshotFiles, "snapshot database": snapshotDatabase,
		"clear files": clearFiles, "clear database": clearDatabase,
		"wait database": waitDatabase, "revalidate database": revalidateDatabase, "import database": importDatabase,
	} {
		if index < 0 {
			t.Fatalf("missing %s event in %v", name, operations.events)
		}
	}
	if stageFiles > down {
		t.Fatalf("artifacts were not staged before outage: %v", operations.events)
	}
	if snapshotFiles < down || snapshotDatabase < down || snapshotFiles > clearFiles || snapshotDatabase > clearFiles {
		t.Fatalf("current volumes were not fully snapshotted before commit: %v", operations.events)
	}
	if clearDatabase >= waitDatabase || waitDatabase >= revalidateDatabase || revalidateDatabase >= importDatabase {
		t.Fatalf("MariaDB restore did not clear fresh data, wait, revalidate frozen input, then import: %v", operations.events)
	}
	if operations.volumes["files-recovery"] || operations.volumes["database-recovery"] {
		t.Fatalf("successful restore retained recovery volumes: %#v", operations.volumes)
	}
}

func TestExecuteSiteRestoreRollsBackEveryDestructiveFailure(t *testing.T) {
	t.Parallel()

	failures := []string{
		"clear:site_files",
		"extract:files-stage->site_files",
		"clear:site_database",
		"wait-database:mariadb",
		"restore-database:mariadb",
		"compose:up --remove-orphans --wait --wait-timeout 600 -d",
	}
	for _, failure := range failures {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			operations := newFakeRestoreOperations()
			operations.failOnce[failure] = 1
			err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
			if err == nil || !strings.Contains(err.Error(), "rolled back and restarted") {
				t.Fatalf("executeSiteRestore() error = %v, want successful automatic rollback", err)
			}
			for _, wanted := range []string{
				"restore-snapshot:files-recovery->site_files",
				"restore-snapshot:database-recovery->site_database",
			} {
				if eventIndex(operations.events, wanted) < 0 {
					t.Fatalf("failure %q did not perform %q: %v", failure, wanted, operations.events)
				}
			}
			if countEvent(operations.events, "compose:up --remove-orphans --wait --wait-timeout 600 -d") < 1 {
				t.Fatalf("failure %q did not restart site: %v", failure, operations.events)
			}
		})
	}
}

func TestExecuteSiteRestoreRetainsRecoveryIdentifiersWhenRollbackFails(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	operations.failOnce["restore-database:mariadb"] = 1
	operations.failAlways["restore-snapshot:database-recovery->site_database"] = true
	err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
	if err == nil {
		t.Fatal("executeSiteRestore() unexpectedly succeeded")
	}
	for _, wanted := range []string{"files-recovery", "database-recovery", "retained recovery Docker volumes"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Fatalf("rollback failure error %q does not report %q", err, wanted)
		}
	}
	if !operations.volumes["files-recovery"] || !operations.volumes["database-recovery"] {
		t.Fatalf("rollback failure removed recovery volumes: %#v", operations.volumes)
	}
	if countEvent(operations.events, "compose:up --remove-orphans --wait --wait-timeout 600 -d") != 0 {
		t.Fatalf("rollback restarted the site after recovery failed: %v", operations.events)
	}
}

func TestExecuteSiteRestoreDoesNotMutateVolumesWhenRollbackCannotQuiescePartialSite(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	operations.failOnce["restore-database:mariadb"] = 1
	operations.failOnOccurrence["compose:down --remove-orphans --timeout 120"] = 2
	operations.failAlways["compose:stop --timeout 120"] = true
	err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
	if err == nil || !strings.Contains(err.Error(), "retained recovery Docker volumes") {
		t.Fatalf("executeSiteRestore() error = %v, want retained recovery error", err)
	}
	if eventIndex(operations.events, "restore-snapshot:files-recovery->site_files") >= 0 || eventIndex(operations.events, "restore-snapshot:database-recovery->site_database") >= 0 {
		t.Fatalf("rollback modified live volumes after both stop attempts failed: %v", operations.events)
	}
}

func TestExecuteSiteRestoreDoesNotRecoverWhileVolumeIsReattached(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	operations.failOnce["restore-database:mariadb"] = 1
	operations.failOnOccurrence["attachments:site_files:none"] = 2
	err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
	if err == nil || !strings.Contains(err.Error(), "site remains stopped") {
		t.Fatalf("executeSiteRestore() error = %v, want stopped-site attachment refusal", err)
	}
	for _, forbidden := range []string{
		"restore-snapshot:files-recovery->site_files",
		"restore-snapshot:database-recovery->site_database",
		"compose:up --remove-orphans --wait --wait-timeout 600 -d",
	} {
		if eventIndex(operations.events, forbidden) >= 0 {
			t.Fatalf("rollback performed %q while a target volume was reattached: %v", forbidden, operations.events)
		}
	}
}

func TestExecuteSiteRestoreDoesNotStopSiteWhenStagingFails(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	operations.failOnce["stage:files-stage"] = 1
	err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
	if err == nil || !strings.Contains(err.Error(), "stage backup") {
		t.Fatalf("executeSiteRestore() error = %v, want staging failure", err)
	}
	if eventIndex(operations.events, "compose:down --remove-orphans --timeout 120") >= 0 {
		t.Fatalf("site stopped after a pre-stage failure: %v", operations.events)
	}
}

func TestExecuteSiteRestoreRefusesAttachedVolumeBeforeSnapshotOrMutation(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	operations.failOnce["attachments:site_files:none"] = 1
	err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
	if err == nil || !strings.Contains(err.Error(), "detached before restore") {
		t.Fatalf("executeSiteRestore() error = %v, want attached-volume refusal", err)
	}
	for _, forbidden := range []string{
		"snapshot:site_files->files-recovery",
		"clear:site_files",
		"clear:site_database",
	} {
		if eventIndex(operations.events, forbidden) >= 0 {
			t.Fatalf("attached-volume refusal still performed %q: %v", forbidden, operations.events)
		}
	}
}

func TestExecuteSiteRestoreRestartsWithoutMutationWhenRecoverySnapshotFails(t *testing.T) {
	t.Parallel()

	operations := newFakeRestoreOperations()
	operations.failOnce["snapshot:site_database->database-recovery"] = 1
	err := executeSiteRestore(testBackupCommand(), operations, testSiteRestorePlan())
	if err == nil || !strings.Contains(err.Error(), "stopped before commit") {
		t.Fatalf("executeSiteRestore() error = %v, want precommit snapshot failure", err)
	}
	if eventIndex(operations.events, "clear:site_files") >= 0 || eventIndex(operations.events, "clear:site_database") >= 0 {
		t.Fatalf("restore mutated target volumes after a snapshot failure: %v", operations.events)
	}
	if eventIndex(operations.events, "compose:up --remove-orphans --wait --wait-timeout 600 -d") < 0 {
		t.Fatalf("restore did not restart the original site after a snapshot failure: %v", operations.events)
	}
}

func TestResolveProjectBackupPathRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(project, "backups")); err != nil {
		t.Fatalf("create backup symlink: %v", err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: project}
	if _, err := resolveProjectBackupPath(context.Background(), ctx, filepath.Join("backups", "customer"), false); err == nil {
		t.Fatal("resolveProjectBackupPath() accepted a symlink outside the project")
	}
}

func containsArgument(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func argumentAfter(args []string, wanted string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == wanted {
			return args[index+1]
		}
	}
	return ""
}

func testRestoreContext() *config.Context {
	return &config.Context{DatabaseService: "mariadb", DatabaseName: "appdb", ProjectDir: "/srv/site"}
}

func testRestoreComposeConfig() composeSecretConfig {
	return composeSecretConfig{
		Name: "site",
		Services: map[string]composeService{
			"init": {Image: "example/init:1"},
			"mariadb": {Volumes: []composeServiceVolume{{
				Type: "volume", Source: "database", Target: "/var/lib/mysql",
			}}},
		},
		Volumes: map[string]composeVolume{
			"database": {Name: "site_database"},
			"files":    {Name: "site_files"},
		},
	}
}

func testRestoreManifest() siteBackupManifest {
	return siteBackupManifest{
		Version:        siteBackupManifestVersion,
		Database:       "mariadb.sql.gz",
		DatabaseName:   "appdb",
		DatabaseVolume: "database",
		Volumes:        map[string]string{"files": "volume-files.tar.gz"},
		Checksums: map[string]string{
			"mariadb.sql.gz":      strings.Repeat("a", 64),
			"volume-files.tar.gz": strings.Repeat("b", 64),
		},
	}
}

func testSiteRestorePlan() siteRestorePlan {
	return siteRestorePlan{
		image:      "example/init:1",
		backupDir:  "/srv/site/backups/one",
		identifier: "test",
		database: siteDatabaseRestore{
			service:        "mariadb",
			databaseName:   "appdb",
			logicalVolume:  "database",
			actualVolume:   "site_database",
			archive:        "mariadb.sql.gz",
			checksum:       strings.Repeat("a", 64),
			recoveryVolume: "database-recovery",
		},
		volumes: []siteVolumeRestore{{
			logical:        "files",
			actual:         "site_files",
			archive:        "volume-files.tar.gz",
			checksum:       strings.Repeat("b", 64),
			stageVolume:    "files-stage",
			recoveryVolume: "files-recovery",
		}},
	}
}

func testBackupCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "backup-test"}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

type fakeSiteBackupOperations struct {
	events           []string
	running          []string
	volumes          map[string]bool
	failOnce         map[string]int
	failAlways       map[string]bool
	failOnOccurrence map[string]int
	eventCounts      map[string]int
}

func newFakeSiteBackupOperations() *fakeSiteBackupOperations {
	return &fakeSiteBackupOperations{
		volumes:          map[string]bool{},
		failOnce:         map[string]int{},
		failAlways:       map[string]bool{},
		failOnOccurrence: map[string]int{},
		eventCounts:      map[string]int{},
	}
}

func newFakeRestoreOperations() *fakeSiteBackupOperations {
	operations := newFakeSiteBackupOperations()
	operations.running = []string{"mariadb", "web"}
	operations.volumes["site_files"] = true
	operations.volumes["site_database"] = true
	return operations
}

func (f *fakeSiteBackupOperations) record(event string) error {
	f.events = append(f.events, event)
	f.eventCounts[event]++
	if f.failAlways[event] {
		return fmt.Errorf("injected failure at %s", event)
	}
	if occurrence := f.failOnOccurrence[event]; occurrence > 0 && f.eventCounts[event] == occurrence {
		return fmt.Errorf("injected failure at occurrence %d of %s", occurrence, event)
	}
	if f.failOnce[event] > 0 {
		f.failOnce[event]--
		return fmt.Errorf("injected failure at %s", event)
	}
	return nil
}

func (f *fakeSiteBackupOperations) runningServices(context.Context) ([]string, error) {
	if err := f.record("running-services"); err != nil {
		return nil, err
	}
	return append([]string{}, f.running...), nil
}

func (f *fakeSiteBackupOperations) compose(_ context.Context, args ...string) error {
	return f.record("compose:" + strings.Join(args, " "))
}

func (f *fakeSiteBackupOperations) backupDatabase(context.Context, string, string, string) error {
	return f.record("backup-database")
}

func (f *fakeSiteBackupOperations) restoreDatabase(_ context.Context, service, database, _ string, checksum string) error {
	if database != "appdb" {
		return fmt.Errorf("unexpected database %q", database)
	}
	if checksum != strings.Repeat("a", 64) {
		return fmt.Errorf("unexpected database checksum %q", checksum)
	}
	return f.record("restore-database:" + service)
}

func (f *fakeSiteBackupOperations) backupVolume(_ context.Context, _, volume, _, _ string) error {
	return f.record("backup-volume:" + volume)
}

func (f *fakeSiteBackupOperations) validateArtifact(_ context.Context, _, archive, _ string, _ bool) error {
	return f.record("validate-artifact:" + archive)
}

func (f *fakeSiteBackupOperations) volumeExists(_ context.Context, volume string) (bool, error) {
	if err := f.record("volume-exists:" + volume); err != nil {
		return false, err
	}
	return f.volumes[volume], nil
}

func (f *fakeSiteBackupOperations) validateOwnedVolume(_ context.Context, logical, actual string) error {
	return f.record("validate-owned-volume:" + logical + "->" + actual)
}

func (f *fakeSiteBackupOperations) createOwnedVolume(_ context.Context, logical, actual string) error {
	if err := f.record("create-owned-volume:" + logical + "->" + actual); err != nil {
		return err
	}
	f.volumes[actual] = true
	return nil
}

func (f *fakeSiteBackupOperations) ensureVolumeAttachments(_ context.Context, volume, allowedService string) error {
	allowed := "none"
	if allowedService != "" {
		allowed = allowedService
	}
	return f.record("attachments:" + volume + ":" + allowed)
}

func (f *fakeSiteBackupOperations) createTemporaryVolume(_ context.Context, volume string) error {
	if err := f.record("create-volume:" + volume); err != nil {
		return err
	}
	f.volumes[volume] = true
	return nil
}

func (f *fakeSiteBackupOperations) removeVolume(_ context.Context, volume string) error {
	if err := f.record("remove-volume:" + volume); err != nil {
		return err
	}
	delete(f.volumes, volume)
	return nil
}

func (f *fakeSiteBackupOperations) stageArtifact(_ context.Context, _, _, _, _, volume string, _ bool) error {
	return f.record("stage:" + volume)
}

func (f *fakeSiteBackupOperations) snapshotVolume(_ context.Context, _, source, recovery string) error {
	return f.record("snapshot:" + source + "->" + recovery)
}

func (f *fakeSiteBackupOperations) clearVolume(_ context.Context, _, volume string) error {
	if err := f.record("clear:" + volume); err != nil {
		return err
	}
	f.volumes[volume] = true
	return nil
}

func (f *fakeSiteBackupOperations) extractStagedVolume(_ context.Context, _, stage, _, target string) error {
	return f.record("extract:" + stage + "->" + target)
}

func (f *fakeSiteBackupOperations) restoreSnapshot(_ context.Context, _, recovery, target string) error {
	return f.record("restore-snapshot:" + recovery + "->" + target)
}

func (f *fakeSiteBackupOperations) waitForDatabase(_ context.Context, service string) error {
	return f.record("wait-database:" + service)
}

func (f *fakeSiteBackupOperations) prepareRestoreWorkspace(_ context.Context, _ string, _ []string, _ string) (string, error) {
	if err := f.record("prepare-database-input"); err != nil {
		return "/srv/site/.sitectl/restore-work-test", err
	}
	return "/srv/site/.sitectl/restore-work-test", nil
}

func (f *fakeSiteBackupOperations) removeWorkDirectory(_ context.Context, workDir string) error {
	return f.record("remove-work-directory:" + workDir)
}

func eventIndex(events []string, wanted string) int {
	for index, event := range events {
		if event == wanted {
			return index
		}
	}
	return -1
}

func countEvent(events []string, wanted string) int {
	count := 0
	for _, event := range events {
		if event == wanted {
			count++
		}
	}
	return count
}

func lastEventIndex(events []string, wanted string) int {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index] == wanted {
			return index
		}
	}
	return -1
}
