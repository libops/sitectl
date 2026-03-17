package component

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	"github.com/libops/sitectl/pkg/docker"
)

type Hook func(context.Context, *Runtime) error

var ErrActionCancelled = errors.New("component action cancelled")

type State string

const (
	StateOn  State = "on"
	StateOff State = "off"
)

type Disposition string

const (
	DispositionDisabled    Disposition = "disabled"
	DispositionSuperseded  Disposition = "superceded"
	DispositionEnabled     Disposition = "enabled"
	DispositionDistributed Disposition = "distributed"
)

type ComposeSpec struct {
	Definitions         *ComposeDefinitions
	RemoveServices      []string
	PruneUnusedResource bool
}

type DrupalSpec struct {
	ConfigSyncDir     string
	Files             map[string][]byte
	DeleteFiles       []string
	DisableTransforms []DrupalTransform
	EnableTransforms  []DrupalTransform
}

type DrupalTransform interface {
	Apply(*DrupalConfigSet) error
}

type ReplaceStringsTransform struct {
	Replacements []StringReplacement
}

type DeleteMapEntriesTransform struct {
	Matches []MapEntryMatch
}

type ComponentSpec struct {
	Name          string
	Gates         GateSpec
	Compose       ComposeSpec
	Drupal        DrupalSpec
	BeforeDisable []Hook
	AfterDisable  []Hook
	BeforeEnable  []Hook
	AfterEnable   []Hook
}

type Component interface {
	Name() string
	DefaultState() State
	SpecFor(state State) ComponentSpec
}

type StaticComponent struct {
	name         string
	defaultState State
	on           ComponentSpec
	off          ComponentSpec
}

type GateSpec struct {
	LocalOnly           bool
	DisableConfirmation string
	EnableConfirmation  string
}

type ApplyOptions struct {
	Yolo bool

	// AutoApprove is kept for compatibility with earlier callers.
	AutoApprove bool
	Confirm     func(prompt string) (bool, error)
}

type Runtime struct {
	Context *config.Context
	Docker  *docker.DockerClient
}

type Manager struct {
	ctx *config.Context
}

func NewManager(ctx *config.Context) *Manager {
	return &Manager{ctx: ctx}
}

func NewStaticComponent(name string, defaultState State, on ComponentSpec, off ComponentSpec) StaticComponent {
	on.Name = resolveComponentSpecName(on.Name, name)
	off.Name = resolveComponentSpecName(off.Name, name)
	return StaticComponent{
		name:         name,
		defaultState: defaultState,
		on:           on,
		off:          off,
	}
}

func (f StaticComponent) Name() string {
	return f.name
}

func (f StaticComponent) DefaultState() State {
	if f.defaultState == "" {
		return StateOn
	}
	return f.defaultState
}

func (f StaticComponent) SpecFor(state State) ComponentSpec {
	switch normalizeState(state) {
	case StateOff:
		return f.off
	default:
		return f.on
	}
}

func (m *Manager) DisableComponent(ctx context.Context, spec ComponentSpec) error {
	return m.DisableComponentWithOptions(ctx, spec, ApplyOptions{})
}

func (m *Manager) DisableComponentWithOptions(ctx context.Context, spec ComponentSpec, opts ApplyOptions) error {
	if err := m.checkGates(spec, opts, false); err != nil {
		return err
	}

	runtime, err := m.newRuntime(spec.BeforeDisable, spec.AfterDisable)
	if err != nil {
		return err
	}
	if runtime != nil {
		defer runtime.close()
	}

	for _, hook := range spec.BeforeDisable {
		if err := hook(ctx, runtime); err != nil {
			return fmt.Errorf("run before-disable hook for %s: %w", spec.Name, err)
		}
	}

	if err := m.applyComposeDisable(spec.Compose); err != nil {
		return err
	}
	if err := m.applyDrupalDisable(spec.Drupal); err != nil {
		return err
	}

	for _, hook := range spec.AfterDisable {
		if err := hook(ctx, runtime); err != nil {
			return fmt.Errorf("run after-disable hook for %s: %w", spec.Name, err)
		}
	}

	return nil
}

func (m *Manager) EnableComponent(ctx context.Context, spec ComponentSpec) error {
	return m.EnableComponentWithOptions(ctx, spec, ApplyOptions{})
}

