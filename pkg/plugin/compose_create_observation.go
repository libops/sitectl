package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

const (
	maxComposeCreateStateBytes = 256 << 20
	maxComposeCreateStateFiles = 100_000
)

type composeCreateTargetState uint8

const (
	composeCreateTargetInvalid composeCreateTargetState = iota
	composeCreateTargetEmpty
	composeCreateTargetTemplate
	composeCreateTargetExisting
)

// ComposeTemplateProvenance is the template source recorded by sitectl after a
// successful staged checkout. Digest is the SHA-256 digest of the exact lock
// file used for create retry compare-and-swap validation.
type ComposeTemplateProvenance struct {
	Repository string
	Ref        string
	Commit     string
	Digest     string
}

// ComposeCreateTargetObservation is an opaque compare-and-swap token for one
// claimed Compose create target. Callers obtain it before waiting for the
// project mutation lock and pass it back under that lock. Its zero value is
// invalid.
type ComposeCreateTargetObservation struct {
	projectDir       string
	remote           bool
	checkoutSource   CheckoutSource
	requestedRepo    string
	requestedRef     string
	state            composeCreateTargetState
	localRoot        os.FileInfo
	remoteRoot       string
	rootMode         os.FileMode
	rootSize         int64
	rootModification time.Time
	stateFile        string
	stateDigest      [sha256.Size]byte
	provenance       ComposeTemplateProvenance
}

// IsEmpty reports whether the observed target was an empty claimed directory.
func (o ComposeCreateTargetObservation) IsEmpty() bool {
	return o.state == composeCreateTargetEmpty
}

// TemplateProvenance returns verified template provenance when the observation
// represents a retry of a previously completed template checkout.
func (o ComposeCreateTargetObservation) TemplateProvenance() (ComposeTemplateProvenance, bool) {
	return o.provenance, o.state == composeCreateTargetTemplate
}

// PrepareComposeCreateTargetContext safely claims a missing template target and
// returns an exact pre-lock observation. A template retry is accepted only when
// its sitectl provenance matches the requested repository and ref semantics.
func (s *SDK) PrepareComposeCreateTargetContext(runCtx context.Context, req ComposeCreateRequest, ctx *config.Context) (ComposeCreateTargetObservation, error) {
	if s == nil {
		return ComposeCreateTargetObservation{}, fmt.Errorf("plugin sdk is not initialized")
	}
	return prepareComposeCreateTargetContext(runCtx, req, ctx)
}

func prepareComposeCreateTargetContext(runCtx context.Context, req ComposeCreateRequest, ctx *config.Context) (ComposeCreateTargetObservation, error) {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := ensureComposeCreateProjectDirectory(runCtx, ctx, req); err != nil {
		return ComposeCreateTargetObservation{}, err
	}
	return observeComposeCreateTargetContext(runCtx, req, ctx)
}

// RevalidateComposeCreateTargetContext performs the compare-and-swap check for
// a pre-lock observation. Callers must pass their project mutation lock Context
// and stop all target mutations when this method fails.
func (s *SDK) RevalidateComposeCreateTargetContext(runCtx context.Context, req ComposeCreateRequest, ctx *config.Context, observation ComposeCreateTargetObservation) error {
	if s == nil {
		return fmt.Errorf("plugin sdk is not initialized")
	}
	return revalidateComposeCreateTargetObservation(runCtx, req, ctx, observation)
}

