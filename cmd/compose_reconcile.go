package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
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
	Name string `json:"name"`
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
)

var composeReconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Run plugin Docker Compose init/build/up reconciliation",
	Long: `Run plugin Docker Compose init/build/up reconciliation.

This is the same lifecycle repair path sitectl runs automatically before
'sitectl compose up' for plugin-managed local Compose projects. Use --force to
rerun build/up even when the project is cached as current. Use --reset-init to
remove plugin-declared init artifacts and init volumes before reconciling.`,
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
		return runComposeReconcileCommand(cmd, composeReconcileOptions{
			Force:     force,
			ResetInit: resetInit,
		})
	},
}

func init() {
	composeReconcileCmd.Flags().Bool("force", false, "Ignore the reconcile cache and rerun build/up.")
	composeReconcileCmd.Flags().Bool("reset-init", false, "Remove plugin-declared init artifacts and init volumes before reconciling.")
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

func maybeRunComposeReconcile(cmd *cobra.Command, ctx *config.Context) (bool, error) {
	if cmd == nil || ctx == nil || ctx.DockerHostType != config.ContextLocal {
		return false, nil
	}
	if strings.TrimSpace(ctx.Plugin) == "" || strings.TrimSpace(ctx.Plugin) == "core" {
		return false, nil
	}

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

func runComposeReconcileCommand(cmd *cobra.Command, opts composeReconcileOptions) error {
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

func composeReconcileDecisionForContext(ctx *config.Context) (composeReconcileDecision, error) {
	return composeReconcileDecisionForContextWithOptions(ctx, composeReconcileOptions{})
}

func composeReconcileDecisionForContextWithOptions(ctx *config.Context, opts composeReconcileOptions) (composeReconcileDecision, error) {
	spec, ok, err := composeReconcileSpec(strings.TrimSpace(ctx.Plugin))
	if err != nil || !ok {
		return composeReconcileDecision{}, err
	}

	if !opts.Force {
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

	for _, artifact := range spec.InitArtifacts {
		path := composeProjectPath(ctx, artifact.Path)
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
	if fileMissingOrEmpty(filepath.Join(ctx.ProjectDir, ".env")) && fileExists(filepath.Join(ctx.ProjectDir, "sample.env")) {
		return composeReconcileStatus{Conditions: []composeReconcileCondition{{
			Type:    conditionInitialized,
			Status:  conditionStatusFalse,
			Reason:  "InitArtifactMissing",
			Message: ".env is missing",
		}}}, nil
	}

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
	removed := []string{}
	for _, artifact := range spec.InitArtifacts {
		path := strings.TrimSpace(artifact.Path)
		if path == "" {
			continue
		}
		fullPath := composeProjectPath(ctx, path)
		info, err := os.Lstat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("inspect %s: %w", path, err)
		}
		if info.IsDir() {
			return removed, fmt.Errorf("refusing to remove init artifact directory %s", path)
		}
		if err := composeReconcileRemoveFile(fullPath); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("remove %s: %w", path, err)
		}
		removed = append(removed, "file "+path)
	}

	if len(spec.InitVolumes) == 0 {
		return removed, nil
	}
	composeConfig, err := composeReconcileReadConfig(ctx)
	if err != nil {
		return removed, fmt.Errorf("inspect compose volumes: %w", err)
	}
	configuredVolumes := composeConfiguredVolumeNames(ctx, composeConfig)
	for _, volume := range spec.InitVolumes {
		name := strings.TrimSpace(volume.Name)
		if name == "" {
			continue
		}
		dockerVolume := configuredVolumes[name]
		if strings.TrimSpace(dockerVolume) == "" {
			dockerVolume = name
		}
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
	if decision.RunInit {
		if err := ensureComposeReconcileInitArtifactDirs(ctx, spec); err != nil {
			return err
		}
	}

	var commands []string
	if decision.RunInit {
		commands = append(commands, spec.DockerComposeInit...)
	}
	if decision.RunBuild {
		commands = append(commands, spec.DockerComposeBuild...)
	}
	commands = append(commands, spec.DockerComposeUp...)
	if len(commands) == 0 {
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

	for _, commandText := range commands {
		commandText = strings.TrimSpace(commandText)
		if commandText == "" {
			continue
		}
		commandText = ctx.DockerComposeShellCommand(commandText)
		fmt.Fprintf(cmd.OutOrStdout(), "Running %s\n", commandText)
		command := exec.CommandContext(cmd.Context(), "bash", "-lc", commandText) // #nosec G204 -- commands come from trusted plugin create metadata.
		command.Dir = ctx.ProjectDir
		command.Env = env
		command.Stdin = cmd.InOrStdin()
		command.Stdout = cmd.OutOrStdout()
		command.Stderr = cmd.ErrOrStderr()
		if err := command.Run(); err != nil {
			return fmt.Errorf("run %s: %w", commandText, err)
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
		parent := filepath.Dir(composeProjectPath(ctx, path))
		if parent == "." || parent == string(filepath.Separator) {
			continue
		}
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create init artifact directory %s: %w", filepath.Dir(path), err)
		}
	}
	return nil
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
	current, err := user.Current()
	if err != nil || strings.TrimSpace(current.Uid) == "" {
		return "unknown"
	}
	return strings.TrimSpace(current.Uid)
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