func (m *Manager) EnableComponentWithOptions(ctx context.Context, spec ComponentSpec, opts ApplyOptions) error {
	if err := m.checkGates(spec, opts, true); err != nil {
		return err
	}

	runtime, err := m.newRuntime(spec.BeforeEnable, spec.AfterEnable)
	if err != nil {
		return err
	}
	if runtime != nil {
		defer runtime.close()
	}

	for _, hook := range spec.BeforeEnable {
		if err := hook(ctx, runtime); err != nil {
			return fmt.Errorf("run before-enable hook for %s: %w", spec.Name, err)
		}
	}

	if err := m.applyComposeEnable(spec.Compose); err != nil {
		return err
	}
	if err := m.applyDrupalEnable(spec.Drupal); err != nil {
		return err
	}

	for _, hook := range spec.AfterEnable {
		if err := hook(ctx, runtime); err != nil {
			return fmt.Errorf("run after-enable hook for %s: %w", spec.Name, err)
		}
	}

	return nil
}

func (m *Manager) ReconcileComponent(ctx context.Context, feature Component, state State, opts ApplyOptions) error {
	switch normalizeState(state) {
	case StateOn:
		return m.EnableComponentWithOptions(ctx, feature.SpecFor(StateOn), opts)
	case StateOff:
		return m.DisableComponentWithOptions(ctx, feature.SpecFor(StateOff), opts)
	default:
		return fmt.Errorf("unsupported component state %q for component %q", state, feature.Name())
	}
}

