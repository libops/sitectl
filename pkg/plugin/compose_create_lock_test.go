package plugin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

const composeCreateTestTemplateRepo = "https://example.org/template.git"

func TestComposeTemplateCreateMutationLockCoversEveryMutationPhase(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	operations := &recordingComposeTemplateCreateOperations{t: t, ctx: ctx}
	runner := &composeTemplateCreateRunner{
		sdk:        &SDK{},
		operations: operations,
		spec: CreateSpec{
			DockerComposeInit:  []string{"init"},
			DockerComposeBuild: []string{"build"},
			DockerComposeUp:    []string{"up"},
		},
	}
	request := ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   composeCreateTestTemplateRepo,
		ImageOverrides: ComposeImageOverrides{Images: map[string]string{"app": "example/app:1"}},
	}
	cmd := composeCreateTestCommand(context.Background())
	if err := withComposeTemplateCreateMutationLock(cmd, ctx, request, func(lockedCmd *cobra.Command) error {
		return runner.runLocked(lockedCmd, ctx, request)
	}); err != nil {
		t.Fatalf("withComposeTemplateCreateMutationLock() error = %v", err)
	}
	want := []string{"checkout", "refresh", "reconcile", "overrides", "needs-init", "commands:init", "commands:build", "commands:up", "summary"}
	if !reflect.DeepEqual(operations.phases, want) {
		t.Fatalf("locked create phases = %v, want %v", operations.phases, want)
	}
}

func TestComposeTemplateCreateStopsMutationsWhenLockContextIsCancelled(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	runCtx, cancel := context.WithCancel(context.Background())
	operations := &recordingComposeTemplateCreateOperations{t: t, ctx: ctx, cancelAfter: "checkout", cancel: cancel}
	runner := &composeTemplateCreateRunner{
		sdk:        &SDK{},
		operations: operations,
		spec: CreateSpec{
			DockerComposeInit:  []string{"init"},
			DockerComposeBuild: []string{"build"},
			DockerComposeUp:    []string{"up"},
		},
	}
	request := ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}
	err := withComposeTemplateCreateMutationLock(composeCreateTestCommand(runCtx), ctx, request, func(lockedCmd *cobra.Command) error {
		return runner.runLocked(lockedCmd, ctx, request)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled locked create error = %v, want context cancellation", err)
	}
	if want := []string{"checkout"}; !reflect.DeepEqual(operations.phases, want) {
		t.Fatalf("phases after lock-context cancellation = %v, want %v", operations.phases, want)
	}
}

func TestComposeTemplateCreateMutationLockSerializesConcurrentEmptyTargets(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	request := ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withComposeTemplateCreateMutationLock(composeCreateTestCommand(context.Background()), ctx, request, func(*cobra.Command) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- withComposeTemplateCreateMutationLock(composeCreateTestCommand(context.Background()), ctx, request, func(*cobra.Command) error {
			close(secondEntered)
			return nil
		})
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		t.Fatal("second create entered while the first create held the project mutation lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first create error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second create error = %v", err)
	}
}

func TestComposeTemplateCreateRevalidatesTargetAfterWaitingForLock(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	request := ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}
	if err := ensureComposeCreateProjectDirectory(context.Background(), ctx, request); err != nil {
		t.Fatal(err)
	}
	blocker, err := ctx.AcquireProjectMutationLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	originalAcquire := acquireComposeProjectMutationLock
	t.Cleanup(func() { acquireComposeProjectMutationLock = originalAcquire })
	waiting := make(chan struct{})
	acquireComposeProjectMutationLock = func(runCtx context.Context, target *config.Context) (*config.ProjectMutationLock, error) {
		close(waiting)
		return target.AcquireProjectMutationLock(runCtx)
	}

	var secondRan atomic.Bool
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- withComposeTemplateCreateMutationLock(composeCreateTestCommand(context.Background()), ctx, request, func(*cobra.Command) error {
			secondRan.Store(true)
			return nil
		})
	}()
	<-waiting
	if err := os.WriteFile(filepath.Join(projectDir, "winner"), []byte("claimed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Release(); err != nil {
		t.Fatal(err)
	}
	secondErr := <-secondDone
	if secondErr == nil || !strings.Contains(secondErr.Error(), "changed while create waited") {
		t.Fatalf("second create error = %v, want under-lock state-change refusal", secondErr)
	}
	if secondRan.Load() {
		t.Fatal("second create mutated the target after under-lock revalidation failed")
	}
}

