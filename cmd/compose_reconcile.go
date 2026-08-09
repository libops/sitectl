package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/plugin"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

const (
	composeReconcileCacheVersion = 2
	composeReconcileCacheTTL     = 7 * 24 * time.Hour
)

type composeReconcileCache struct {
	Version int                                   `json:"version"`
	Entries map[string]composeReconcileCacheEntry `json:"entries"`
}

type composeReconcileCacheEntry struct {
	Host               string                      `json:"host"`
	Plugin             string                      `json:"plugin"`
	ProjectDir         string                      `json:"project_dir"`
	ObservedGeneration string                      `json:"observed_generation"`
	Reason             string                      `json:"reason"`
	Conditions         []composeReconcileCondition `json:"conditions,omitempty"`
	CheckedAt          time.Time                   `json:"checked_at"`
}

type composeReconcileDecision struct {
	Needed   bool
	RunInit  bool
	RunBuild bool
	Reason   string
	Status   composeReconcileStatus
	Spec     plugin.CreateSpec
}

type composeReconcileOptions struct {
	Force     bool
	ResetInit bool
	Yolo      bool
}

type composeReconcileStatus struct {
	Conditions []composeReconcileCondition `json:"conditions,omitempty"`
}

type composeReconcileCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

const (
	conditionStatusTrue  = "True"
	conditionStatusFalse = "False"

	conditionInitialized     = "Initialized"
	conditionImagesAvailable = "ImagesAvailable"
	conditionReconciled      = "Reconciled"
)

type composeConfigDocument struct {
	Services map[string]composeConfigService `json:"services"`
	Secrets  map[string]composeConfigSecret  `json:"secrets"`
	Volumes  map[string]composeConfigVolume  `json:"volumes"`
	Name     string                          `json:"name"`
}

type composeConfigService struct {
	Image   string                       `json:"image"`
	Build   json.RawMessage              `json:"build"`
	Volumes []composeConfigServiceVolume `json:"volumes"`
}

type composeConfigSecret struct {
	File string `json:"file"`
}

type composeConfigVolume struct {
	Name     string `json:"name"`
	External bool   `json:"external"`
}

type composeConfigServiceVolume struct {
	Type   string `json:"type"`
	Source string `json:"source"`
}

type composeImageOverrideDocument struct {
	Services map[string]composeImageOverrideService `yaml:"services"`
}

type composeImageOverrideService struct {
	Image string                    `yaml:"image"`
	Build composeImageOverrideBuild `yaml:"build"`
}

type composeImageOverrideBuild struct {
	Args map[string]any `yaml:"args"`
}

var (
	composeReconcileHost          = os.Hostname
	composeReconcileNow           = time.Now
	composeReconcileSpec          = composeReconcileCreateSpec
	composeReconcileNeed          = inspectComposeReconcileNeed
	composeReconcileRun           = runComposeReconcileCommands
	composeReconcileHit           = composeReconcileChecked
	composeReconcileMark          = markComposeReconcileChecked
	composeReconcileClear         = clearComposeReconcileCacheEntry
	composeReconcileReset         = resetComposeReconcileInitState
	composeReconcileImageMissing  = dockerImageMissing
	composeReconcileVolumeMissing = dockerVolumeMissing
	composeReconcileVolumeRemove  = dockerVolumeRemove
	composeReconcileRemoveFile    = os.Remove
	composeReconcileReadConfig    = readComposeConfigDocument
	composeReconcileUserID        = currentComposeReconcileUserID
	composeReconcileLocalIdentity = config.LocalComposeHostNumericIdentity
	composeReconcileInput         = config.GetInput
	composeReconcileAcquireLock   = func(runCtx context.Context, ctx *config.Context) (*config.ProjectMutationLock, error) {
		return ctx.AcquireProjectMutationLock(runCtx)
	}
)

var composeReconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Run plugin Docker Compose init/build/up reconciliation",
	Long: `Run plugin Docker Compose init/build/up reconciliation.

This is the same lifecycle repair path sitectl runs automatically before a
plain full-stack 'sitectl compose up' for plugin-managed local Compose projects.
Starts with additional Compose flags or selected services pass through unchanged;
run this command explicitly first when those starts also need lifecycle repair.
Use --force to rerun build/up even when the project is cached as current. Use
--reset-init to remove plugin-declared init artifacts and init volumes before
reconciling. Because that operation destroys data, it requires a typed
confirmation unless --yolo is supplied.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, err := cmd.Flags().GetBool("force")
		if err != nil {
			return err
		}
		resetInit, err := cmd.Flags().GetBool("reset-init")
		if err != nil {
			return err
		}
		yolo, err := cmd.Flags().GetBool("yolo")
		if err != nil {
			return err
		}
		return runComposeReconcileCommand(cmd, composeReconcileOptions{
			Force:     force,
			ResetInit: resetInit,
			Yolo:      yolo,
		})
	},
}

func init() {
	composeReconcileCmd.Flags().Bool("force", false, "Ignore the reconcile cache and rerun build/up.")
	composeReconcileCmd.Flags().Bool("reset-init", false, "Remove plugin-declared init artifacts and init volumes before reconciling.")
	composeReconcileCmd.Flags().Bool("yolo", false, "Skip the destructive --reset-init confirmation prompt.")
	composeCmd.AddCommand(composeReconcileCmd)
}

func (s composeReconcileStatus) needsInit() bool {
	return s.conditionFalse(conditionInitialized)
}

func (s composeReconcileStatus) needsBuild() bool {
	return s.conditionFalse(conditionImagesAvailable)
}

func (s composeReconcileStatus) conditionFalse(conditionType string) bool {
	for _, condition := range s.Conditions {
		if condition.Type == conditionType && condition.Status == conditionStatusFalse {
			return true
		}
	}
	return false
}

func (s composeReconcileStatus) summary() string {
	var parts []string
	for _, condition := range s.Conditions {
		if condition.Status != conditionStatusFalse {
			continue
		}
		message := strings.TrimSpace(condition.Message)
		if message == "" {
			message = strings.TrimSpace(condition.Reason)
		}
		if message != "" {
			parts = append(parts, message)
		}
	}
	if len(parts) == 0 {
		return "conditions satisfied"
	}
	return strings.Join(parts, "; ")
}

func maybeRunComposeReconcile(cmd *cobra.Command, ctx *config.Context) (handled bool, returnErr error) {
	if cmd == nil || ctx == nil || ctx.DockerHostType != config.ContextLocal {
		return false, nil
	}
	if strings.TrimSpace(ctx.Plugin) == "" || strings.TrimSpace(ctx.Plugin) == "core" {
		return false, nil
	}
	lock, err := composeReconcileAcquireLock(cmd.Context(), ctx)
	if err != nil {
		return false, fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()

	decision, err := composeReconcileDecisionForContext(ctx)
	if err != nil {
		return false, err
	}
	if !decision.Needed {
		return false, nil
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "sitectl: running reconcile for %s (%s)\n", ctx.Plugin, decision.Reason)
	if err := composeReconcileRun(cmd, ctx, decision); err != nil {
		return false, err
	}
	if err := composeReconcileMark(ctx, decision.Status, decision.Spec); err != nil {
		return false, err
	}
	return true, nil
}

func runComposeReconcileCommand(cmd *cobra.Command, opts composeReconcileOptions) (returnErr error) {
	ctx, err := resolveCurrentContext(cmd)
	if err != nil {
		return err
	}
	if ctx.DockerHostType != config.ContextLocal {
		return fmt.Errorf("compose reconcile currently requires a local context")
	}
	if strings.TrimSpace(ctx.Plugin) == "" || strings.TrimSpace(ctx.Plugin) == "core" {
		return fmt.Errorf("context %q is not managed by a plugin", ctx.Name)
	}
	if strings.TrimSpace(ctx.ProjectDir) == "" {
		return fmt.Errorf("context %q does not define a project directory", ctx.Name)
	}
	spec, ok, err := composeReconcileSpec(strings.TrimSpace(ctx.Plugin))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("plugin %q does not define a create lifecycle", ctx.Plugin)
	}
	if opts.ResetInit {
		// Do not hold the project mutation lock while waiting for an operator.
		// The reset plan is rebuilt from Compose while holding the lock below.
		if err := confirmComposeReconcileReset(ctx, opts.Yolo); err != nil {
			return err
		}
	}
	lock, err := composeReconcileAcquireLock(cmd.Context(), ctx)
	if err != nil {
		return fmt.Errorf("acquire project mutation lock: %w", err)
	}
	originalContext := cmd.Context()
	cmd.SetContext(lock.Context())
	defer func() {
		cmd.SetContext(originalContext)
		returnErr = errors.Join(returnErr, lock.Release())
	}()

	if opts.ResetInit {
		removed, err := composeReconcileReset(ctx, spec)
		if err != nil {
			return err
		}
		for _, name := range removed {
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", name)
		}
		opts.Force = true
	}
	if opts.Force {
		if err := composeReconcileClear(ctx, spec); err != nil {
			return err
		}
	}

	decision, err := composeReconcileDecisionForContextWithOptions(ctx, opts)
	if err != nil {
		return err
	}
	if !decision.Needed {
		fmt.Fprintln(cmd.OutOrStdout(), "Compose reconcile is current")
		return nil
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "sitectl: running reconcile for %s (%s)\n", ctx.Plugin, decision.Reason)
	if err := composeReconcileRun(cmd, ctx, decision); err != nil {
		return err
	}
	if err := composeReconcileMark(ctx, decision.Status, decision.Spec); err != nil {
		return err
	}
	return nil
}

func confirmComposeReconcileReset(ctx *config.Context, yolo bool) error {
	if yolo {
		return nil
	}
	contextName := strings.TrimSpace(ctx.Name)
	if contextName == "" {
		contextName = "this context"
	}
	token := "reset " + contextName
	input, err := composeReconcileInput(
		fmt.Sprintf("This will permanently remove declared initialization files and local named volumes for %q.", contextName),
		fmt.Sprintf("Project directory: %s", strings.TrimSpace(ctx.ProjectDir)),
		"Database contents, uploaded files, generated secrets, and certificates in those declared resources can be lost.",
		fmt.Sprintf("Type %q to continue: ", token),
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input) != token {
		return fmt.Errorf("compose reconcile reset cancelled")
	}
	return nil
}

func composeReconcileDecisionForContext(ctx *config.Context) (composeReconcileDecision, error) {
	return composeReconcileDecisionForContextWithOptions(ctx, composeReconcileOptions{})
}

func composeReconcileDecisionForContextWithOptions(ctx *config.Context, opts composeReconcileOptions) (composeReconcileDecision, error) {
	spec, ok, err := composeReconcileSpec(strings.TrimSpace(ctx.Plugin))
	if err != nil || !ok {
		return composeReconcileDecision{}, err
	}

	if !opts.Force && !composeSpecAlwaysBuilds(spec) {
		cached, err := composeReconcileHit(ctx, spec)
		if err != nil {
			return composeReconcileDecision{}, err
		}
		if cached {
			return composeReconcileDecision{Spec: spec}, nil
		}
	}

	status, err := composeReconcileNeed(ctx, spec)
	if err != nil {
		return composeReconcileDecision{}, err
	}
	runInit := status.needsInit()
	runBuild := status.needsBuild() || (runInit && len(spec.DockerComposeBuild) > 0)
	if opts.Force {
		runBuild = runBuild || len(spec.DockerComposeBuild) > 0
		if runInit || runBuild || len(spec.DockerComposeUp) > 0 {
			reason := status.summary()
			if reason == "conditions satisfied" {
				reason = "forced"
			}
			return composeReconcileDecision{
				Needed:   true,
				RunInit:  runInit,
				RunBuild: runBuild,
				Reason:   reason,
				Status:   status,
				Spec:     spec,
			}, nil
		}
	}
	if !runInit && !runBuild {
		if err := composeReconcileMark(ctx, status, spec); err != nil {
			return composeReconcileDecision{}, err
		}
		return composeReconcileDecision{Spec: spec, Status: status}, nil
	}
	return composeReconcileDecision{
		Needed:   true,
		RunInit:  runInit,
		RunBuild: runBuild,
		Reason:   status.summary(),
		Status:   status,
		Spec:     spec,
	}, nil
}

func composeSpecAlwaysBuilds(spec plugin.CreateSpec) bool {
	for _, image := range spec.Images {
		if image.BuildPolicy == plugin.BuildPolicyAlways {
			return true
		}
	}
	return false
}

func composeReconcileCreateSpec(pluginName string) (plugin.CreateSpec, bool, error) {
	installed, ok := plugin.FindInstalled(pluginName)
	if !ok || len(installed.CreateDefinitions) == 0 {
		return plugin.CreateSpec{}, false, nil
	}
	for _, spec := range installed.CreateDefinitions {
		if spec.Default {
			return spec, true, nil
		}
	}
	return installed.CreateDefinitions[0], true, nil
}

func inspectComposeReconcileNeed(ctx *config.Context, spec plugin.CreateSpec) (composeReconcileStatus, error) {
	if len(spec.DockerComposeInit) == 0 && len(spec.DockerComposeBuild) == 0 {
		return composeReconcileStatus{Conditions: []composeReconcileCondition{{
			Type:    conditionReconciled,
			Status:  conditionStatusTrue,
			Reason:  "NoLifecycleSpec",
			Message: "plugin does not define reconcile commands",
		}}}, nil
	}

	if len(spec.InitArtifacts) > 0 || len(spec.InitVolumes) > 0 || len(spec.Images) > 0 {
		return inspectExplicitComposeReconcileNeed(ctx, spec), nil
	}

	return inspectComposeConfigReconcileNeed(ctx, spec)
}

func inspectExplicitComposeReconcileNeed(ctx *config.Context, spec plugin.CreateSpec) composeReconcileStatus {
	var initMessages []string
	var imageMessages []string
	imageOverrides, buildArgOverrides := composeImageOverrideServices(ctx)
	declaredArtifacts := map[string]bool{}
	declaredVolumes := map[string]bool{}

	for _, artifact := range spec.InitArtifacts {
		path := composeProjectPath(ctx, artifact.Path)
		declaredArtifacts[filepath.Clean(path)] = true
		if artifact.ValueFrom == plugin.InitArtifactValueFromHostUID {
			needsInit, reason := hostUIDArtifactNeedsInit(ctx, artifact)
			if needsInit {
				initMessages = append(initMessages, reason)
			}
			continue
		}
		if fileMissingOrEmpty(path) {
			initMessages = append(initMessages, fmt.Sprintf("%s is missing", artifact.Path))
		}
	}
	if len(spec.InitVolumes) > 0 {
		composeConfig, err := composeReconcileReadConfig(ctx)
		if err != nil {
			initMessages = append(initMessages, "docker compose config could not be inspected")
		} else {
			configuredVolumes := composeConfiguredVolumeNames(ctx, composeConfig)
			for _, volume := range spec.InitVolumes {
				declaredVolumes[volume.Name] = true
				dockerVolume, ok := configuredVolumes[volume.Name]
				if !ok {
					initMessages = append(initMessages, fmt.Sprintf("volume %s is not defined", volume.Name))
					continue
				}
				if composeReconcileVolumeMissing(dockerVolume) {
					initMessages = append(initMessages, fmt.Sprintf("volume %s is missing", dockerVolume))
				}
			}
		}
	}
	// Compose is authoritative for generic secrets and volumes. Plugin metadata
	// remains additive for genuinely application-specific artifacts such as a
	// host UID marker; forks can add Compose resources without updating a plugin.
	if composeConfig, err := composeReconcileReadConfig(ctx); err != nil {
		initMessages = append(initMessages, "docker compose config could not be inspected")
	} else {
		for name, secret := range composeConfig.Secrets {
			if strings.TrimSpace(secret.File) == "" {
				continue
			}
			path := composeProjectPath(ctx, secret.File)
			if !declaredArtifacts[filepath.Clean(path)] && fileMissingOrEmpty(path) {
				initMessages = append(initMessages, fmt.Sprintf("secret %s is missing", name))
			}
		}
		configuredVolumes := composeConfiguredVolumeNames(ctx, composeConfig)
		for logical, volume := range configuredVolumes {
			if !declaredVolumes[logical] && composeReconcileVolumeMissing(volume) {
				initMessages = append(initMessages, fmt.Sprintf("volume %s is missing", volume))
			}
		}
	}
	for _, imageSpec := range spec.Images {
		if imageSpec.BuildPolicy == plugin.BuildPolicyNever {
			continue
		}
		if imageOverrides[imageSpec.Service] != "" {
			continue
		}
		if imageSpec.BuildPolicy == plugin.BuildPolicyAlways {
			imageMessages = append(imageMessages, fmt.Sprintf("build policy for %s is Always", imageSpec.Service))
			continue
		}
		if composeReconcileImageMissing(imageSpec.Image) {
			imageMessages = append(imageMessages, fmt.Sprintf("image %s is missing", imageSpec.Image))
			continue
		}
		if buildArgOverrides[imageSpec.Service] {
			imageMessages = append(imageMessages, fmt.Sprintf("build args for %s need applying", imageSpec.Service))
		}
	}

	status := composeReconcileStatus{}
	if len(spec.InitArtifacts) > 0 || len(spec.InitVolumes) > 0 {
		status.Conditions = append(status.Conditions, conditionFromMessages(conditionInitialized, "InitStatePresent", "InitStateMissing", initMessages))
	}
	if len(spec.Images) > 0 {
		status.Conditions = append(status.Conditions, conditionFromMessages(conditionImagesAvailable, "ImagesAvailable", "ImageBuildRequired", imageMessages))
	}
	if len(status.Conditions) == 0 {
		status.Conditions = append(status.Conditions, composeReconcileCondition{
			Type:    conditionReconciled,
			Status:  conditionStatusTrue,
			Reason:  "NoObservedResources",
			Message: "reconcile check passed",
		})
	}
	return status
}

func conditionFromMessages(conditionType, trueReason, falseReason string, messages []string) composeReconcileCondition {
	if len(messages) == 0 {
		return composeReconcileCondition{Type: conditionType, Status: conditionStatusTrue, Reason: trueReason}
	}
	return composeReconcileCondition{
		Type:    conditionType,
		Status:  conditionStatusFalse,
		Reason:  falseReason,
		Message: strings.Join(messages, "; "),
	}
}

func inspectComposeConfigReconcileNeed(ctx *config.Context, spec plugin.CreateSpec) (composeReconcileStatus, error) {
	composeConfig, err := composeReconcileReadConfig(ctx)
	if err != nil {
		conditions := []composeReconcileCondition{}
		if len(spec.DockerComposeInit) > 0 {
			conditions = append(conditions, composeReconcileCondition{Type: conditionInitialized, Status: conditionStatusFalse, Reason: "ComposeConfigUnreadable", Message: "docker compose config could not be inspected"})
		}
		if len(spec.DockerComposeBuild) > 0 {
			conditions = append(conditions, composeReconcileCondition{Type: conditionImagesAvailable, Status: conditionStatusFalse, Reason: "ComposeConfigUnreadable", Message: "docker compose config could not be inspected"})
		}
		return composeReconcileStatus{Conditions: conditions}, nil
	}
	for name, secret := range composeConfig.Secrets {
		if strings.TrimSpace(secret.File) == "" {
			continue
		}
		secretPath := secret.File
		if !filepath.IsAbs(secretPath) {
			secretPath = filepath.Join(ctx.ProjectDir, secretPath)
		}
		if fileMissingOrEmpty(secretPath) {
			return composeReconcileStatus{Conditions: []composeReconcileCondition{{
				Type:    conditionInitialized,
				Status:  conditionStatusFalse,
				Reason:  "InitArtifactMissing",
				Message: fmt.Sprintf("secret %s is missing", name),
			}}}, nil
		}
	}
	for source, dockerVolume := range composeServiceVolumeNames(ctx, composeConfig) {
		if composeReconcileVolumeMissing(dockerVolume) {
			return composeReconcileStatus{Conditions: []composeReconcileCondition{{
				Type:    conditionInitialized,
				Status:  conditionStatusFalse,
				Reason:  "InitVolumeMissing",
				Message: fmt.Sprintf("volume %s is missing", source),
			}}}, nil
		}
	}
	for serviceName, service := range composeConfig.Services {
		if !serviceHasBuild(service) || strings.TrimSpace(service.Image) == "" {
			continue
		}
		if composeReconcileImageMissing(service.Image) {
			return composeReconcileStatus{Conditions: []composeReconcileCondition{{
				Type:    conditionImagesAvailable,
				Status:  conditionStatusFalse,
				Reason:  "ImageMissing",
				Message: fmt.Sprintf("image for %s is missing", serviceName),
			}}}, nil
		}
	}
	return composeReconcileStatus{Conditions: []composeReconcileCondition{{
		Type:    conditionReconciled,
		Status:  conditionStatusTrue,
		Reason:  "Observed",
		Message: "reconcile check passed",
	}}}, nil
}

func readComposeConfigDocument(ctx *config.Context) (composeConfigDocument, error) {
	args := []string{"compose"}
	args = append(args, ctx.DockerComposeGlobalArgs()...)
	args = append(args, "config", "--format", "json")

	command := exec.Command("docker", args...) // #nosec G204 -- fixed docker compose command with context-owned compose/env file arguments.
	command.Dir = ctx.ProjectDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	config.LogDockerComposeCommand(ctx, command.String())
	if err := command.Run(); err != nil {
		return composeConfigDocument{}, fmt.Errorf("docker compose config: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var document composeConfigDocument
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		return composeConfigDocument{}, fmt.Errorf("parse docker compose config json: %w", err)
	}
	return document, nil
}

func composeConfiguredVolumeNames(ctx *config.Context, document composeConfigDocument) map[string]string {
	out := map[string]string{}
	projectName := strings.TrimSpace(document.Name)
	if projectName == "" && ctx != nil {
		projectName = strings.TrimSpace(ctx.EffectiveComposeProjectName())
	}
	if projectName == "" && ctx != nil {
		projectName = strings.TrimSpace(filepath.Base(ctx.ProjectDir))
	}
	for source, volume := range document.Volumes {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		name := strings.TrimSpace(volume.Name)
		if name == "" && projectName != "" {
			name = projectName + "_" + source
		}
		if name != "" {
			out[source] = name
		}
	}
	return out
}

func composeServiceVolumeNames(ctx *config.Context, document composeConfigDocument) map[string]string {
	configured := composeConfiguredVolumeNames(ctx, document)
	out := map[string]string{}
	for _, service := range document.Services {
		for _, volume := range service.Volumes {
			if strings.TrimSpace(volume.Type) != "volume" {
				continue
			}
			source := strings.TrimSpace(volume.Source)
			if source == "" {
				continue
			}
			dockerVolume := configured[source]
			if dockerVolume == "" {
				dockerVolume = source
			}
			out[source] = dockerVolume
		}
	}
	return out
}

func serviceHasBuild(service composeConfigService) bool {
	build := bytes.TrimSpace(service.Build)
	return len(build) > 0 && !bytes.Equal(build, []byte("null"))
}

func dockerImageMissing(image string) bool {
	command := exec.Command("docker", "image", "inspect", image) // #nosec G204 -- image reference comes from docker compose config.
	command.Stdout = nil
	command.Stderr = nil
	return command.Run() != nil
}

func dockerVolumeMissing(volume string) bool {
	command := exec.Command("docker", "volume", "inspect", volume) // #nosec G204 -- volume name comes from docker compose config.
	command.Stdout = nil
	command.Stderr = nil
	return command.Run() != nil
}

func dockerVolumeRemove(volume string) error {
	volume = strings.TrimSpace(volume)
	if volume == "" || composeReconcileVolumeMissing(volume) {
		return nil
	}
	command := exec.Command("docker", "volume", "rm", volume) // #nosec G204 -- volume name comes from docker compose config.
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("remove volume %s: %w: %s", volume, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func resetComposeReconcileInitState(ctx *config.Context, spec plugin.CreateSpec) ([]string, error) {
	composeConfig, err := composeReconcileReadConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect compose volumes: %w", err)
	}
	configuredVolumes := composeConfiguredVolumeNames(ctx, composeConfig)
	volumePlan := []string{}
	requestedVolumes := map[string]bool{}
	for _, volume := range spec.InitVolumes {
		name := strings.TrimSpace(volume.Name)
		if name == "" || requestedVolumes[name] {
			continue
		}
		requestedVolumes[name] = true
		configured, ok := composeConfig.Volumes[name]
		if !ok {
			return nil, fmt.Errorf("refusing to reset undeclared Compose volume %q", name)
		}
		if configured.External {
			return nil, fmt.Errorf("refusing to reset external Compose volume %q", name)
		}
		dockerVolume := strings.TrimSpace(configuredVolumes[name])
		if dockerVolume == "" {
			return nil, fmt.Errorf("resolve declared Compose volume %q", name)
		}
		volumePlan = append(volumePlan, dockerVolume)
	}

	type artifactRemoval struct {
		label string
		path  string
	}
	artifactPlan := []artifactRemoval{}
	plannedArtifacts := map[string]bool{}
	planArtifact := func(label, configuredPath string) error {
		fullPath, err := safeComposeInitArtifactPath(ctx, configuredPath)
		if err != nil {
			return err
		}
		clean := filepath.Clean(fullPath)
		if plannedArtifacts[clean] {
			return nil
		}
		if err := ensureComposeInitArtifactExistingAncestor(ctx, filepath.Dir(fullPath), configuredPath); err != nil {
			return err
		}
		info, err := os.Lstat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect %s: %w", configuredPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to remove init artifact symlink %s", configuredPath)
		}
		if info.IsDir() {
			return fmt.Errorf("refusing to remove init artifact directory %s", configuredPath)
		}
		plannedArtifacts[clean] = true
		artifactPlan = append(artifactPlan, artifactRemoval{label: label, path: fullPath})
		return nil
	}

	for _, artifact := range spec.InitArtifacts {
		artifactPath := strings.TrimSpace(artifact.Path)
		if artifactPath == "" {
			continue
		}
		if err := planArtifact("file "+artifactPath, artifactPath); err != nil {
			return nil, err
		}
	}
	for name, secret := range composeConfig.Secrets {
		if strings.TrimSpace(secret.File) == "" {
			continue
		}
		if err := planArtifact("file "+secret.File, secret.File); err != nil {
			return nil, fmt.Errorf("refusing unsafe Compose secret %q: %w", name, err)
		}
	}

	removed := make([]string, 0, len(artifactPlan)+len(volumePlan))
	for _, artifact := range artifactPlan {
		if err := composeReconcileRemoveFile(artifact.path); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("remove %s: %w", artifact.label, err)
		}
		removed = append(removed, artifact.label)
	}
	for _, dockerVolume := range volumePlan {
		if err := composeReconcileVolumeRemove(dockerVolume); err != nil {
			return removed, err
		}
		removed = append(removed, "volume "+dockerVolume)
	}
	return removed, nil
}

func hostUIDArtifactNeedsInit(ctx *config.Context, artifact plugin.InitArtifact) (bool, string) {
	userID := strings.TrimSpace(composeReconcileUserID())
	if userID == "" || userID == "unknown" {
		if fileMissingOrEmpty(composeProjectPath(ctx, artifact.Path)) {
			return true, fmt.Sprintf("%s is missing", artifact.Path)
		}
		return false, ""
	}
	data, err := os.ReadFile(composeProjectPath(ctx, artifact.Path)) // #nosec G304 -- path is local plugin metadata relative to the project.
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return true, fmt.Sprintf("%s is missing", artifact.Path)
	}
	if strings.TrimSpace(string(data)) != userID {
		return true, fmt.Sprintf("%s does not match host uid %s", artifact.Path, userID)
	}
	return false, ""
}

func composeProjectPath(ctx *config.Context, path string) string {
	path = strings.TrimSpace(path)
	if filepath.IsAbs(path) || ctx == nil {
		return path
	}
	return filepath.Join(ctx.ProjectDir, path)
}

func composeImageOverrideServices(ctx *config.Context) (map[string]string, map[string]bool) {
	path := composeProjectPath(ctx, plugin.ComposeImageOverrideFile)
	data, err := os.ReadFile(path) // #nosec G304 -- compose override path is generated from the local project directory.
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var document composeImageOverrideDocument
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, nil
	}
	images := map[string]string{}
	buildArgs := map[string]bool{}
	for service, value := range document.Services {
		service = strings.TrimSpace(service)
		if strings.TrimSpace(value.Image) != "" {
			images[service] = strings.TrimSpace(value.Image)
		}
		if len(value.Build.Args) > 0 {
			buildArgs[service] = true
		}
	}
	return images, buildArgs
}

func runComposeReconcileCommands(cmd *cobra.Command, ctx *config.Context, decision composeReconcileDecision) error {
	spec := decision.Spec
	var commands []string
	if decision.RunInit {
		commands = append(commands, spec.DockerComposeInit...)
	}
	if decision.RunBuild {
		commands = append(commands, spec.DockerComposeBuild...)
	}
	commands = append(commands, spec.DockerComposeUp...)
	commandsToRun := make([]string, 0, len(commands))
	for _, commandText := range commands {
		commandText = strings.TrimSpace(commandText)
		if commandText == "" || (decision.RunInit && isLegacyHostUIDArtifactCommand(ctx, spec, commandText)) {
			continue
		}
		_, plans, err := planLifecycleCommandList(ctx, commandText)
		if err != nil {
			return fmt.Errorf("validate compose reconcile command %q: %w", commandText, err)
		}
		if err := validateLifecycleProjectScripts(ctx, commandText, plans); err != nil {
			return err
		}
		commandsToRun = append(commandsToRun, commandText)
	}

	if decision.RunInit {
		if err := ensureComposeReconcileInitArtifactDirs(ctx, spec); err != nil {
			return err
		}
	}
	if len(commandsToRun) == 0 {
		return nil
	}

	envValues, messages, err := ctx.PrepareComposeUpPortOverride()
	if err != nil {
		return err
	}
	for _, message := range messages {
		fmt.Fprintln(cmd.ErrOrStderr(), message)
	}
	env := config.AppendEnvOverrides(os.Environ(), envValues)

	for _, commandText := range commandsToRun {
		if err := runLifecycleCommandList(cmd, ctx, commandText, env, false); err != nil {
			return err
		}
	}
	return nil
}

func ensureComposeReconcileInitArtifactDirs(ctx *config.Context, spec plugin.CreateSpec) error {
	for _, artifact := range spec.InitArtifacts {
		path := strings.TrimSpace(artifact.Path)
		if path == "" {
			continue
		}
		artifactPath, err := safeComposeInitArtifactPath(ctx, path)
		if err != nil {
			return err
		}
		parent := filepath.Dir(artifactPath)
		if parent == "." || parent == string(filepath.Separator) {
			continue
		}
		if err := ensureComposeInitArtifactExistingAncestor(ctx, parent, path); err != nil {
			return err
		}
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create init artifact directory %s: %w", filepath.Dir(path), err)
		}
		if err := ensureComposeInitArtifactParent(ctx, parent, path); err != nil {
			return err
		}
	}
	userID := strings.TrimSpace(composeReconcileUserID())
	for _, artifact := range spec.InitArtifacts {
		if artifact.ValueFrom != plugin.InitArtifactValueFromHostUID {
			continue
		}
		if userID == "" || userID == "unknown" {
			return fmt.Errorf("resolve host uid for init artifact %s", artifact.Path)
		}
		path, err := safeComposeInitArtifactPath(ctx, artifact.Path)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to replace host uid artifact symlink %s", artifact.Path)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect host uid artifact %s: %w", artifact.Path, err)
		}
		if err := os.WriteFile(path, []byte(userID+"\n"), 0o600); err != nil {
			return fmt.Errorf("write host uid artifact %s: %w", artifact.Path, err)
		}
	}
	return nil
}

func safeComposeInitArtifactPath(ctx *config.Context, artifactPath string) (string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return "", fmt.Errorf("project directory cannot be empty")
	}
	if artifactPath == "" || filepath.IsAbs(artifactPath) {
		return "", fmt.Errorf("init artifact path must be relative to the project: %q", artifactPath)
	}
	clean := filepath.Clean(artifactPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("init artifact path escapes the project: %q", artifactPath)
	}
	return filepath.Join(ctx.ProjectDir, clean), nil
}

func ensureComposeInitArtifactExistingAncestor(ctx *config.Context, parent, artifactPath string) error {
	existing := parent
	for {
		if _, err := os.Lstat(existing); err == nil {
			return ensureComposeInitArtifactParent(ctx, existing, artifactPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect init artifact parent for %s: %w", artifactPath, err)
		}
		next := filepath.Dir(existing)
		if next == existing {
			return fmt.Errorf("find existing init artifact parent for %s", artifactPath)
		}
		existing = next
	}
}

func ensureComposeInitArtifactParent(ctx *config.Context, parent, artifactPath string) error {
	projectRoot := canonicalComposeProjectDir(ctx.ProjectDir)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve init artifact parent for %s: %w", artifactPath, err)
	}
	resolvedParent, err = filepath.Abs(resolvedParent)
	if err != nil {
		return fmt.Errorf("resolve absolute init artifact parent for %s: %w", artifactPath, err)
	}
	relative, err := filepath.Rel(projectRoot, resolvedParent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("init artifact parent escapes the project through a symlink: %q", artifactPath)
	}
	return nil
}

func isLegacyHostUIDArtifactCommand(ctx *config.Context, spec plugin.CreateSpec, commandText string) bool {
	fields := strings.Fields(commandText)
	if len(fields) != 4 || fields[0] != "id" || fields[1] != "-u" || fields[2] != ">" {
		return false
	}
	writtenPath := filepath.Clean(composeProjectPath(ctx, fields[3]))
	for _, artifact := range spec.InitArtifacts {
		if artifact.ValueFrom == plugin.InitArtifactValueFromHostUID && writtenPath == filepath.Clean(composeProjectPath(ctx, artifact.Path)) {
			return true
		}
	}
	return false
}

func composeReconcileChecked(ctx *config.Context, spec plugin.CreateSpec) (bool, error) {
	cache, err := loadComposeReconcileCache()
	if err != nil {
		return false, err
	}
	key, err := composeReconcileCacheKey(ctx, spec)
	if err != nil {
		return false, err
	}
	entry, ok := cache.Entries[key]
	if !ok {
		return false, nil
	}
	if entry.CheckedAt.IsZero() || composeReconcileNow().Sub(entry.CheckedAt) > composeReconcileCacheTTL {
		delete(cache.Entries, key)
		_ = saveComposeReconcileCache(cache)
		return false, nil
	}
	return true, nil
}

func markComposeReconcileChecked(ctx *config.Context, status composeReconcileStatus, spec plugin.CreateSpec) error {
	cache, err := loadComposeReconcileCache()
	if err != nil {
		return err
	}
	key, err := composeReconcileCacheKey(ctx, spec)
	if err != nil {
		return err
	}
	host, _ := composeReconcileHost()
	cache.Entries[key] = composeReconcileCacheEntry{
		Host:               host,
		Plugin:             strings.TrimSpace(ctx.Plugin),
		ProjectDir:         canonicalComposeProjectDir(ctx.ProjectDir),
		ObservedGeneration: composeReconcileSpecFingerprint(spec),
		Reason:             status.summary(),
		Conditions:         append([]composeReconcileCondition{}, status.Conditions...),
		CheckedAt:          composeReconcileNow().UTC(),
	}
	return saveComposeReconcileCache(cache)
}

func clearComposeReconcileCacheEntry(ctx *config.Context, spec plugin.CreateSpec) error {
	cache, err := loadComposeReconcileCache()
	if err != nil {
		return err
	}
	key, err := composeReconcileCacheKey(ctx, spec)
	if err != nil {
		return err
	}
	if _, ok := cache.Entries[key]; !ok {
		return nil
	}
	delete(cache.Entries, key)
	return saveComposeReconcileCache(cache)
}

func loadComposeReconcileCache() (composeReconcileCache, error) {
	path, err := composeReconcileCachePath()
	if err != nil {
		return composeReconcileCache{}, err
	}
	cache := composeReconcileCache{Version: composeReconcileCacheVersion, Entries: map[string]composeReconcileCacheEntry{}}
	data, err := os.ReadFile(path) // #nosec G304 -- cache path is generated under sitectl config state.
	if err != nil {
		if os.IsNotExist(err) {
			return cache, nil
		}
		return composeReconcileCache{}, err
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return composeReconcileCache{Version: composeReconcileCacheVersion, Entries: map[string]composeReconcileCacheEntry{}}, nil
	}
	if cache.Version != composeReconcileCacheVersion || cache.Entries == nil {
		cache.Version = composeReconcileCacheVersion
		cache.Entries = map[string]composeReconcileCacheEntry{}
	}
	return cache, nil
}

func saveComposeReconcileCache(cache composeReconcileCache) error {
	path, err := composeReconcileCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func composeReconcileCachePath() (string, error) {
	configPath, err := config.ConfigFilePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(configPath), "compose-up-reconcile.json"), nil
}

func composeReconcileCacheKey(ctx *config.Context, spec plugin.CreateSpec) (string, error) {
	host, err := composeReconcileHost()
	if err != nil {
		host = "unknown"
	}
	projectDir := canonicalComposeProjectDir(ctx.ProjectDir)
	projectFingerprint := composeReconcileProjectFingerprint(projectDir)
	specFingerprint := composeReconcileSpecFingerprint(spec)
	userID := strings.TrimSpace(composeReconcileUserID())
	sum := sha256.Sum256([]byte(strings.Join([]string{host, userID, strings.TrimSpace(ctx.Plugin), projectDir, projectFingerprint, specFingerprint}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

func currentComposeReconcileUserID() string {
	uid, _, available, err := composeReconcileLocalIdentity()
	if err != nil {
		return "unknown"
	}
	if !available {
		return "0"
	}
	return uid
}

func composeReconcileProjectFingerprint(projectDir string) string {
	path := filepath.Join(projectDir, plugin.ComposeImageOverrideFile)
	data, err := os.ReadFile(path) // #nosec G304 -- compose override path is generated from the local project directory.
	if err != nil {
		if os.IsNotExist(err) {
			return "image-override:missing"
		}
		return "image-override:unreadable"
	}
	sum := sha256.Sum256(data)
	return "image-override:" + hex.EncodeToString(sum[:])
}

func composeReconcileSpecFingerprint(spec plugin.CreateSpec) string {
	desired := struct {
		Name               string                    `json:"name,omitempty"`
		Plugin             string                    `json:"plugin,omitempty"`
		DockerComposeBuild []string                  `json:"docker_compose_build,omitempty"`
		DockerComposeInit  []string                  `json:"docker_compose_init,omitempty"`
		DockerComposeUp    []string                  `json:"docker_compose_up,omitempty"`
		InitArtifacts      []plugin.InitArtifact     `json:"init_artifacts,omitempty"`
		InitVolumes        []plugin.InitVolume       `json:"init_volumes,omitempty"`
		Images             []plugin.ComposeImageSpec `json:"images,omitempty"`
	}{
		Name:               spec.Name,
		Plugin:             spec.Plugin,
		DockerComposeBuild: append([]string{}, spec.DockerComposeBuild...),
		DockerComposeInit:  append([]string{}, spec.DockerComposeInit...),
		DockerComposeUp:    append([]string{}, spec.DockerComposeUp...),
		InitArtifacts:      append([]plugin.InitArtifact{}, spec.InitArtifacts...),
		InitVolumes:        append([]plugin.InitVolume{}, spec.InitVolumes...),
		Images:             append([]plugin.ComposeImageSpec{}, spec.Images...),
	}
	data, err := json.Marshal(desired)
	if err != nil {
		return "spec:unreadable"
	}
	sum := sha256.Sum256(data)
	return "spec:" + hex.EncodeToString(sum[:])
}

func canonicalComposeProjectDir(projectDir string) string {
	projectDir = filepath.Clean(strings.TrimSpace(projectDir))
	if projectDir == "" {
		return ""
	}
	if absolute, err := filepath.Abs(projectDir); err == nil {
		projectDir = absolute
	}
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	return projectDir
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileMissingOrEmpty(path string) bool {
	info, err := os.Stat(path)
	return err != nil || info.Size() == 0
}