func (m *Manager) ReconcileAll(ctx context.Context, states map[string]State, opts ApplyOptions, features ...Component) error {
	for _, feature := range features {
		state, ok := states[feature.Name()]
		if !ok || state == "" {
			state = feature.DefaultState()
		}
		if err := m.ReconcileComponent(ctx, feature, state, opts); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) applyComposeDisable(spec ComposeSpec) error {
	if len(spec.RemoveServices) == 0 {
		return nil
	}

	composePath := m.composePath()
	data, err := m.ctx.ReadFile(composePath)
	if err != nil {
		return fmt.Errorf("read compose file %q: %w", composePath, err)
	}

	composeFile, err := ParseComposeProject(data)
	if err != nil {
		return err
	}
	for _, name := range spec.RemoveServices {
		composeFile.RemoveService(name)
	}
	if spec.PruneUnusedResource {
		composeFile.PruneUnusedResources()
	}

	updated, err := composeFile.Bytes()
	if err != nil {
		return err
	}
	if err := m.ctx.WriteFile(composePath, updated); err != nil {
		return fmt.Errorf("write compose file %q: %w", composePath, err)
	}

	return nil
}

func (m *Manager) applyComposeEnable(spec ComposeSpec) error {
	if spec.Definitions == nil {
		return nil
	}

	composePath := m.composePath()
	data, err := m.ctx.ReadFile(composePath)
	if err != nil {
		return fmt.Errorf("read compose file %q: %w", composePath, err)
	}

	composeFile, err := ParseComposeProject(data)
	if err != nil {
		return err
	}
	composeFile.AddDefinitions(spec.Definitions)

	updated, err := composeFile.Bytes()
	if err != nil {
		return err
	}
	if err := m.ctx.WriteFile(composePath, updated); err != nil {
		return fmt.Errorf("write compose file %q: %w", composePath, err)
	}

	return nil
}

func (m *Manager) applyDrupalDisable(spec DrupalSpec) error {
	if !spec.hasChanges() {
		return nil
	}
	set, err := LoadDrupalConfigSet(m.ctx, spec.configSyncDir(m.ctx))
	if err != nil {
		return err
	}

	for _, transform := range spec.DisableTransforms {
		if err := transform.Apply(set); err != nil {
			return err
		}
	}
	set.DeleteFiles(spec.DeleteFiles...)

	return set.Save(m.ctx)
}

func (m *Manager) applyDrupalEnable(spec DrupalSpec) error {
	if !spec.hasChanges() {
		return nil
	}
	set, err := LoadDrupalConfigSet(m.ctx, spec.configSyncDir(m.ctx))
	if err != nil {
		return err
	}

	for _, transform := range spec.EnableTransforms {
		if err := transform.Apply(set); err != nil {
			return err
		}
	}
	for name, data := range spec.Files {
		set.UpsertFile(name, data)
	}

	return set.Save(m.ctx)
}

func (m *Manager) composePath() string {
	if len(m.ctx.ComposeFile) > 0 {
		return filepath.Join(m.ctx.ProjectDir, m.ctx.ComposeFile[0])
	}
	return filepath.Join(m.ctx.ProjectDir, "docker-compose.yml")
}

func (s DrupalSpec) configSyncDir(ctx *config.Context) string {
	if s.ConfigSyncDir != "" {
		return filepath.Join(ctx.ProjectDir, s.ConfigSyncDir)
	}
	return filepath.Join(ctx.ProjectDir, "config", "sync")
}

func (s DrupalSpec) hasChanges() bool {
	return len(s.Files) > 0 || len(s.DeleteFiles) > 0 || len(s.DisableTransforms) > 0 || len(s.EnableTransforms) > 0
}

func (r *Runtime) close() {
	if r.Docker != nil {
		_ = r.Docker.Close()
	}
}

func (m *Manager) newRuntime(hookSets ...[]Hook) (*Runtime, error) {
	needsDocker := false
	for _, hooks := range hookSets {
		if len(hooks) > 0 {
			needsDocker = true
			break
		}
	}
	if !needsDocker {
		return &Runtime{Context: m.ctx}, nil
	}

	dockerClient, err := docker.GetDockerCli(m.ctx)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Runtime{
		Context: m.ctx,
		Docker:  dockerClient,
	}, nil
}

func (t ReplaceStringsTransform) Apply(set *DrupalConfigSet) error {
	for _, replacement := range t.Replacements {
		set.ReplaceString(replacement.Old, replacement.New)
	}
	return nil
}

func (t DeleteMapEntriesTransform) Apply(set *DrupalConfigSet) error {
	for _, match := range t.Matches {
		if err := set.DeleteMapEntries(match); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) checkGates(spec ComponentSpec, opts ApplyOptions, enable bool) error {
	if spec.Gates.LocalOnly && m.ctx.DockerHostType != config.ContextLocal {
		return fmt.Errorf("component %q can only run on local contexts", spec.Name)
	}

	action := "disable"
	if enable {
		action = "enable"
	}

	prompt := m.confirmationPrompt(spec, enable)
	if prompt == "" {
		return nil
	}
	if opts.Yolo || opts.AutoApprove {
		return nil
	}

	confirm := opts.Confirm
	if confirm == nil {
		confirm = defaultConfirm
	}

	ok, err := confirm(prompt)
	if err != nil {
		return fmt.Errorf("confirm %s for %q: %w", action, spec.Name, err)
	}
	if !ok {
		return ErrActionCancelled
	}

	return nil
}

func defaultConfirm(prompt string) (bool, error) {
	if !strings.Contains(prompt, "[") {
		prompt += " [y/N]: "
	}

	input, err := config.GetInput(prompt)
	if err != nil {
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(input)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func (m *Manager) confirmationPrompt(spec ComponentSpec, enable bool) string {
	if enable && spec.Gates.EnableConfirmation != "" {
		return spec.Gates.EnableConfirmation
	}
	if !enable && spec.Gates.DisableConfirmation != "" {
		return spec.Gates.DisableConfirmation
	}

	actionLabel := "Disable"
	if enable {
		actionLabel = "Enable"
	}

	return fmt.Sprintf(
		"%s component %q on context %q? This may rewrite docker compose and Drupal config, and future deploys or compose runs may delete containers, volumes, or data. Use --yolo only if you have reviewed the change and have a backup. [y/N]: ",
		actionLabel, spec.Name, m.ctx.Name,
	)
}

func ParseState(value string) (State, error) {
	state := normalizeState(State(value))
	switch state {
	case StateOn, StateOff:
		return state, nil
	default:
		return "", fmt.Errorf("invalid component state %q: expected on or off", value)
	}
}

func normalizeState(state State) State {
	return State(strings.ToLower(strings.TrimSpace(string(state))))
}

func ParseDisposition(value string) (Disposition, error) {
	disposition := normalizeDisposition(Disposition(value))
	switch disposition {
	case DispositionDisabled, DispositionSuperseded, DispositionEnabled, DispositionDistributed:
		return disposition, nil
	default:
		return "", fmt.Errorf("invalid component disposition %q: expected one of %s, %s, %s, %s", value, DispositionDisabled, DispositionSuperseded, DispositionEnabled, DispositionDistributed)
	}
}

func normalizeDisposition(disposition Disposition) Disposition {
	switch strings.ToLower(strings.TrimSpace(string(disposition))) {
	case "", "on":
		if strings.TrimSpace(string(disposition)) == "" {
			return ""
		}
		return DispositionEnabled
	case "off":
		return DispositionDisabled
	case string(DispositionDisabled):
		return DispositionDisabled
	case string(DispositionSuperseded):
		return DispositionSuperseded
	case string(DispositionEnabled):
		return DispositionEnabled
	case string(DispositionDistributed):
		return DispositionDistributed
	default:
		return Disposition(strings.ToLower(strings.TrimSpace(string(disposition))))
	}
}

func DispositionToState(disposition Disposition) State {
	switch normalizeDisposition(disposition) {
	case DispositionEnabled:
		return StateOn
	default:
		return StateOff
	}
}

func StateToDisposition(state State) Disposition {
	switch normalizeState(state) {
	case StateOn:
		return DispositionEnabled
	default:
		return DispositionDisabled
	}
}

func resolveComponentSpecName(current, fallback string) string {
	if current != "" {
		return current
	}
	return fallback
}