func TestComposeTemplateCreateLockWaitHonorsCancellation(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	request := ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withComposeTemplateCreateMutationLock(composeCreateTestCommand(context.Background()), ctx, request, func(*cobra.Command) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	waitContext, cancel := context.WithCancel(context.Background())
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- withComposeTemplateCreateMutationLock(composeCreateTestCommand(waitContext), ctx, request, func(*cobra.Command) error {
			t.Error("cancelled create entered its mutation operation")
			return nil
		})
	}()
	<-secondStarted
	select {
	case err := <-secondDone:
		t.Fatalf("second create stopped waiting before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	err := <-secondDone
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create error = %v, want context cancellation", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first create error = %v", err)
	}
}

func TestComposeTemplateCreateReturnsProjectLockReleaseError(t *testing.T) {
	originalRelease := releaseComposeCreateProjectMutationLock
	t.Cleanup(func() { releaseComposeCreateProjectMutationLock = originalRelease })
	releaseErr := errors.New("release failed")
	releaseComposeCreateProjectMutationLock = func(lock *config.ProjectMutationLock) error {
		return errors.Join(lock.Release(), releaseErr)
	}

	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: filepath.Join(t.TempDir(), "site")}
	cmdContext := context.WithValue(context.Background(), composeCreateTestContextKey{}, "original")
	cmd := composeCreateTestCommand(cmdContext)
	err := withComposeTemplateCreateMutationLock(cmd, ctx, ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}, func(*cobra.Command) error {
		return nil
	})
	if !errors.Is(err, releaseErr) || !strings.Contains(err.Error(), "release project mutation lock") {
		t.Fatalf("withComposeTemplateCreateMutationLock() error = %v, want release failure", err)
	}
	if cmd.Context() != cmdContext {
		t.Fatal("create command context was not restored after releasing the lock")
	}
}