func observeComposeCreateTargetContext(runCtx context.Context, req ComposeCreateRequest, ctx *config.Context) (ComposeCreateTargetObservation, error) {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return ComposeCreateTargetObservation{}, err
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return ComposeCreateTargetObservation{}, fmt.Errorf("project directory cannot be empty")
	}
	source, err := normalizedComposeCreateCheckoutSource(req.CheckoutSource)
	if err != nil {
		return ComposeCreateTargetObservation{}, err
	}
	observation, notEmpty, err := inspectComposeCreateRoot(runCtx, ctx)
	if err != nil {
		return ComposeCreateTargetObservation{}, err
	}
	observation.checkoutSource = source
	if source == CheckoutSourceTemplate {
		observation.requestedRepo, err = validateTemplateRepository(req.TemplateRepo)
		if err != nil {
			return ComposeCreateTargetObservation{}, err
		}
		observation.requestedRef, err = validateTemplateRef(req.TemplateBranch)
		if err != nil {
			return ComposeCreateTargetObservation{}, err
		}
	}
	if !notEmpty {
		if source == CheckoutSourceExisting {
			return ComposeCreateTargetObservation{}, fmt.Errorf("project directory %q is empty; checkout source %q requires an existing Compose checkout", ctx.ProjectDir, CheckoutSourceExisting)
		}
		observation.state = composeCreateTargetEmpty
		observation.stateDigest = sha256.Sum256([]byte("empty\x00"))
		return observation, nil
	}
	if source == CheckoutSourceTemplate {
		_, digest, err := validateComposeTemplateProvenanceContext(runCtx, ctx, req)
		if err != nil {
			return ComposeCreateTargetObservation{}, fmt.Errorf("%w; choose checkout source %q only after explicitly validating the existing checkout", err, CheckoutSourceExisting)
		}
		treeDigest, err := composeCreateTreeDigest(runCtx, ctx)
		if err != nil {
			return ComposeCreateTargetObservation{}, err
		}
		confirmedProvenance, confirmedDigest, err := validateComposeTemplateProvenanceContext(runCtx, ctx, req)
		if err != nil {
			return ComposeCreateTargetObservation{}, fmt.Errorf("template provenance changed while the create target was observed: %w", err)
		}
		if confirmedDigest != digest {
			return ComposeCreateTargetObservation{}, fmt.Errorf("template provenance changed while the create target was observed")
		}
		observation.state = composeCreateTargetTemplate
		observation.stateFile = templateLockPath
		observation.stateDigest = treeDigest
		observation.provenance = confirmedProvenance
		observation.provenance.Digest = "sha256:" + hex.EncodeToString(digest[:])
		return observation, nil
	}
	composeInputs, digest, err := composeCreateExistingStateDigest(runCtx, ctx)
	if err != nil {
		return ComposeCreateTargetObservation{}, err
	}
	treeDigest, err := composeCreateTreeDigest(runCtx, ctx)
	if err != nil {
		return ComposeCreateTargetObservation{}, err
	}
	combinedDigest := sha256.New()
	_, _ = combinedDigest.Write(digest[:])
	_, _ = combinedDigest.Write(treeDigest[:])
	observation.state = composeCreateTargetExisting
	observation.stateFile = composeInputs
	copy(observation.stateDigest[:], combinedDigest.Sum(nil))
	return observation, nil
}

