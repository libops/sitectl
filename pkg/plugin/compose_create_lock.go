package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

const composeCreateStagingPrefix = ".sitectl-create-checkout-"

var errComposeCreateRecoveryRequired = errors.New("compose create checkout requires manual recovery")

var releaseComposeCreateProjectMutationLock = func(lock *config.ProjectMutationLock) error {
	return lock.Release()
}

type composeTemplateCreateOperations interface {
	checkout(*cobra.Command, *config.Context, ComposeCreateRequest) (bool, error)
	refreshContext(*cobra.Command, *config.Context, ComposeCreateRequest) error
	reconcileComponents(*cobra.Command, *config.Context, map[string]corecomponent.ReviewDecision) error
	applyImageOverrides(*cobra.Command, *config.Context, ComposeImageOverrides) error
	needsInit(*cobra.Command, *config.Context, CreateSpec) (bool, error)
	runCommands(*cobra.Command, *config.Context, []string) error
	printSummary(*cobra.Command, *config.Context, string, bool) error
}

type defaultComposeTemplateCreateOperations struct {
	runner *composeTemplateCreateRunner
}

func (o defaultComposeTemplateCreateOperations) checkout(cmd *cobra.Command, ctx *config.Context, req ComposeCreateRequest) (bool, error) {
	return o.runner.sdk.EnsureClaimedComposeTemplateCheckoutContext(cmd.Context(), cmd.OutOrStdout(), req, ctx)
}

func (o defaultComposeTemplateCreateOperations) refreshContext(_ *cobra.Command, ctx *config.Context, req ComposeCreateRequest) error {
	return refreshCreateContextComposeIdentity(ctx, req)
}

func (o defaultComposeTemplateCreateOperations) reconcileComponents(cmd *cobra.Command, ctx *config.Context, decisions map[string]corecomponent.ReviewDecision) error {
	return o.runner.sdk.reconcileCreateServiceComponents(cmd.Context(), ctx, decisions)
}

func (defaultComposeTemplateCreateOperations) applyImageOverrides(_ *cobra.Command, ctx *config.Context, overrides ComposeImageOverrides) error {
	return ApplyComposeImageOverridesContext(ctx, overrides)
}

func (defaultComposeTemplateCreateOperations) needsInit(_ *cobra.Command, ctx *config.Context, spec CreateSpec) (bool, error) {
	return composeTemplateNeedsInit(ctx, spec)
}

func (o defaultComposeTemplateCreateOperations) runCommands(cmd *cobra.Command, ctx *config.Context, commands []string) error {
	return o.runner.sdk.RunComposeProjectCommandList(cmd, ctx, commands)
}

func (defaultComposeTemplateCreateOperations) printSummary(cmd *cobra.Command, ctx *config.Context, readyMessage string, setupOnly bool) error {
	PrintComposeTemplateCreateSummary(cmd.OutOrStdout(), ctx, readyMessage, setupOnly)
	return nil
}

func withComposeTemplateCreateMutationLock(cmd *cobra.Command, ctx *config.Context, req ComposeCreateRequest, operation func(*cobra.Command) error) (returnErr error) {
	if cmd == nil {
		return fmt.Errorf("create command is nil")
	}
	if operation == nil {
		return fmt.Errorf("compose create operation is nil")
	}
	observation, err := prepareComposeCreateTargetContext(cmd.Context(), req, ctx)
	if err != nil {
		return err
	}
	lock, err := acquireComposeProjectMutationLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		if releaseErr := releaseComposeCreateProjectMutationLock(lock); releaseErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release project mutation lock: %w", releaseErr))
		}
	}()
	if err := revalidateComposeCreateTargetObservation(cmd.Context(), req, ctx, observation); err != nil {
		return err
	}
	return operation(cmd)
}