func TestComposeCreateObservationRejectsNestedTemplateMutation(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".libops", "config"), 0o750); err != nil {
		t.Fatal(err)
	}
	lock, err := buildTemplateLock("https://example.org/template.git", templateCheckoutMetadata{
		Commit: testTemplateCommit,
		Ref:    "main",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, filepath.FromSlash(templateLockPath)), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	nestedPath := filepath.Join(projectDir, ".libops", "config", "settings.yaml")
	if err := os.WriteFile(nestedPath, []byte("value: one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	req := ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "https://example.org/template.git",
		TemplateBranch: "main",
	}
	observation, err := (&SDK{}).PrepareComposeCreateTargetContext(context.Background(), req, ctx)
	if err != nil {
		t.Fatalf("PrepareComposeCreateTargetContext() error = %v", err)
	}
	if err := os.WriteFile(nestedPath, []byte("value: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = (&SDK{}).RevalidateComposeCreateTargetContext(context.Background(), req, ctx, observation)
	if err == nil || !strings.Contains(err.Error(), "changed while create waited") {
		t.Fatalf("nested template mutation revalidation error = %v, want compare-and-swap refusal", err)
	}
}

func TestComposeTemplateProvenanceBindsRequestedRef(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".libops"), 0o750); err != nil {
		t.Fatal(err)
	}
	lock, err := buildTemplateLock("https://example.org/template.git", templateCheckoutMetadata{
		Commit: testTemplateCommit,
		Ref:    "v1.0.0",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, filepath.FromSlash(templateLockPath)), lock, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	_, err = (&SDK{}).ValidateComposeTemplateProvenanceContext(context.Background(), ctx, ComposeCreateRequest{
		TemplateRepo:   "https://example.org/template.git",
		TemplateBranch: "v2.0.0",
	})
	if err == nil || !strings.Contains(err.Error(), "not requested ref") {
		t.Fatalf("different requested ref validation error = %v, want ref mismatch", err)
	}
}

func TestExistingComposeObservationBindsAllConfiguredInputs(t *testing.T) {
	projectDir := t.TempDir()
	for name, data := range map[string]string{
		"compose.yaml":  "services: {}\n",
		"override.yaml": "services: {}\n",
		"site.env":      "SITE_NAME=one\n",
	} {
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &config.Context{
		DockerHostType: config.ContextLocal,
		ProjectDir:     projectDir,
		ComposeFile:    []string{"compose.yaml", "override.yaml"},
		EnvFile:        []string{"site.env"},
	}
	req := ComposeCreateRequest{CheckoutSource: CheckoutSourceExisting}
	observation, err := (&SDK{}).PrepareComposeCreateTargetContext(context.Background(), req, ctx)
	if err != nil {
		t.Fatalf("PrepareComposeCreateTargetContext() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "override.yaml"), []byte("services: {app: {}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = (&SDK{}).RevalidateComposeCreateTargetContext(context.Background(), req, ctx, observation)
	if err == nil || !strings.Contains(err.Error(), "changed while create waited") {
		t.Fatalf("configured Compose input mutation error = %v, want compare-and-swap refusal", err)
	}
}

func TestExistingComposeObservationRejectsNestedPluginMutation(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".libops"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	desiredState := filepath.Join(projectDir, ".libops", "desired-state.yaml")
	if err := os.WriteFile(desiredState, []byte("components: one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	req := ComposeCreateRequest{CheckoutSource: CheckoutSourceExisting}
	observation, err := (&SDK{}).PrepareComposeCreateTargetContext(context.Background(), req, ctx)
	if err != nil {
		t.Fatalf("PrepareComposeCreateTargetContext() error = %v", err)
	}
	if err := os.WriteFile(desiredState, []byte("components: two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = (&SDK{}).RevalidateComposeCreateTargetContext(context.Background(), req, ctx, observation)
	if err == nil || !strings.Contains(err.Error(), "changed while create waited") {
		t.Fatalf("nested plugin mutation error = %v, want compare-and-swap refusal", err)
	}
}

func TestRemoteComposeCreateRootIdentityDetectsReplacement(t *testing.T) {
	identity, err := parseRemoteComposeCreateRootIdentity("2049:987654\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := "posix:2049:987654"; identity != want {
		t.Fatalf("remote root identity = %q, want %q", identity, want)
	}
	want := ComposeCreateTargetObservation{
		remote:           true,
		remoteRoot:       identity,
		rootMode:         os.ModeDir | 0o750,
		rootModification: time.Unix(1, 0),
	}
	got := want
	got.remoteRoot = "posix:2049:987655"
	if sameComposeCreateRoot(want, got) {
		t.Fatal("remote project root replacement retained the same create observation identity")
	}
	if _, err := parseRemoteComposeCreateRootIdentity("not-an-inode"); err == nil {
		t.Fatal("invalid remote root device/inode identity was accepted")
	}
}

func TestClaimedLocalTemplateCloneFailureCleansOnlyStagingDirectory(t *testing.T) {
	originalRunner := runGitCommandContext
	t.Cleanup(func() { runGitCommandContext = originalRunner })
	cloneErr := errors.New("clone failed")
	runGitCommandContext = func(_ context.Context, _, _ io.Writer, name string, args ...string) error {
		if name != "git" || len(args) == 0 || args[0] != "clone" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		stagingPath := args[len(args)-1]
		if err := os.WriteFile(filepath.Join(stagingPath, "partial"), []byte("partial\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return cloneErr
	}

	projectDir := t.TempDir()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	created, err := (&SDK{}).ensureClaimedComposeTemplateCheckoutContext(context.Background(), io.Discard, ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "https://example.org/template.git",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), cloneErr.Error()) {
		t.Fatalf("ensureClaimedComposeTemplateCheckoutContext() error = %v", err)
	}
	if created {
		t.Fatal("failed staged checkout reported created")
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil {
		t.Fatalf("claimed project root was removed: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed staged checkout left project contents: %v", entries)
	}
}

func TestComposeTemplateCheckoutRejectsForgedSourceLockWithoutPoisoningRetry(t *testing.T) {
	originalRunner := runGitCommandContext
	t.Cleanup(func() { runGitCommandContext = originalRunner })
	attempt := 0
	runGitCommandContext = func(_ context.Context, stdout, _ io.Writer, name string, args ...string) error {
		if name != "git" || len(args) == 0 {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		switch {
		case args[0] == "clone":
			attempt++
			stagingPath := args[len(args)-1]
			if err := os.Mkdir(filepath.Join(stagingPath, ".git"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(stagingPath, ".libops"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if attempt == 1 {
				if err := os.WriteFile(filepath.Join(stagingPath, filepath.FromSlash(templateLockPath)), []byte("forged-source-lock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		case len(args) > 2 && args[0] == "-C" && args[2] == "rev-parse":
			_, _ = io.WriteString(stdout, testTemplateCommit+"\n")
			return nil
		case len(args) > 2 && args[0] == "-C" && args[2] == "init":
			return os.Mkdir(filepath.Join(args[1], ".git"), 0o750)
		default:
			t.Fatalf("unexpected git args: %v", args)
			return nil
		}
	}

	projectDir := t.TempDir()
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	request := ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "https://example.org/template.git",
		TemplateBranch: "main",
	}
	created, err := (&SDK{}).EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, request, ctx)
	if err == nil || !strings.Contains(err.Error(), "source template must not contain") {
		t.Fatalf("forged source lock checkout error = %v", err)
	}
	if created {
		t.Fatal("forged source lock checkout reported created")
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("forged source checkout poisoned target: entries=%v err=%v", entries, readErr)
	}

	created, err = (&SDK{}).EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, request, ctx)
	if err != nil {
		t.Fatalf("clean retry checkout error = %v", err)
	}
	if !created {
		t.Fatal("clean retry checkout reported created = false")
	}
	lockData, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(templateLockPath)))
	if err != nil {
		t.Fatalf("read published template lock: %v", err)
	}
	if strings.Contains(string(lockData), "forged-source-lock") {
		t.Fatalf("published template lock retained forged source content: %s", lockData)
	}
}

func TestPublishLocalComposeCreateStagingPreservesClaimedRoot(t *testing.T) {
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeComposeCreateTestProvenance(t, stagingPath)
	if err := publishLocalComposeCreateStaging(context.Background(), projectDir, stagingPath); err != nil {
		t.Fatalf("publishLocalComposeCreateStaging() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "compose.yaml")); err != nil {
		t.Fatalf("published Compose file is missing: %v", err)
	}
	if _, err := os.Lstat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("staging directory remains after publish: %v", err)
	}
}

func TestRemoteComposeCreateClaimsAndRevalidatesEmptyTarget(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	connection := &localRemoteTemplateConnection{}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	request := ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}
	if err := ensureComposeCreateProjectDirectory(context.Background(), ctx, request); err != nil {
		t.Fatalf("ensureComposeCreateProjectDirectory() error = %v", err)
	}
	observation, err := (&SDK{}).PrepareComposeCreateTargetContext(context.Background(), request, ctx)
	if err != nil {
		t.Fatalf("PrepareComposeCreateTargetContext() error = %v", err)
	}
	if !observation.IsEmpty() {
		t.Fatal("claimed remote create target was not observed as empty")
	}
	info, err := os.Lstat(projectDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Fatalf("remote claimed project directory info = %v, err = %v", info, err)
	}
}

func TestRemoteComposeCreateClaimAcceptsConcurrentEmptyWinner(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "site")
	connection := &localRemoteTemplateConnection{
		mkdir: func(directory string) error {
			if directory != projectDir {
				t.Fatalf("claimed remote directory = %q, want %q", directory, projectDir)
			}
			if err := os.Mkdir(directory, 0o750); err != nil {
				t.Fatal(err)
			}
			return os.ErrExist
		},
	}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	request := ComposeCreateRequest{CheckoutSource: CheckoutSourceTemplate, TemplateRepo: composeCreateTestTemplateRepo}
	if err := ensureComposeCreateProjectDirectory(context.Background(), ctx, request); err != nil {
		t.Fatalf("ensureComposeCreateProjectDirectory() error = %v", err)
	}
	observation, err := (&SDK{}).PrepareComposeCreateTargetContext(context.Background(), request, ctx)
	if err != nil {
		t.Fatalf("PrepareComposeCreateTargetContext() error = %v", err)
	}
	if !observation.IsEmpty() {
		t.Fatal("concurrent empty winner was not observed as an empty target")
	}
}

func TestClaimedRemoteTemplateCloneFailureCleansOnlyStagingDirectory(t *testing.T) {
	projectDir := t.TempDir()
	cloneErr := errors.New("clone failed")
	connection := &localRemoteTemplateConnection{}
	connection.run = func(args []string) (string, error) {
		switch {
		case len(args) > 1 && args[0] == "git" && args[1] == "clone":
			stagingPath := args[len(args)-1]
			if err := os.WriteFile(filepath.Join(stagingPath, "partial"), []byte("partial\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "", cloneErr
		case len(args) == 4 && args[0] == "rm" && args[1] == "-rf" && args[2] == "--":
			return "", os.RemoveAll(args[3])
		default:
			t.Fatalf("unexpected remote command args: %v", args)
			return "", nil
		}
	}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	created, err := (&SDK{}).ensureClaimedComposeTemplateCheckoutContext(context.Background(), io.Discard, ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "https://example.org/template.git",
	}, ctx)
	if err == nil || !strings.Contains(err.Error(), cloneErr.Error()) {
		t.Fatalf("ensureClaimedComposeTemplateCheckoutContext() error = %v", err)
	}
	if created {
		t.Fatal("failed staged remote checkout reported created")
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil {
		t.Fatalf("claimed remote project root was removed: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed staged remote checkout left project contents: %v", entries)
	}
}

func TestRemoteComposeTemplateCheckoutRejectsForgedSourceLockWithoutPoisoningRetry(t *testing.T) {
	useNoopComposeCreateProjectMutationLock(t)
	projectDir := filepath.Join(t.TempDir(), "site")
	attempt := 0
	connection := &localRemoteTemplateConnection{}
	connection.run = func(args []string) (string, error) {
		switch {
		case len(args) > 1 && args[0] == "git" && args[1] == "clone":
			attempt++
			stagingPath := args[len(args)-1]
			createRemoteGitDirectory(t, stagingPath)
			if err := os.Mkdir(filepath.Join(stagingPath, ".libops"), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if attempt == 1 {
				if err := os.WriteFile(filepath.Join(stagingPath, filepath.FromSlash(templateLockPath)), []byte("forged-source-lock\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			return "", nil
		case len(args) > 3 && args[0] == "git" && args[1] == "-C" && args[3] == "rev-parse":
			return testTemplateCommit, nil
		case len(args) == 4 && args[0] == "rm" && args[1] == "-rf" && args[2] == "--":
			return "", os.RemoveAll(args[3])
		case len(args) > 3 && args[0] == "git" && args[1] == "-C" && args[3] == "init":
			return "", os.Mkdir(filepath.Join(args[2], ".git"), 0o750)
		default:
			t.Fatalf("unexpected remote command args: %v", args)
			return "", nil
		}
	}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	request := ComposeCreateRequest{
		CheckoutSource: CheckoutSourceTemplate,
		TemplateRepo:   "https://example.org/template.git",
		TemplateBranch: "main",
	}
	created, err := (&SDK{}).EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, request, ctx)
	if err == nil || !strings.Contains(err.Error(), "source template must not contain") {
		t.Fatalf("forged remote source lock checkout error = %v", err)
	}
	if created {
		t.Fatal("forged remote source lock checkout reported created")
	}
	entries, readErr := os.ReadDir(projectDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("forged remote source checkout poisoned target: entries=%v err=%v", entries, readErr)
	}

	created, err = (&SDK{}).EnsureComposeTemplateCheckoutContext(context.Background(), io.Discard, request, ctx)
	if err != nil {
		t.Fatalf("clean remote retry checkout error = %v", err)
	}
	if !created {
		t.Fatal("clean remote retry checkout reported created = false")
	}
	lockData, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(templateLockPath)))
	if err != nil {
		t.Fatalf("read published remote template lock: %v", err)
	}
	if strings.Contains(string(lockData), "forged-source-lock") {
		t.Fatalf("published remote template lock retained forged source content: %s", lockData)
	}
}

func TestPublishRemoteComposeCreateStagingPreservesClaimedRoot(t *testing.T) {
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeComposeCreateTestProvenance(t, stagingPath)
	connection := &localRemoteTemplateConnection{}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	if err := publishRemoteComposeCreateStaging(context.Background(), ctx, filepath.ToSlash(stagingPath)); err != nil {
		t.Fatalf("publishRemoteComposeCreateStaging() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "compose.yaml")); err != nil {
		t.Fatalf("published remote Compose file is missing: %v", err)
	}
	if _, err := os.Lstat(stagingPath); !os.IsNotExist(err) {
		t.Fatalf("remote staging directory remains after publish: %v", err)
	}
}

func TestPublishRemoteComposeCreateStagingPublishesProvenanceLastAndRollsBackCancellation(t *testing.T) {
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeComposeCreateTestProvenance(t, stagingPath)

	runCtx, cancel := context.WithCancel(context.Background())
	var published []string
	connection := &localRemoteTemplateConnection{}
	connection.rename = func(oldName, newName string) error {
		name := filepath.Base(oldName)
		if filepath.Clean(filepath.Dir(oldName)) == filepath.Clean(stagingPath) {
			published = append(published, name)
		}
		if err := os.Rename(oldName, newName); err != nil {
			return err
		}
		if name == ".libops" && filepath.Clean(filepath.Dir(newName)) == filepath.Clean(projectDir) {
			cancel()
		}
		return nil
	}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	err = publishRemoteComposeCreateStaging(runCtx, ctx, filepath.ToSlash(stagingPath))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publishRemoteComposeCreateStaging() error = %v, want cancellation", err)
	}
	if got, want := strings.Join(published, ","), "compose.yaml,.libops"; got != want {
		t.Fatalf("published entries = %q, want %q", got, want)
	}
	rootEntries, readErr := os.ReadDir(projectDir)
	if readErr != nil || len(rootEntries) != 1 || rootEntries[0].Name() != filepath.Base(stagingPath) {
		t.Fatalf("cancelled publish did not roll back to staging: entries=%v err=%v", rootEntries, readErr)
	}
}

func TestPublishRemoteComposeCreateStagingPreservesPartialTreeWhenLockIsLost(t *testing.T) {
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeComposeCreateTestProvenance(t, stagingPath)

	runCtx, cancel := context.WithCancelCause(context.Background())
	connection := &localRemoteTemplateConnection{}
	connection.rename = func(oldName, newName string) error {
		if err := os.Rename(oldName, newName); err != nil {
			return err
		}
		if filepath.Base(oldName) == "compose.yaml" && filepath.Clean(filepath.Dir(oldName)) == filepath.Clean(stagingPath) {
			cancel(config.ErrProjectMutationLockLost)
		}
		return nil
	}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	err = publishRemoteComposeCreateStaging(runCtx, ctx, filepath.ToSlash(stagingPath))
	if !errors.Is(err, config.ErrProjectMutationLockLost) || !errors.Is(err, errComposeCreateRecoveryRequired) {
		t.Fatalf("publishRemoteComposeCreateStaging() error = %v, want lock-loss recovery error", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "compose.yaml")); err != nil {
		t.Fatalf("partial published entry was unexpectedly rolled back after lock loss: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingPath, filepath.FromSlash(templateLockPath))); err != nil {
		t.Fatalf("staging provenance was not preserved after lock loss: %v", err)
	}
}

func TestPublishRemoteComposeCreateStagingPreservesIncompleteRollback(t *testing.T) {
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeComposeCreateTestProvenance(t, stagingPath)

	publishErr := errors.New("publish provenance failed")
	rollbackErr := errors.New("rollback failed")
	connection := &localRemoteTemplateConnection{}
	connection.rename = func(oldName, newName string) error {
		name := filepath.Base(oldName)
		sourceParent := filepath.Clean(filepath.Dir(oldName))
		if sourceParent == filepath.Clean(stagingPath) && name == ".libops" {
			return publishErr
		}
		if sourceParent == filepath.Clean(projectDir) && name == "compose.yaml" {
			return rollbackErr
		}
		return os.Rename(oldName, newName)
	}
	useLocalRemoteTemplateConnection(t, connection)
	ctx := &config.Context{DockerHostType: config.ContextRemote, ProjectDir: projectDir, SSHHostname: "example.invalid"}
	err = publishRemoteComposeCreateStaging(context.Background(), ctx, filepath.ToSlash(stagingPath))
	if !errors.Is(err, publishErr) || !errors.Is(err, rollbackErr) || !errors.Is(err, errComposeCreateRecoveryRequired) {
		t.Fatalf("publishRemoteComposeCreateStaging() error = %v, want incomplete rollback recovery error", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "compose.yaml")); err != nil {
		t.Fatalf("incompletely rolled back entry is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingPath, filepath.FromSlash(templateLockPath))); err != nil {
		t.Fatalf("staging provenance is missing after incomplete rollback: %v", err)
	}
}

func TestReadLocalComposeCreateDirectoryEnforcesEntryLimit(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, exceeded, err := readLocalComposeCreateDirectory(directory, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded || len(entries) != 2 {
		t.Fatalf("bounded directory read = %d entries, exceeded=%t; want 2 entries and exceeded", len(entries), exceeded)
	}
}

func TestComposeCreateTreeDigestRejectsIncompleteStagingDirectory(t *testing.T) {
	projectDir := t.TempDir()
	stagingPath, err := newComposeCreateStagingPath(projectDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := &config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}
	if _, err := composeCreateTreeDigest(context.Background(), ctx); err == nil || !strings.Contains(err.Error(), "incomplete create staging") {
		t.Fatalf("composeCreateTreeDigest() error = %v, want incomplete staging rejection", err)
	}
}

func writeComposeCreateTestProvenance(t *testing.T, stagingPath string) {
	t.Helper()
	metadataPath := filepath.Join(stagingPath, ".libops")
	if err := os.Mkdir(metadataPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPath, filepath.FromSlash(templateLockPath)), []byte("schema: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type composeCreateTestContextKey struct{}

func useNoopComposeCreateProjectMutationLock(t *testing.T) {
	t.Helper()
	originalAcquire := acquireComposeProjectMutationLock
	acquireComposeProjectMutationLock = func(runCtx context.Context, _ *config.Context) (*config.ProjectMutationLock, error) {
		return &config.ProjectMutationLock{}, runCtx.Err()
	}
	t.Cleanup(func() { acquireComposeProjectMutationLock = originalAcquire })
}

func composeCreateTestCommand(runCtx context.Context) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(runCtx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd
}

type recordingComposeTemplateCreateOperations struct {
	t           *testing.T
	ctx         *config.Context
	phases      []string
	cancelAfter string
	cancel      context.CancelFunc
}

func (o *recordingComposeTemplateCreateOperations) record(cmd *cobra.Command, phase string) {
	o.t.Helper()
	lock, err := o.ctx.AcquireProjectMutationLock(cmd.Context())
	if err != nil {
		o.t.Fatalf("phase %q did not receive the held mutation-lock context: %v", phase, err)
	}
	if err := lock.Release(); err != nil {
		o.t.Fatalf("release reentrant phase %q lock: %v", phase, err)
	}
	o.phases = append(o.phases, phase)
	if phase == o.cancelAfter && o.cancel != nil {
		o.cancel()
	}
}

func (o *recordingComposeTemplateCreateOperations) checkout(cmd *cobra.Command, _ *config.Context, _ ComposeCreateRequest) (bool, error) {
	o.record(cmd, "checkout")
	return true, nil
}

func (o *recordingComposeTemplateCreateOperations) refreshContext(cmd *cobra.Command, _ *config.Context, _ ComposeCreateRequest) error {
	o.record(cmd, "refresh")
	return nil
}

func (o *recordingComposeTemplateCreateOperations) reconcileComponents(cmd *cobra.Command, _ *config.Context, _ map[string]corecomponent.ReviewDecision) error {
	o.record(cmd, "reconcile")
	return nil
}

func (o *recordingComposeTemplateCreateOperations) applyImageOverrides(cmd *cobra.Command, _ *config.Context, _ ComposeImageOverrides) error {
	o.record(cmd, "overrides")
	return nil
}

func (o *recordingComposeTemplateCreateOperations) needsInit(cmd *cobra.Command, _ *config.Context, _ CreateSpec) (bool, error) {
	o.record(cmd, "needs-init")
	return true, nil
}

func (o *recordingComposeTemplateCreateOperations) runCommands(cmd *cobra.Command, _ *config.Context, commands []string) error {
	o.record(cmd, "commands:"+strings.Join(commands, ","))
	return nil
}

func (o *recordingComposeTemplateCreateOperations) printSummary(cmd *cobra.Command, _ *config.Context, _ string, _ bool) error {
	o.record(cmd, "summary")
	return nil
}