func inspectComposeCreateRoot(runCtx context.Context, ctx *config.Context) (ComposeCreateTargetObservation, bool, error) {
	observation := ComposeCreateTargetObservation{
		projectDir: strings.TrimSpace(ctx.ProjectDir),
		remote:     ctx.DockerHostType == config.ContextRemote,
	}
	if !observation.remote {
		info, err := os.Lstat(observation.projectDir)
		if err != nil {
			return ComposeCreateTargetObservation{}, false, fmt.Errorf("inspect claimed project directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ComposeCreateTargetObservation{}, false, fmt.Errorf("project directory %q must be a real directory, not a symlink or other file", observation.projectDir)
		}
		notEmpty, err := localComposeCreateDirectoryHasEntries(observation.projectDir)
		if err != nil {
			return ComposeCreateTargetObservation{}, false, fmt.Errorf("read claimed project directory: %w", err)
		}
		observation.localRoot = info
		observation.rootMode = info.Mode()
		observation.rootSize = info.Size()
		observation.rootModification = info.ModTime()
		return observation, notEmpty, nil
	}
	connection, err := openRemoteTemplateConnection(runCtx, ctx)
	if err != nil {
		return ComposeCreateTargetObservation{}, false, fmt.Errorf("open remote create target: %w", err)
	}
	defer connection.Close()
	info, err := connection.Lstat(ctx.ResolveProjectPath("."))
	if err != nil {
		return ComposeCreateTargetObservation{}, false, fmt.Errorf("inspect claimed remote project directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ComposeCreateTargetObservation{}, false, fmt.Errorf("remote project directory %q must be a real directory, not a symlink or other file", observation.projectDir)
	}
	notEmpty, err := connection.DirectoryHasEntries(runCtx, ctx.ResolveProjectPath("."))
	if err != nil {
		return ComposeCreateTargetObservation{}, false, fmt.Errorf("read claimed remote project directory: %w", err)
	}
	observation.remoteRoot = path.Clean(strings.ReplaceAll(observation.projectDir, `\`, "/"))
	// Production SSH transports fence root replacement with the remote POSIX
	// device/inode pair. Minimal injected transports used by source tests fall
	// back to path plus directory attributes and therefore model cooperative
	// sitectl actors rather than out-of-band root replacement.
	if identityProvider, ok := connection.(remoteComposeCreateRootIdentityProvider); ok {
		identity, err := identityProvider.composeCreateRootIdentity(runCtx, observation.remoteRoot)
		if err != nil {
			return ComposeCreateTargetObservation{}, false, err
		}
		observation.remoteRoot = identity
	}
	observation.rootMode = info.Mode()
	observation.rootSize = info.Size()
	observation.rootModification = info.ModTime()
	return observation, notEmpty, nil
}

func revalidateComposeCreateTargetObservation(runCtx context.Context, req ComposeCreateRequest, ctx *config.Context, want ComposeCreateTargetObservation) error {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if want.state == composeCreateTargetInvalid || strings.TrimSpace(want.projectDir) == "" {
		return fmt.Errorf("compose create target observation is invalid")
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("project directory cannot be empty")
	}
	source, err := normalizedComposeCreateCheckoutSource(req.CheckoutSource)
	if err != nil {
		return err
	}
	remote := ctx.DockerHostType == config.ContextRemote
	requestedRepo, requestedRef := "", ""
	if source == CheckoutSourceTemplate {
		requestedRepo, err = validateTemplateRepository(req.TemplateRepo)
		if err != nil {
			return err
		}
		requestedRef, err = validateTemplateRef(req.TemplateBranch)
		if err != nil {
			return err
		}
	}
	if source != want.checkoutSource || requestedRepo != want.requestedRepo || requestedRef != want.requestedRef || remote != want.remote || strings.TrimSpace(ctx.ProjectDir) != want.projectDir {
		return fmt.Errorf("compose create target request differs from its pre-lock observation")
	}
	got, err := observeComposeCreateTargetContext(runCtx, req, ctx)
	if err != nil {
		return fmt.Errorf("revalidate create target under mutation lock: %w", err)
	}
	if got.state != want.state || got.stateFile != want.stateFile || got.stateDigest != want.stateDigest {
		return fmt.Errorf("project directory %q changed while create waited for its mutation lock", ctx.ProjectDir)
	}
	if !sameComposeCreateRoot(want, got) {
		return fmt.Errorf("project directory %q was replaced while create waited for its mutation lock", ctx.ProjectDir)
	}
	return nil
}

func sameComposeCreateRoot(want, got ComposeCreateTargetObservation) bool {
	if want.remote != got.remote || want.rootMode != got.rootMode || want.rootSize != got.rootSize || !want.rootModification.Equal(got.rootModification) {
		return false
	}
	if want.remote {
		return want.remoteRoot != "" && want.remoteRoot == got.remoteRoot
	}
	return want.localRoot != nil && got.localRoot != nil && os.SameFile(want.localRoot, got.localRoot)
}

func composeCreateExistingStateDigest(runCtx context.Context, ctx *config.Context) (string, [sha256.Size]byte, error) {
	composeNames := normalizedComposeCreateInputNames(ctx.ComposeFile)
	if len(composeNames) == 0 {
		for _, candidate := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
			exists, err := ctx.FileExists(ctx.ResolveProjectPath(candidate))
			if err != nil {
				return "", [sha256.Size]byte{}, fmt.Errorf("inspect Compose project file %q: %w", candidate, err)
			}
			if exists {
				composeNames = []string{candidate}
				break
			}
		}
	}
	if len(composeNames) == 0 {
		return "", [sha256.Size]byte{}, fmt.Errorf("project directory %q does not look like a Docker Compose project for checkout source %q", ctx.ProjectDir, CheckoutSourceExisting)
	}
	envNames := normalizedComposeCreateInputNames(ctx.EnvFile)
	if len(envNames) == 0 {
		if exists, err := ctx.FileExists(ctx.ResolveProjectPath(".env")); err != nil {
			return "", [sha256.Size]byte{}, fmt.Errorf("inspect Compose environment file: %w", err)
		} else if exists {
			envNames = []string{".env"}
		}
	}
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, "sitectl-compose-create-inputs-v1\x00")
	identity := make([]string, 0, len(composeNames)+len(envNames))
	totalBytes := 0
	for _, input := range append(composeNames, envNames...) {
		data, err := readComposeCreateProjectFile(runCtx, ctx, ctx.ResolveProjectPath(input), maxComposeCreateStateBytes)
		if err != nil {
			return "", [sha256.Size]byte{}, fmt.Errorf("read Compose input %q: %w", input, err)
		}
		if len(data) > maxComposeCreateStateBytes-totalBytes {
			return "", [sha256.Size]byte{}, fmt.Errorf("Compose inputs exceed %d bytes", maxComposeCreateStateBytes)
		}
		totalBytes += len(data)
		_, _ = io.WriteString(hasher, input+"\x00"+strconv.Itoa(len(data))+"\x00")
		_, _ = hasher.Write(data)
		_, _ = hasher.Write([]byte{0})
		identity = append(identity, input)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return strings.Join(identity, "\x00"), digest, nil
}

func normalizedComposeCreateInputNames(values []string) []string {
	names := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		names = append(names, value)
	}
	return names
}

type composeCreateTreeDigestState struct {
	hasher hash.Hash
	files  int
	bytes  int64
}

func composeCreateTreeDigest(runCtx context.Context, ctx *config.Context) ([sha256.Size]byte, error) {
	if err := composeCreateContextError(runCtx); err != nil {
		return [sha256.Size]byte{}, err
	}
	state := &composeCreateTreeDigestState{hasher: sha256.New()}
	_, _ = io.WriteString(state.hasher, "sitectl-compose-create-tree-v1\x00")
	var err error
	if ctx.DockerHostType == config.ContextRemote {
		err = composeCreateRemoteTreeDigest(runCtx, ctx, state)
	} else {
		err = composeCreateLocalTreeDigest(runCtx, ctx.ProjectDir, state)
	}
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], state.hasher.Sum(nil))
	return digest, nil
}

func composeCreateLocalTreeDigest(runCtx context.Context, projectDir string, state *composeCreateTreeDigestState) error {
	var walk func(string, string) error
	walk = func(directory, relativeDirectory string) error {
		if err := composeCreateContextError(runCtx); err != nil {
			return err
		}
		remaining := maxComposeCreateStateFiles - state.files
		entries, exceeded, err := readLocalComposeCreateDirectory(directory, remaining)
		if err != nil {
			return fmt.Errorf("read create target tree %q: %w", directory, err)
		}
		if exceeded {
			return fmt.Errorf("create target tree exceeds %d entries; choose checkout source %q after explicit validation", maxComposeCreateStateFiles, CheckoutSourceExisting)
		}
		for _, info := range entries {
			if err := composeCreateContextError(runCtx); err != nil {
				return err
			}
			name := info.Name()
			relativePath := filepath.ToSlash(filepath.Join(relativeDirectory, name))
			if relativeDirectory == "" && isComposeCreateStagingName(name) {
				return incompleteComposeCreateStagingError(relativePath)
			}
			if skipComposeCreateTreeEntry(relativePath) {
				continue
			}
			if err := state.addEntry(relativePath, info); err != nil {
				return err
			}
			fullPath := filepath.Join(directory, name)
			switch {
			case info.IsDir():
				if err := walk(fullPath, relativePath); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if err := digestComposeCreateLocalFile(runCtx, fullPath, relativePath, info, state); err != nil {
					return err
				}
			case info.Mode()&os.ModeSymlink != 0:
				if err := digestComposeCreateLocalSymlink(fullPath, relativePath, info, state); err != nil {
					return err
				}
			default:
				return fmt.Errorf("create target tree entry %q has an unsupported file type; choose checkout source %q after explicit validation", relativePath, CheckoutSourceExisting)
			}
		}
		return nil
	}
	return walk(filepath.Clean(projectDir), "")
}

func composeCreateRemoteTreeDigest(runCtx context.Context, ctx *config.Context, state *composeCreateTreeDigestState) (returnErr error) {
	connection, err := openRemoteTemplateConnection(runCtx, ctx)
	if err != nil {
		return fmt.Errorf("open remote create target for state digest: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close remote create target state digest connection: %w", closeErr))
		}
	}()
	root := path.Clean(strings.ReplaceAll(ctx.ProjectDir, `\`, "/"))
	var walk func(string, string) error
	walk = func(directory, relativeDirectory string) error {
		if err := composeCreateContextError(runCtx); err != nil {
			return err
		}
		remaining := maxComposeCreateStateFiles - state.files
		entries, exceeded, err := readRemoteComposeCreateDirectory(runCtx, connection, directory, remaining)
		if err != nil {
			return remoteTemplateContextError(runCtx, fmt.Errorf("read remote create target tree %q: %w", directory, err))
		}
		if exceeded {
			return fmt.Errorf("remote create target tree exceeds %d entries; choose checkout source %q after explicit validation", maxComposeCreateStateFiles, CheckoutSourceExisting)
		}
		for _, info := range entries {
			if err := composeCreateContextError(runCtx); err != nil {
				return err
			}
			name := info.Name()
			if name == "" || name == "." || name == ".." || path.Base(name) != name {
				return fmt.Errorf("remote create target contains unsafe entry name %q", name)
			}
			relativePath := path.Join(relativeDirectory, name)
			if relativeDirectory == "" && isComposeCreateStagingName(name) {
				return incompleteComposeCreateStagingError(relativePath)
			}
			if skipComposeCreateTreeEntry(relativePath) {
				continue
			}
			if err := state.addEntry(relativePath, info); err != nil {
				return err
			}
			fullPath := path.Join(directory, name)
			switch {
			case info.IsDir():
				if err := walk(fullPath, relativePath); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if err := digestComposeCreateRemoteFile(runCtx, connection, fullPath, relativePath, info, state); err != nil {
					return err
				}
			case info.Mode()&os.ModeSymlink != 0:
				if err := digestComposeCreateRemoteSymlink(runCtx, connection, fullPath, relativePath, info, state); err != nil {
					return err
				}
			default:
				return fmt.Errorf("remote create target tree entry %q has an unsupported file type; choose checkout source %q after explicit validation", relativePath, CheckoutSourceExisting)
			}
		}
		return nil
	}
	return walk(root, "")
}

func skipComposeCreateTreeEntry(relativePath string) bool {
	if strings.Contains(relativePath, "/") {
		return false
	}
	return relativePath == ".git"
}

func incompleteComposeCreateStagingError(relativePath string) error {
	return fmt.Errorf("project contains incomplete create staging directory %q; inspect and recover it before retrying create", relativePath)
}

func (s *composeCreateTreeDigestState) addEntry(relativePath string, info os.FileInfo) error {
	s.files++
	if s.files > maxComposeCreateStateFiles {
		return fmt.Errorf("create target tree exceeds %d entries; choose checkout source %q after explicit validation", maxComposeCreateStateFiles, CheckoutSourceExisting)
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	} else if info.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
	}
	_, _ = io.WriteString(s.hasher, kind)
	_, _ = io.WriteString(s.hasher, "\x00"+relativePath+"\x00"+strconv.FormatUint(uint64(info.Mode()), 10)+"\x00"+strconv.FormatInt(info.Size(), 10)+"\x00")
	return nil
}

func (s *composeCreateTreeDigestState) reserveFile(relativePath string, size int64) error {
	if size < 0 || size > maxComposeCreateStateBytes-s.bytes {
		return fmt.Errorf("create target tree exceeds %d bytes while reading %q; choose checkout source %q after explicit validation", maxComposeCreateStateBytes, relativePath, CheckoutSourceExisting)
	}
	s.bytes += size
	return nil
}

func digestComposeCreateLocalFile(runCtx context.Context, filename, relativePath string, before os.FileInfo, state *composeCreateTreeDigestState) (returnErr error) {
	if err := state.reserveFile(relativePath, before.Size()); err != nil {
		return err
	}
	file, err := os.Open(filename) // #nosec G304 -- filename is a walked child of the caller-selected project root.
	if err != nil {
		return fmt.Errorf("open create target tree file %q: %w", relativePath, err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if err := digestComposeCreateFileContents(runCtx, file, before.Size(), relativePath, state.hasher); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return fmt.Errorf("reinspect create target tree file %q: %w", relativePath, err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("create target tree file %q changed while it was observed", relativePath)
	}
	return nil
}

func digestComposeCreateRemoteFile(runCtx context.Context, connection remoteTemplateConnection, filename, relativePath string, before os.FileInfo, state *composeCreateTreeDigestState) (returnErr error) {
	if err := state.reserveFile(relativePath, before.Size()); err != nil {
		return err
	}
	file, err := connection.Open(filename)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("open remote create target tree file %q: %w", relativePath, err))
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if err := digestComposeCreateFileContents(runCtx, file, before.Size(), relativePath, state.hasher); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("reinspect remote create target tree file %q: %w", relativePath, err))
	}
	if before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("remote create target tree file %q changed while it was observed", relativePath)
	}
	return nil
}

func digestComposeCreateLocalSymlink(filename, relativePath string, before os.FileInfo, state *composeCreateTreeDigestState) error {
	target, err := os.Readlink(filename)
	if err != nil {
		return fmt.Errorf("read create target symlink %q: %w", relativePath, err)
	}
	if err := state.reserveFile(relativePath, int64(len(target))); err != nil {
		return err
	}
	_, _ = io.WriteString(state.hasher, target)
	_, _ = state.hasher.Write([]byte{0})
	after, err := os.Lstat(filename)
	if err != nil {
		return fmt.Errorf("reinspect create target symlink %q: %w", relativePath, err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("create target symlink %q changed while it was observed", relativePath)
	}
	return nil
}

func digestComposeCreateRemoteSymlink(runCtx context.Context, connection remoteTemplateConnection, filename, relativePath string, before os.FileInfo, state *composeCreateTreeDigestState) error {
	target, err := connection.ReadLink(filename)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("read remote create target symlink %q: %w", relativePath, err))
	}
	if err := state.reserveFile(relativePath, int64(len(target))); err != nil {
		return err
	}
	_, _ = io.WriteString(state.hasher, target)
	_, _ = state.hasher.Write([]byte{0})
	after, err := connection.Lstat(filename)
	if err != nil {
		return remoteTemplateContextError(runCtx, fmt.Errorf("reinspect remote create target symlink %q: %w", relativePath, err))
	}
	if before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("remote create target symlink %q changed while it was observed", relativePath)
	}
	return nil
}

func digestComposeCreateFileContents(runCtx context.Context, reader io.Reader, size int64, relativePath string, hasher hash.Hash) error {
	if err := composeCreateContextError(runCtx); err != nil {
		return err
	}
	written, err := io.CopyN(hasher, reader, size)
	if err != nil {
		return fmt.Errorf("read create target tree file %q: copied %d of %d bytes: %w", relativePath, written, size, err)
	}
	var extra [1]byte
	if count, err := reader.Read(extra[:]); count != 0 || err == nil {
		return fmt.Errorf("create target tree file %q grew while it was observed", relativePath)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("finish reading create target tree file %q: %w", relativePath, err)
	}
	_, _ = hasher.Write([]byte{0})
	return composeCreateContextError(runCtx)
}

// ValidateComposeTemplateProvenanceContext validates an existing claimed
// checkout against the requested repository and ref semantics. It rejects
// ref-less legacy provenance for a named branch or tag; callers may retry with
// the recorded full commit or explicitly select an existing checkout instead.
func (s *SDK) ValidateComposeTemplateProvenanceContext(runCtx context.Context, ctx *config.Context, req ComposeCreateRequest) (ComposeTemplateProvenance, error) {
	if s == nil {
		return ComposeTemplateProvenance{}, fmt.Errorf("plugin sdk is not initialized")
	}
	provenance, _, err := validateComposeTemplateProvenanceContext(runCtx, ctx, req)
	return provenance, err
}

func validateComposeTemplateProvenanceContext(runCtx context.Context, ctx *config.Context, req ComposeCreateRequest) (ComposeTemplateProvenance, [sha256.Size]byte, error) {
	if runCtx == nil {
		runCtx = context.Background()
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, err
	}
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory cannot be empty")
	}
	requestedRepository, err := validateTemplateRepository(req.TemplateRepo)
	if err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, err
	}
	requestedRef, err := validateTemplateRef(req.TemplateBranch)
	if err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, err
	}
	data, err := readComposeCreateProjectFile(runCtx, ctx, ctx.ResolveProjectPath(templateLockPath), maxTemplateLockBytes)
	if err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory %q is not a verified checkout of template %q: %w", ctx.ProjectDir, requestedRepository, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var lock templateLock
	if err := decoder.Decode(&lock); err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("parse template provenance: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("parse template provenance: multiple YAML documents are not allowed")
		}
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("parse template provenance: %w", err)
	}
	if lock.APIVersion != templateLockAPIVersion || lock.Kind != templateLockKind || lock.Schema != templateLockSchema {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory %q has unsupported template provenance", ctx.ProjectDir)
	}
	actualRepository, err := validateTemplateRepository(lock.Template.Repository)
	if err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("validate template provenance repository: %w", err)
	}
	if actualRepository != requestedRepository {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory %q belongs to template %q, not requested template %q", ctx.ProjectDir, actualRepository, requestedRepository)
	}
	commit := strings.ToLower(strings.TrimSpace(lock.Template.Commit))
	if !templateCommitPattern.MatchString(commit) {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory %q has invalid template commit provenance", ctx.ProjectDir)
	}
	actualRef, err := validateTemplateRef(lock.Template.Ref)
	if err != nil {
		return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("validate template provenance ref: %w", err)
	}
	if actualRef != requestedRef {
		legacyCommitMatch := actualRef == "" && templateCommitPattern.MatchString(requestedRef) && strings.EqualFold(requestedRef, commit)
		if !legacyCommitMatch {
			if actualRef == "" && requestedRef != "" {
				return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory %q has legacy template provenance without the requested ref %q; retry with the recorded commit %s or choose checkout source %q after explicit validation", ctx.ProjectDir, requestedRef, commit, CheckoutSourceExisting)
			}
			return ComposeTemplateProvenance{}, [sha256.Size]byte{}, fmt.Errorf("project directory %q belongs to template ref %q, not requested ref %q", ctx.ProjectDir, actualRef, requestedRef)
		}
	}
	digest := sha256.Sum256(data)
	return ComposeTemplateProvenance{
		Repository: actualRepository,
		Ref:        actualRef,
		Commit:     commit,
		Digest:     "sha256:" + hex.EncodeToString(digest[:]),
	}, digest, nil
}

func readComposeCreateProjectFile(runCtx context.Context, ctx *config.Context, filename string, maximum int64) (returnData []byte, returnErr error) {
	if err := composeCreateContextError(runCtx); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if ctx.DockerHostType == config.ContextRemote {
		return readRemoteComposeCreateProjectFile(runCtx, ctx, filename, maximum)
	}
	if err := ctx.ValidateProjectRegularFile(ctx.ProjectDir, filename); err != nil {
		return nil, fmt.Errorf("validate project file %q: %w", filename, err)
	}
	accessor, err := ctx.NewFileAccessor()
	if err != nil {
		return nil, fmt.Errorf("open project file accessor: %w", err)
	}
	defer func() {
		if closeErr := accessor.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close project file accessor: %w", closeErr))
		}
	}()
	info, err := accessor.Stat(filename)
	if err != nil {
		return nil, fmt.Errorf("inspect project file %q: %w", filename, err)
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("project file %q exceeds %d bytes", filename, maximum)
	}
	data, err := accessor.ReadFileContext(runCtx, filename)
	if err != nil {
		return nil, fmt.Errorf("read project file %q: %w", filename, err)
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("project file %q exceeds %d bytes", filename, maximum)
	}
	return data, nil
}

func readRemoteComposeCreateProjectFile(runCtx context.Context, ctx *config.Context, filename string, maximum int64) (returnData []byte, returnErr error) {
	connection, err := openRemoteTemplateConnection(runCtx, ctx)
	if err != nil {
		return nil, fmt.Errorf("open remote project file connection: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close remote project file connection: %w", closeErr))
		}
	}()
	filename, err = validateRemoteComposeCreateProjectFile(connection, ctx.ProjectDir, filename)
	if err != nil {
		return nil, err
	}
	file, err := connection.Open(filename)
	if err != nil {
		return nil, remoteTemplateContextError(runCtx, fmt.Errorf("open remote project file %q: %w", filename, err))
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close remote project file %q: %w", filename, closeErr))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, remoteTemplateContextError(runCtx, fmt.Errorf("inspect remote project file %q: %w", filename, err))
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("project file %q exceeds %d bytes", filename, maximum)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, remoteTemplateContextError(runCtx, fmt.Errorf("read remote project file %q: %w", filename, err))
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("project file %q exceeds %d bytes", filename, maximum)
	}
	if err := composeCreateContextError(runCtx); err != nil {
		return nil, err
	}
	return data, nil
}

func validateRemoteComposeCreateProjectFile(connection remoteTemplateConnection, projectDir, filename string) (string, error) {
	projectDir = path.Clean(strings.ReplaceAll(strings.TrimSpace(projectDir), `\`, "/"))
	filename = path.Clean(strings.ReplaceAll(strings.TrimSpace(filename), `\`, "/"))
	relative, err := remoteComposeCreateRelativePath(projectDir, filename)
	if err != nil || relative == "." {
		return "", fmt.Errorf("remote project file %q must be beneath project root %q", filename, projectDir)
	}
	current := projectDir
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("remote project file %q contains an unsafe path component", filename)
		}
		current = path.Join(current, part)
		info, err := connection.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect remote project file %q: %w", filename, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("remote project file %q traverses symlink %q", filename, current)
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("remote project file %q is not a regular file", filename)
			}
			continue
		}
		if !info.IsDir() {
			return "", fmt.Errorf("remote project file %q traverses non-directory %q", filename, current)
		}
	}
	return filename, nil
}

func remoteComposeCreateRelativePath(root, target string) (string, error) {
	root = path.Clean(root)
	target = path.Clean(target)
	if root == target {
		return ".", nil
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("remote path %q is not beneath %q", target, root)
	}
	return strings.TrimPrefix(target, prefix), nil
}

func parseRemoteComposeCreateRootIdentity(value string) (string, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return "", fmt.Errorf("remote project root returned invalid device and inode identity %q", value)
	}
	device, deviceErr := strconv.ParseUint(parts[0], 10, 64)
	inode, inodeErr := strconv.ParseUint(parts[1], 10, 64)
	if deviceErr != nil || inodeErr != nil {
		return "", fmt.Errorf("remote project root returned invalid device and inode identity %q", value)
	}
	return "posix:" + strconv.FormatUint(device, 10) + ":" + strconv.FormatUint(inode, 10), nil
}

func composeCreateContextError(runCtx context.Context) error {
	if config.ProjectMutationLockContextLost(runCtx) {
		return config.ErrProjectMutationLockLost
	}
	if runCtx == nil || runCtx.Err() == nil {
		return nil
	}
	if cause := context.Cause(runCtx); cause != nil {
		return cause
	}
	return runCtx.Err()
}