func ensureComposeCreateProjectDirectory(runCtx context.Context, ctx *config.Context, req ComposeCreateRequest) error {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	projectDir := strings.TrimSpace(ctx.ProjectDir)
	if projectDir == "" {
		return fmt.Errorf("project directory cannot be empty")
	}
	source, err := normalizedComposeCreateCheckoutSource(req.CheckoutSource)
	if err != nil {
		return err
	}
	if ctx.DockerHostType == config.ContextRemote {
		projectDir = path.Clean(strings.ReplaceAll(projectDir, `\`, "/"))
		return ensureRemoteComposeCreateProjectDirectory(runCtx, ctx, projectDir, source)
	}
	return ensureLocalComposeCreateProjectDirectory(runCtx, projectDir, source)
}

func ensureLocalComposeCreateProjectDirectory(runCtx context.Context, projectDir string, source CheckoutSource) error {
	existed, _, err := localProjectDirectoryState(runCtx, projectDir)
	if err != nil {
		return fmt.Errorf("inspect project directory before claim: %w", err)
	}
	if existed {
		return nil
	}
	if source == CheckoutSourceExisting {
		return fmt.Errorf("project directory %q does not exist for checkout source %q", projectDir, CheckoutSourceExisting)
	}
	if err := os.MkdirAll(filepath.Dir(projectDir), 0o750); err != nil {
		return fmt.Errorf("create parent directory for %q: %w", projectDir, err)
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	if err := os.Mkdir(projectDir, 0o750); err == nil {
		return nil
	} else {
		claimErr := err
		existed, _, stateErr := localProjectDirectoryState(runCtx, projectDir)
		if stateErr == nil && existed {
			return nil
		}
		return errors.Join(fmt.Errorf("claim project directory %q: %w", projectDir, claimErr), stateErr)
	}
}

func ensureRemoteComposeCreateProjectDirectory(runCtx context.Context, ctx *config.Context, projectDir string, source CheckoutSource) (returnErr error) {
	connection, err := openRemoteTemplateConnection(runCtx, ctx)
	if err != nil {
		return fmt.Errorf("open remote project directory connection: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close remote project directory connection: %w", closeErr))
		}
	}()
	existed, _, err := remoteProjectDirectoryState(runCtx, connection, projectDir)
	if err != nil {
		return fmt.Errorf("inspect remote project directory before claim: %w", err)
	}
	if existed {
		return nil
	}
	if source == CheckoutSourceExisting {
		return fmt.Errorf("remote project directory %q does not exist for checkout source %q", projectDir, CheckoutSourceExisting)
	}
	if err := connection.MkdirAll(path.Dir(projectDir)); err != nil {
		return fmt.Errorf("create remote parent directory for %q: %w", projectDir, err)
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	if err := connection.Mkdir(projectDir); err == nil {
		if err := connection.Chmod(projectDir, 0o750); err != nil {
			return fmt.Errorf("set claimed remote project directory permissions: %w", err)
		}
		return nil
	} else {
		claimErr := err
		existed, _, stateErr := remoteProjectDirectoryState(runCtx, connection, projectDir)
		if stateErr == nil && existed {
			return nil
		}
		return errors.Join(fmt.Errorf("claim remote project directory %q: %w", projectDir, claimErr), stateErr)
	}
}

func normalizedComposeCreateCheckoutSource(source CheckoutSource) (CheckoutSource, error) {
	if source == "" {
		return CheckoutSourceTemplate, nil
	}
	switch source {
	case CheckoutSourceTemplate, CheckoutSourceExisting:
		return source, nil
	default:
		return "", fmt.Errorf("unknown checkout source %q", source)
	}
}

func (s *SDK) ensureClaimedComposeTemplateCheckoutContext(runCtx context.Context, out io.Writer, req ComposeCreateRequest, ctx *config.Context) (bool, error) {
	source, err := normalizedComposeCreateCheckoutSource(req.CheckoutSource)
	if err != nil {
		return false, err
	}
	if source == CheckoutSourceExisting {
		return false, nil
	}
	if s == nil {
		return false, fmt.Errorf("plugin sdk is not initialized")
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return false, fmt.Errorf("project directory cannot be empty")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	stagingPath, err := newComposeCreateStagingPath(ctx.ProjectDir, ctx.DockerHostType == config.ContextRemote)
	if err != nil {
		return false, err
	}
	if out != nil {
		if ctx.DockerHostType == config.ContextRemote {
			fmt.Fprintf(out, "Cloning %s into %s on %s\n", req.TemplateRepo, ctx.ProjectDir, ctx.SSHHostname)
		} else {
			fmt.Fprintf(out, "Cloning %s into %s\n", req.TemplateRepo, ctx.ProjectDir)
		}
	}
	if ctx.DockerHostType == config.ContextRemote {
		stagingContext := *ctx
		stagingContext.ProjectDir = stagingPath
		if _, err := s.ensureRemoteComposeTemplateCheckout(runCtx, io.Discard, req, &stagingContext); err != nil {
			return false, cleanupRemoteComposeCreateAfterFailure(runCtx, ctx, stagingPath, err)
		}
		if err := publishRemoteComposeCreateStaging(runCtx, ctx, stagingPath); err != nil {
			return false, cleanupRemoteComposeCreateAfterFailure(runCtx, ctx, stagingPath, err)
		}
		return true, nil
	}
	if _, err := s.ensureLocalComposeTemplateCheckout(runCtx, io.Discard, req, stagingPath); err != nil {
		return false, cleanupLocalComposeCreateAfterFailure(ctx.ProjectDir, stagingPath, err)
	}
	if err := publishLocalComposeCreateStaging(runCtx, ctx.ProjectDir, stagingPath); err != nil {
		return false, cleanupLocalComposeCreateAfterFailure(ctx.ProjectDir, stagingPath, err)
	}
	return true, nil
}

// EnsureClaimedComposeTemplateCheckoutContext clones, validates, finalizes, and
// publishes a template into an already-created project directory. It acquires
// the project mutation lock unless runCtx already carries that lock, accepts an
// exact verified retry or rechecks that a new target is still empty, and
// confines all pre-publication files to a generated staging child. Callers that
// mutate the checkout afterward should pass their held lock's Context and
// retain the outer lock for the full create.
func (s *SDK) EnsureClaimedComposeTemplateCheckoutContext(runCtx context.Context, out io.Writer, req ComposeCreateRequest, ctx *config.Context) (created bool, returnErr error) {
	if s == nil {
		return false, fmt.Errorf("plugin sdk is not initialized")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return false, fmt.Errorf("project directory cannot be empty")
	}
	lock, err := acquireComposeProjectMutationLock(runCtx, ctx)
	if err != nil {
		return false, fmt.Errorf("acquire project mutation lock for template checkout: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release project mutation lock after template checkout: %w", releaseErr))
		}
	}()
	lockedContext := lock.Context()
	observation, err := observeComposeCreateTargetContext(lockedContext, req, ctx)
	if err != nil {
		return false, err
	}
	return s.ensureObservedComposeTemplateCheckoutContext(lockedContext, out, req, ctx, observation)
}

// EnsureObservedComposeTemplateCheckoutContext revalidates an exact pre-lock
// observation while holding the shared project mutation lock. Empty template
// targets are staged and published with provenance as the final commit marker;
// a matching verified template retry or explicit existing checkout is left in
// place.
func (s *SDK) EnsureObservedComposeTemplateCheckoutContext(runCtx context.Context, out io.Writer, req ComposeCreateRequest, ctx *config.Context, observation ComposeCreateTargetObservation) (created bool, returnErr error) {
	if s == nil {
		return false, fmt.Errorf("plugin sdk is not initialized")
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return false, fmt.Errorf("project directory cannot be empty")
	}
	lock, err := acquireComposeProjectMutationLock(runCtx, ctx)
	if err != nil {
		return false, fmt.Errorf("acquire project mutation lock for observed template checkout: %w", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("release project mutation lock after observed template checkout: %w", releaseErr))
		}
	}()
	lockedContext := lock.Context()
	if err := revalidateComposeCreateTargetObservation(lockedContext, req, ctx, observation); err != nil {
		return false, err
	}
	return s.ensureObservedComposeTemplateCheckoutContext(lockedContext, out, req, ctx, observation)
}

func (s *SDK) ensureObservedComposeTemplateCheckoutContext(runCtx context.Context, out io.Writer, req ComposeCreateRequest, ctx *config.Context, observation ComposeCreateTargetObservation) (bool, error) {
	source, err := normalizedComposeCreateCheckoutSource(req.CheckoutSource)
	if err != nil {
		return false, err
	}
	if source == CheckoutSourceExisting {
		if observation.state != composeCreateTargetExisting {
			return false, fmt.Errorf("checkout source %q requires an observed existing Compose checkout", CheckoutSourceExisting)
		}
		return false, nil
	}
	switch observation.state {
	case composeCreateTargetEmpty:
		return s.ensureClaimedComposeTemplateCheckoutContext(runCtx, out, req, ctx)
	case composeCreateTargetTemplate:
		return false, nil
	default:
		return false, fmt.Errorf("checkout source %q requires an empty or verified template target", CheckoutSourceTemplate)
	}
}

func newComposeCreateStagingPath(projectDir string, remote bool) (string, error) {
	var identifier [16]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return "", fmt.Errorf("generate create checkout staging name: %w", err)
	}
	name := composeCreateStagingPrefix + hex.EncodeToString(identifier[:])
	if remote {
		return path.Join(path.Clean(strings.ReplaceAll(projectDir, `\`, "/")), name), nil
	}
	return filepath.Join(filepath.Clean(projectDir), name), nil
}

func publishLocalComposeCreateStaging(runCtx context.Context, projectDir, stagingPath string) error {
	if err := validateComposeCreateStagingPath(projectDir, stagingPath, false); err != nil {
		return err
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	rootEntries, exceeded, err := readLocalComposeCreateDirectory(projectDir, 2)
	if err != nil {
		return fmt.Errorf("inspect claimed project directory before publishing checkout: %w", err)
	}
	if exceeded || !onlyComposeCreateStagingFileInfo(rootEntries, filepath.Base(stagingPath)) {
		return fmt.Errorf("project directory %q changed while the staged checkout was being prepared", projectDir)
	}
	if err := validateLocalComposeCreateProvenanceBarrier(stagingPath); err != nil {
		return err
	}
	entries, exceeded, err := readLocalComposeCreateDirectory(stagingPath, maxComposeCreateStateFiles)
	if err != nil {
		return fmt.Errorf("read staged template checkout: %w", err)
	}
	if exceeded {
		return fmt.Errorf("staged template checkout exceeds %d top-level entries", maxComposeCreateStateFiles)
	}
	orderComposeCreatePublishEntries(entries)
	moved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := composeCreateContextError(runCtx); err != nil {
			return recoverLocalComposeCreatePublish(err, projectDir, stagingPath, moved)
		}
		name := entry.Name()
		source := filepath.Join(stagingPath, name)
		destination := filepath.Join(projectDir, name)
		if destination == stagingPath {
			return recoverLocalComposeCreatePublish(fmt.Errorf("staged template contains reserved create path %q", name), projectDir, stagingPath, moved)
		}
		if err := os.Rename(source, destination); err != nil {
			return recoverLocalComposeCreatePublish(fmt.Errorf("publish staged template entry %q: %w", name, err), projectDir, stagingPath, moved)
		}
		moved = append(moved, name)
		if name == ".libops" {
			if err := composeCreateContextError(runCtx); err != nil {
				return recoverLocalComposeCreatePublish(err, projectDir, stagingPath, moved)
			}
		}
	}
	if err := os.Remove(stagingPath); err != nil {
		return fmt.Errorf("remove empty create checkout staging directory: %w", err)
	}
	return nil
}

func validateLocalComposeCreateProvenanceBarrier(stagingPath string) error {
	metadataPath := filepath.Join(stagingPath, ".libops")
	metadata, err := os.Lstat(metadataPath)
	if err != nil {
		return fmt.Errorf("inspect staged template provenance directory: %w", err)
	}
	if metadata.Mode()&os.ModeSymlink != 0 || !metadata.IsDir() {
		return fmt.Errorf("staged template provenance directory must be a real directory")
	}
	lockPath := filepath.Join(stagingPath, filepath.FromSlash(templateLockPath))
	lock, err := os.Lstat(lockPath)
	if err != nil {
		return fmt.Errorf("inspect staged template provenance lock: %w", err)
	}
	if lock.Mode()&os.ModeSymlink != 0 || !lock.Mode().IsRegular() {
		return fmt.Errorf("staged template provenance lock must be a regular file")
	}
	return nil
}

func orderComposeCreatePublishEntries(entries []os.FileInfo) {
	// The provenance directory is the publication commit marker. Moving it last
	// prevents a partial checkout from looking like a verified template retry.
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i].Name(), entries[j].Name()
		if left == ".libops" {
			return false
		}
		if right == ".libops" {
			return true
		}
		return left < right
	})
}

func recoverLocalComposeCreatePublish(cause error, projectDir, stagingPath string, moved []string) error {
	if errors.Is(cause, config.ErrProjectMutationLockLost) {
		return preserveComposeCreateStaging(cause, stagingPath)
	}
	if rollbackErr := rollbackLocalComposeCreatePublish(projectDir, stagingPath, moved); rollbackErr != nil {
		return preserveComposeCreateStaging(errors.Join(cause, rollbackErr), stagingPath)
	}
	return cause
}

func rollbackLocalComposeCreatePublish(projectDir, stagingPath string, moved []string) error {
	var rollbackErr error
	for index := len(moved) - 1; index >= 0; index-- {
		name := moved[index]
		if err := os.Rename(filepath.Join(projectDir, name), filepath.Join(stagingPath, name)); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("roll back staged template entry %q: %w", name, err))
		}
	}
	return rollbackErr
}

func publishRemoteComposeCreateStaging(runCtx context.Context, ctx *config.Context, stagingPath string) (returnErr error) {
	if runCtx == nil {
		runCtx = context.Background()
	}
	projectDir := path.Clean(strings.ReplaceAll(ctx.ProjectDir, `\`, "/"))
	if err := validateComposeCreateStagingPath(projectDir, stagingPath, true); err != nil {
		return err
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	// Keep the SFTP transport available for rollback after ordinary command
	// cancellation. The original lock context is still checked before every
	// mutation, and lock loss explicitly forbids rollback.
	connection, err := openRemoteTemplateConnection(config.ProjectMutationLockFenceContext(runCtx), ctx)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("open remote connection to publish staged checkout: %w", err))
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close remote staged checkout connection: %w", closeErr))
		}
	}()
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	rootEntries, exceeded, err := readRemoteComposeCreateDirectory(runCtx, connection, projectDir, 2)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("inspect claimed remote project directory before publishing checkout: %w", err))
	}
	if exceeded || !onlyComposeCreateStagingFileInfo(rootEntries, path.Base(stagingPath)) {
		return fmt.Errorf("remote project directory %q changed while the staged checkout was being prepared", projectDir)
	}
	if err := validateRemoteComposeCreateProvenanceBarrier(runCtx, connection, stagingPath); err != nil {
		return err
	}
	entries, exceeded, err := readRemoteComposeCreateDirectory(runCtx, connection, stagingPath, maxComposeCreateStateFiles)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("read staged remote template checkout: %w", err))
	}
	if exceeded {
		return fmt.Errorf("staged remote template checkout exceeds %d top-level entries", maxComposeCreateStateFiles)
	}
	orderComposeCreatePublishEntries(entries)
	moved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := composeCreateContextError(runCtx); err != nil {
			return recoverRemoteComposeCreatePublish(runCtx, err, connection, projectDir, stagingPath, moved)
		}
		name := entry.Name()
		source := path.Join(stagingPath, name)
		destination := path.Join(projectDir, name)
		if destination == stagingPath {
			return recoverRemoteComposeCreatePublish(runCtx, fmt.Errorf("staged remote template contains reserved create path %q", name), connection, projectDir, stagingPath, moved)
		}
		if err := connection.Rename(source, destination); err != nil {
			cause := remoteTemplateContextError(runCtx, fmt.Errorf("publish staged remote template entry %q: %w", name, err))
			return recoverRemoteComposeCreatePublish(runCtx, cause, connection, projectDir, stagingPath, moved)
		}
		moved = append(moved, name)
		if name == ".libops" {
			if err := composeCreateContextError(runCtx); err != nil {
				return recoverRemoteComposeCreatePublish(runCtx, err, connection, projectDir, stagingPath, moved)
			}
		}
	}
	if err := connection.Remove(stagingPath); err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("remove empty remote create checkout staging directory: %w", err))
	}
	return nil
}

func validateRemoteComposeCreateProvenanceBarrier(runCtx context.Context, connection remoteTemplateConnection, stagingPath string) error {
	metadataPath := path.Join(stagingPath, ".libops")
	metadata, err := connection.Lstat(metadataPath)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("inspect staged remote template provenance directory: %w", err))
	}
	if metadata.Mode()&os.ModeSymlink != 0 || !metadata.IsDir() {
		return fmt.Errorf("staged remote template provenance directory must be a real directory")
	}
	lockPath := path.Join(stagingPath, path.Clean(templateLockPath))
	lock, err := connection.Lstat(lockPath)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("inspect staged remote template provenance lock: %w", err))
	}
	if lock.Mode()&os.ModeSymlink != 0 || !lock.Mode().IsRegular() {
		return fmt.Errorf("staged remote template provenance lock must be a regular file")
	}
	return nil
}

func recoverRemoteComposeCreatePublish(runCtx context.Context, cause error, connection remoteTemplateConnection, projectDir, stagingPath string, moved []string) error {
	if errors.Is(cause, config.ErrProjectMutationLockLost) {
		return preserveComposeCreateStaging(cause, stagingPath)
	}
	if rollbackErr := rollbackRemoteComposeCreatePublish(runCtx, connection, projectDir, stagingPath, moved); rollbackErr != nil {
		return preserveComposeCreateStaging(errors.Join(cause, rollbackErr), stagingPath)
	}
	if config.ProjectMutationLockContextLost(runCtx) {
		return preserveComposeCreateStaging(errors.Join(cause, config.ErrProjectMutationLockLost), stagingPath)
	}
	return cause
}

func rollbackRemoteComposeCreatePublish(runCtx context.Context, connection remoteTemplateConnection, projectDir, stagingPath string, moved []string) error {
	var rollbackErr error
	for index := len(moved) - 1; index >= 0; index-- {
		if config.ProjectMutationLockContextLost(runCtx) {
			return errors.Join(rollbackErr, config.ErrProjectMutationLockLost)
		}
		name := moved[index]
		if err := connection.Rename(path.Join(projectDir, name), path.Join(stagingPath, name)); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("roll back staged remote template entry %q: %w", name, err))
		}
	}
	return rollbackErr
}

func preserveComposeCreateStaging(cause error, stagingPath string) error {
	if errors.Is(cause, errComposeCreateRecoveryRequired) {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("%w: staged checkout %q was preserved for inspection", errComposeCreateRecoveryRequired, stagingPath))
}

func cleanupLocalComposeCreateAfterFailure(projectDir, stagingPath string, cause error) error {
	if errors.Is(cause, config.ErrProjectMutationLockLost) || errors.Is(cause, errComposeCreateRecoveryRequired) {
		return preserveComposeCreateStaging(cause, stagingPath)
	}
	if cleanupErr := cleanupLocalComposeCreateStaging(projectDir, stagingPath); cleanupErr != nil {
		return preserveComposeCreateStaging(errors.Join(cause, cleanupErr), stagingPath)
	}
	return cause
}

func cleanupRemoteComposeCreateAfterFailure(runCtx context.Context, ctx *config.Context, stagingPath string, cause error) error {
	if config.ProjectMutationLockContextLost(runCtx) || errors.Is(cause, config.ErrProjectMutationLockLost) || errors.Is(cause, errComposeCreateRecoveryRequired) {
		if config.ProjectMutationLockContextLost(runCtx) {
			cause = errors.Join(cause, config.ErrProjectMutationLockLost)
		}
		return preserveComposeCreateStaging(cause, stagingPath)
	}
	if cleanupErr := cleanupRemoteComposeCreateStaging(runCtx, ctx, stagingPath); cleanupErr != nil {
		return preserveComposeCreateStaging(errors.Join(cause, cleanupErr), stagingPath)
	}
	return cause
}

func cleanupLocalComposeCreateStaging(projectDir, stagingPath string) error {
	if err := validateComposeCreateStagingPath(projectDir, stagingPath, false); err != nil {
		return err
	}
	if err := os.RemoveAll(stagingPath); err != nil {
		return fmt.Errorf("clean up staged template checkout: %w", err)
	}
	return nil
}

func cleanupRemoteComposeCreateStaging(runCtx context.Context, ctx *config.Context, stagingPath string) (returnErr error) {
	if ctx == nil {
		return fmt.Errorf("context is nil")
	}
	if err := validateComposeCreateStagingPath(ctx.ProjectDir, stagingPath, true); err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(config.ProjectMutationLockFenceContext(runCtx), remoteTemplateCleanupTimeout)
	defer cancel()
	connection, err := openRemoteTemplateConnection(cleanupCtx, ctx)
	if err != nil {
		return fmt.Errorf("open remote connection to clean up staged checkout: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close remote staged checkout cleanup connection: %w", closeErr))
		}
	}()
	if _, err := connection.Run(cleanupCtx, io.Discard, nil, "rm", "-rf", "--", stagingPath); err != nil {
		return fmt.Errorf("clean up staged remote template checkout: %w", err)
	}
	return nil
}

func validateComposeCreateStagingPath(projectDir, stagingPath string, remote bool) error {
	projectDir = strings.TrimSpace(projectDir)
	stagingPath = strings.TrimSpace(stagingPath)
	if projectDir == "" || stagingPath == "" {
		return fmt.Errorf("refuse unsafe create checkout staging path %q for project %q", stagingPath, projectDir)
	}
	var parent, name string
	if remote {
		projectDir = path.Clean(strings.ReplaceAll(projectDir, `\`, "/"))
		stagingPath = path.Clean(strings.ReplaceAll(stagingPath, `\`, "/"))
		parent = path.Dir(stagingPath)
		name = path.Base(stagingPath)
	} else {
		projectDir = filepath.Clean(projectDir)
		stagingPath = filepath.Clean(stagingPath)
		parent = filepath.Dir(stagingPath)
		name = filepath.Base(stagingPath)
	}
	if parent != projectDir || !isComposeCreateStagingName(name) {
		return fmt.Errorf("refuse unsafe create checkout staging path %q for project %q", stagingPath, projectDir)
	}
	return nil
}

func isComposeCreateStagingName(name string) bool {
	if !strings.HasPrefix(name, composeCreateStagingPrefix) {
		return false
	}
	identifier, err := hex.DecodeString(strings.TrimPrefix(name, composeCreateStagingPrefix))
	return err == nil && len(identifier) == 16
}

func onlyComposeCreateStagingFileInfo(entries []os.FileInfo, stagingName string) bool {
	return len(entries) == 1 && entries[0].Name() == stagingName && entries[0].IsDir()
}
