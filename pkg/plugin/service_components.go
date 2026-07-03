package plugin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	corecomponent "github.com/libops/sitectl/pkg/component"
	"github.com/libops/sitectl/pkg/config"
	"github.com/spf13/cobra"
)

// ServiceComponentRegistryOptions configures component registration for
// service plugins backed by compose definitions.
type ServiceComponentRegistryOptions struct {
	DisplayName string
	Components  []corecomponent.ComposeServiceComponent
}

// RegisterServiceComponents registers service components on the provided SDK
// when the SDK is non-nil.
//
// Deprecated: call s.RegisterServiceComponents(opts) when an SDK is already
// available. This helper remains for existing plugin packages.
func RegisterServiceComponents(s *SDK, opts ServiceComponentRegistryOptions) {
	if s != nil {
		s.RegisterServiceComponents(opts)
	}
}

// RegisterServiceComponents registers compose-backed service components and
// their hidden RPC command handlers. It is idempotent for component names: each
// call adds new components to the SDK's accumulated set, then rebuilds the
// component command tree from that set.
func (s *SDK) RegisterServiceComponents(opts ServiceComponentRegistryOptions) {
	if s == nil || len(opts.Components) == 0 {
		return
	}

	if displayName := strings.TrimSpace(opts.DisplayName); displayName != "" && s.serviceComponentDisplayName == "" {
		s.serviceComponentDisplayName = displayName
	}
	s.serviceComponents = appendUniqueServiceComponents(s.serviceComponents, opts.Components)
	displayName := s.serviceComponentDisplayName
	if displayName == "" {
		displayName = s.Metadata.Name
	}
	registry := serviceComponentRegistry{
		sdk:         s,
		displayName: displayName,
		components:  append([]corecomponent.ComposeServiceComponent{}, s.serviceComponents...),
	}
	if registry.displayName == "" {
		registry.displayName = s.Metadata.Name
	}

	defs := registry.definitions()
	s.RegisterComponentDefinitions(defs...)
	s.RegisterComponentCommand(registry.command())
}

func appendUniqueServiceComponents(existing, incoming []corecomponent.ComposeServiceComponent) []corecomponent.ComposeServiceComponent {
	out := append([]corecomponent.ComposeServiceComponent{}, existing...)
	seen := map[string]bool{}
	for _, component := range out {
		if name := strings.TrimSpace(component.Name()); name != "" {
			seen[name] = true
		}
	}
	for _, component := range incoming {
		name := strings.TrimSpace(component.Name())
		if name != "" && seen[name] {
			continue
		}
		if name != "" {
			seen[name] = true
		}
		out = append(out, component)
	}
	return out
}

type serviceComponentRegistry struct {
	sdk         *SDK
	displayName string
	components  []corecomponent.ComposeServiceComponent
}

func (r serviceComponentRegistry) command() *cobra.Command {
	root := &cobra.Command{
		Use:    "component",
		Short:  "Internal component extension command",
		Hidden: true,
	}

	root.AddCommand(r.listCommand())
	root.AddCommand(r.describeCommand())
	root.AddCommand(r.reconcileCommand())
	root.AddCommand(r.setCommand())
	return root
}

func (r serviceComponentRegistry) listCommand() *cobra.Command {
	var listName string
	list := &cobra.Command{
		Use:   "list [component]",
		Short: "Internal component list hook",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(listName)
			if len(args) > 0 {
				name = strings.TrimSpace(args[0])
			}
			return corecomponent.WriteComponentCatalog(cmd.OutOrStdout(), r.displayName, r.definitions(), name)
		},
	}
	list.Flags().StringVarP(&listName, "component", "c", "", "Specific component to list")
	return list
}

func (r serviceComponentRegistry) describeCommand() *cobra.Command {
	var componentName string
	var projectPath string
	var codebaseRootfs string
	var drupalRootfs string
	var verbose bool
	var format string
	describe := &cobra.Command{
		Use:   "describe",
		Short: "Internal component describe hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootfs, err := componentRootfsFlagValue(cmd, codebaseRootfs, drupalRootfs)
			if err != nil {
				return err
			}
			views, err := r.detectViews(componentName, projectPath, rootfs)
			if err != nil {
				return err
			}
			return corecomponent.WriteComponentStatusReportWithFormat(cmd.OutOrStdout(), views, verbose, normalizeComponentReportFormat(format))
		},
	}
	// These flags intentionally mirror ComponentTargetParams rpc_flags tags.
	// RegisterComponentCommand validates the names and cobra value types.
	describe.Flags().StringVarP(&componentName, "component", "c", "", "Specific component to describe")
	describe.Flags().StringVar(&projectPath, "path", "", "Project path override")
	describe.Flags().StringVar(&codebaseRootfs, "codebase-rootfs", "", "Codebase rootfs path override")
	describe.Flags().StringVar(&drupalRootfs, "drupal-rootfs", "", "Drupal rootfs path override")
	corecomponent.MarkCodebaseRootfsFlag(describe, "codebase-rootfs")
	describe.Flags().BoolVar(&verbose, "verbose", false, "Include verbose component details")
	describe.Flags().StringVar(&format, "format", "", "Output format override")
	return describe
}

func (r serviceComponentRegistry) reconcileCommand() *cobra.Command {
	var componentName string
	var projectPath string
	var codebaseRootfs string
	var drupalRootfs string
	var report bool
	var verbose bool
	var format string
	var yolo bool
	reconcile := &cobra.Command{
		Use:   "reconcile",
		Short: "Internal component reconcile hook",
		RunE: func(cmd *cobra.Command, args []string) error {
			rootfs, err := componentRootfsFlagValue(cmd, codebaseRootfs, drupalRootfs)
			if err != nil {
				return err
			}
			if report {
				views, err := r.detectViews(componentName, projectPath, rootfs)
				if err != nil {
					return err
				}
				return corecomponent.WriteComponentStatusReportWithFormat(cmd.OutOrStdout(), views, verbose, normalizeComponentReportFormat(format))
			}
			return r.reconcile(cmd, componentName, projectPath, yolo)
		},
	}
	// These flags intentionally mirror ComponentTargetParams rpc_flags tags.
	// RegisterComponentCommand validates the names and cobra value types.
	reconcile.Flags().StringVarP(&componentName, "component", "c", "", "Specific component to reconcile")
	reconcile.Flags().StringVar(&projectPath, "path", "", "Project path override")
	reconcile.Flags().StringVar(&codebaseRootfs, "codebase-rootfs", "", "Codebase rootfs path override")
	reconcile.Flags().StringVar(&drupalRootfs, "drupal-rootfs", "", "Drupal rootfs path override")
	corecomponent.MarkCodebaseRootfsFlag(reconcile, "codebase-rootfs")
	reconcile.Flags().BoolVar(&report, "report", false, "Render a report instead of applying changes")
	reconcile.Flags().BoolVar(&verbose, "verbose", false, "Include verbose component details")
	reconcile.Flags().StringVar(&format, "format", "", "Output format override")
	reconcile.Flags().BoolVar(&yolo, "yolo", false, "Apply without confirmation")
	return reconcile
}

func (r serviceComponentRegistry) setCommand() *cobra.Command {
	var projectPath string
	var setState string
	var setDisposition string
	var yolo bool
	setFollowUps := map[string]*serviceFollowUpFlagValue{}
	set := &cobra.Command{
		Use:   "set <name> [disposition]",
		Short: "Internal component set hook",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			value := ""
			if len(args) > 1 {
				value = args[1]
			}
			followUps := collectServiceFollowUps(setFollowUps)
			return r.set(cmd, args[0], value, setState, setDisposition, projectPath, yolo, followUps)
		},
	}
	// These flags and positionals intentionally mirror ComponentSetParams.
	// RegisterComponentCommand validates bridged flag names and cobra value types.
	set.Flags().StringVar(&projectPath, "path", "", "Project path override")
	set.Flags().StringVar(&setState, "state", "", "Explicit state override")
	set.Flags().StringVar(&setDisposition, "disposition", "", "Explicit disposition override")
	set.Flags().BoolVar(&yolo, "yolo", false, "Apply without confirmation")
	bindServiceFollowUpFlags(set, r.definitions(), setFollowUps)
	return set
}

func (r serviceComponentRegistry) definitions() []corecomponent.Definition {
	defs := make([]corecomponent.Definition, 0, len(r.components))
	for _, component := range r.components {
		defs = append(defs, component.Definition())
	}
	return defs
}

func (r serviceComponentRegistry) componentByName(name string) (corecomponent.ComposeServiceComponent, bool) {
	name = strings.TrimSpace(name)
	for _, component := range r.components {
		if component.Name() == name {
			return component, true
		}
	}
	return corecomponent.ComposeServiceComponent{}, false
}

func componentRootfsFlagValue(cmd *cobra.Command, codebaseRootfs, drupalRootfs string) (string, error) {
	codebaseRootfs = strings.TrimSpace(codebaseRootfs)
	drupalRootfs = strings.TrimSpace(drupalRootfs)
	codebaseChanged := cmd != nil && cmd.Flags().Changed("codebase-rootfs")
	drupalChanged := cmd != nil && cmd.Flags().Changed("drupal-rootfs")
	if codebaseChanged && drupalChanged && codebaseRootfs != drupalRootfs {
		return "", fmt.Errorf("--codebase-rootfs and --drupal-rootfs cannot be combined with different values")
	}
	if drupalChanged {
		return drupalRootfs, nil
	}
	return codebaseRootfs, nil
}

func (r serviceComponentRegistry) detectViews(componentName, projectPath, codebaseRootfs string) ([]corecomponent.ReviewView, error) {
	ctx, err := r.resolveContext(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve component context: %w", err)
	}
	allDefs := r.definitions()
	defs := allDefs
	if strings.TrimSpace(componentName) != "" {
		name := strings.TrimSpace(componentName)
		defs = nil
		for _, def := range allDefs {
			if def.Name == name {
				defs = append(defs, def)
				break
			}
		}
		if len(defs) == 0 {
			return nil, fmt.Errorf("unknown component %q", componentName)
		}
	}

	statuses, err := corecomponent.DetectComponentStatuses(ctx, ctx.ProjectDir, corecomponent.DetectOptions{
		ComposeRoot:  ctx.ProjectDir,
		DrupalRootfs: codebaseRootfs,
	}, defs...)
	if err != nil {
		return nil, fmt.Errorf("detect component statuses: %w", err)
	}

	defByName := map[string]corecomponent.Definition{}
	for _, def := range allDefs {
		defByName[def.Name] = def
	}
	views := make([]corecomponent.ReviewView, 0, len(statuses))
	for i := range statuses {
		status := statuses[i]
		def := defByName[status.Name]
		views = append(views, corecomponent.ReviewView{
			Definition:  def,
			Name:        status.Name,
			State:       status.State,
			Disposition: serviceDisposition(status.State, def),
			SDKStatus:   &statuses[i],
		})
	}
	return views, nil
}

func (r serviceComponentRegistry) reconcile(cmd *cobra.Command, componentName, projectPath string, yolo bool) error {
	ctx, err := r.resolveContext(projectPath)
	if err != nil {
		return fmt.Errorf("resolve component context: %w", err)
	}
	warnRemoteComponentMutation(cmd, ctx)

	components := r.components
	if strings.TrimSpace(componentName) != "" {
		component, ok := r.componentByName(componentName)
		if !ok {
			return fmt.Errorf("unknown component %q", componentName)
		}
		components = []corecomponent.ComposeServiceComponent{component}
	}

	manager := corecomponent.NewManager(ctx)
	for _, component := range components {
		if err := manager.ReconcileComponent(cmd.Context(), component, component.DefaultState(), corecomponent.ApplyOptions{Yolo: yolo}); err != nil {
			return fmt.Errorf("reconcile component %q: %w", component.Name(), err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", component.Name(), corecomponent.StateToDisposition(component.DefaultState()))
	}
	return nil
}

func (s *SDK) reconcileCreateServiceComponents(ctx context.Context, target *config.Context, decisions map[string]corecomponent.ReviewDecision) error {
	if s == nil || target == nil || len(s.serviceComponents) == 0 || len(decisions) == 0 {
		return nil
	}

	manager := corecomponent.NewManager(target)
	for _, serviceComponent := range s.serviceComponents {
		decision, ok := decisions[serviceComponent.Name()]
		if !ok {
			continue
		}
		state := decision.State
		if state == "" {
			state = serviceComponent.DefaultState()
		}
		spec := serviceComponent.SpecForWithOptions(state, decision.Options)
		switch state {
		case corecomponent.StateOn:
			if err := manager.EnableComponentWithOptions(ctx, spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
				return fmt.Errorf("enable create component %q: %w", serviceComponent.Name(), err)
			}
		case corecomponent.StateOff:
			if err := manager.DisableComponentWithOptions(ctx, spec, corecomponent.ApplyOptions{Yolo: true}); err != nil {
				return fmt.Errorf("disable create component %q: %w", serviceComponent.Name(), err)
			}
		default:
			return fmt.Errorf("unsupported create component state %q for component %q", state, serviceComponent.Name())
		}
	}
	return nil
}

func (r serviceComponentRegistry) set(cmd *cobra.Command, name, argDisposition, stateFlag, dispositionFlag, projectPath string, yolo bool, followUps map[string]string) error {
	component, ok := r.componentByName(name)
	if !ok {
		return fmt.Errorf("unknown component %q", name)
	}
	def := component.Definition()
	state, disposition, err := resolveServiceSetState(def, argDisposition, stateFlag, dispositionFlag)
	if err != nil {
		return fmt.Errorf("resolve component %q state: %w", component.Name(), err)
	}

	ctx, err := r.resolveContext(projectPath)
	if err != nil {
		return fmt.Errorf("resolve component context: %w", err)
	}
	warnRemoteComponentMutation(cmd, ctx)

	if err := promptRequiredServiceFollowUps(cmd, def, disposition, yolo, followUps); err != nil {
		return err
	}

	manager := corecomponent.NewManager(ctx)
	spec := component.SpecForWithOptions(state, followUps)
	switch state {
	case corecomponent.StateOn:
		if err := manager.EnableComponentWithOptions(cmd.Context(), spec, corecomponent.ApplyOptions{Yolo: yolo}); err != nil {
			return fmt.Errorf("enable component %q: %w", component.Name(), err)
		}
	case corecomponent.StateOff:
		if err := manager.DisableComponentWithOptions(cmd.Context(), spec, corecomponent.ApplyOptions{Yolo: yolo}); err != nil {
			return fmt.Errorf("disable component %q: %w", component.Name(), err)
		}
	default:
		return fmt.Errorf("unsupported component state %q for component %q", state, component.Name())
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", component.Name(), disposition)
	return nil
}

func warnRemoteComponentMutation(cmd *cobra.Command, ctx *config.Context) {
	if cmd == nil || ctx == nil || ctx.DockerHostType != config.ContextRemote {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Warning: modifying remote project files directly; commit and review these changes through version control before promoting them.")
}

func (r serviceComponentRegistry) resolveContext(projectPath string) (*config.Context, error) {
	projectPath = strings.TrimSpace(projectPath)
	if projectPath != "" {
		abs, err := filepath.Abs(projectPath)
		if err != nil {
			return nil, fmt.Errorf("resolve project path %q: %w", projectPath, err)
		}
		ctx := &config.Context{
			Name:           ".",
			Plugin:         r.sdk.Metadata.Name,
			DockerHostType: config.ContextLocal,
			ProjectDir:     abs,
		}
		return ctx, nil
	}

	ctx, err := r.sdk.GetContext()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("no context resolved")
	}
	return ctx, nil
}

func serviceDisposition(state corecomponent.DetectedState, def corecomponent.Definition) corecomponent.Disposition {
	switch state {
	case corecomponent.DetectedState(corecomponent.StateOn):
		return corecomponent.DispositionEnabled
	case corecomponent.DetectedState(corecomponent.StateOff):
		return corecomponent.DispositionDisabled
	default:
		if def.DefaultDisposition != "" {
			return def.DefaultDisposition
		}
		return corecomponent.StateToDisposition(def.DefaultState)
	}
}

func resolveServiceSetState(def corecomponent.Definition, argDisposition, stateFlag, dispositionFlag string) (corecomponent.State, corecomponent.Disposition, error) {
	if strings.TrimSpace(stateFlag) != "" && (strings.TrimSpace(dispositionFlag) != "" || strings.TrimSpace(argDisposition) != "") {
		return "", "", fmt.Errorf("--state cannot be combined with a disposition")
	}
	if strings.TrimSpace(dispositionFlag) != "" && strings.TrimSpace(argDisposition) != "" {
		return "", "", fmt.Errorf("--disposition cannot be combined with a positional disposition")
	}

	if strings.TrimSpace(stateFlag) != "" {
		state, err := corecomponent.ParseState(stateFlag)
		if err != nil {
			return "", "", err
		}
		disposition := corecomponent.StateToDisposition(state)
		disposition, err = corecomponent.ResolveAllowedDisposition(def.AllowedDispositions, disposition)
		if err != nil {
			return "", "", err
		}
		return state, disposition, nil
	}

	value := strings.TrimSpace(dispositionFlag)
	if value == "" {
		value = strings.TrimSpace(argDisposition)
	}
	if value == "" {
		value = string(def.DefaultDisposition)
	}
	if value == "" {
		value = string(corecomponent.StateToDisposition(def.DefaultState))
	}
	disposition, err := corecomponent.ParseDisposition(value)
	if err != nil {
		return "", "", err
	}
	disposition, err = corecomponent.ResolveAllowedDisposition(def.AllowedDispositions, disposition)
	if err != nil {
		return "", "", err
	}
	return corecomponent.DispositionToState(disposition), disposition, nil
}

func normalizeComponentReportFormat(format string) string {
	format = strings.TrimSpace(format)
	if format == "" {
		return corecomponent.ReportFormatSection
	}
	return format
}

type serviceFollowUpFlagValue struct {
	name   string
	multi  bool
	single string
	values []string
}

func bindServiceFollowUpFlags(cmd *cobra.Command, defs []corecomponent.Definition, values map[string]*serviceFollowUpFlagValue) {
	if cmd == nil || values == nil {
		return
	}
	// Component set follow-up flags are forwarded through RPC Args. Args are
	// not covered by the argv sensitivity gate, so follow-ups must never collect
	// secret-bearing values.
	seen := map[string]bool{}
	for _, def := range defs {
		for _, followUp := range def.FollowUps {
			flagName := strings.TrimSpace(followUp.FlagName)
			if flagName == "" {
				flagName = strings.TrimSpace(followUp.Name)
			}
			if flagName == "" || seen[flagName] || cmd.Flags().Lookup(flagName) != nil {
				continue
			}
			seen[flagName] = true
			holder := &serviceFollowUpFlagValue{name: followUp.Name, multi: followUp.MultiValue}
			if followUp.MultiValue {
				holder.values = corecomponent.SplitFollowUpValues(followUp.DefaultValue)
			} else {
				holder.single = strings.TrimSpace(followUp.DefaultValue)
			}
			values[flagName] = holder
			usage := strings.TrimSpace(followUp.FlagUsage)
			if usage == "" {
				usage = fmt.Sprintf("%s option", followUp.Name)
			}
			if followUp.MultiValue {
				cmd.Flags().StringArrayVar(&holder.values, flagName, append([]string{}, holder.values...), usage)
			} else {
				cmd.Flags().StringVar(&holder.single, flagName, holder.single, usage)
			}
		}
	}
}

func collectServiceFollowUps(values map[string]*serviceFollowUpFlagValue) map[string]string {
	out := map[string]string{}
	for fallbackName, value := range values {
		if value == nil {
			continue
		}
		name := strings.TrimSpace(value.name)
		if name == "" {
			name = fallbackName
		}
		if value.multi {
			out[name] = corecomponent.JoinFollowUpValues(value.values)
			continue
		}
		out[name] = strings.TrimSpace(value.single)
	}
	return out
}

func promptRequiredServiceFollowUps(cmd *cobra.Command, def corecomponent.Definition, disposition corecomponent.Disposition, yolo bool, values map[string]string) error {
	for _, spec := range def.FollowUpsForDisposition(disposition) {
		if !spec.Required || corecomponent.FollowUpValuePresent(values[spec.Name]) {
			continue
		}
		flagName := strings.TrimSpace(spec.FlagName)
		if flagName == "" {
			flagName = strings.TrimSpace(spec.Name)
		}
		if yolo {
			if flagName != "" {
				return fmt.Errorf("--%s is required when enabling component %q", flagName, def.Name)
			}
			return fmt.Errorf("%s is required when enabling component %q", spec.Name, def.Name)
		}
		value, err := corecomponent.PromptFollowUp(def.Name, spec, strings.TrimSpace(spec.DefaultValue), config.GetInput, nil)
		if err != nil {
			return err
		}
		if spec.MultiValue {
			value = corecomponent.NormalizeFollowUpValue(value)
		} else {
			value = strings.TrimSpace(value)
		}
		if !corecomponent.FollowUpValuePresent(value) {
			return fmt.Errorf("%s is required when enabling component %q", spec.Name, def.Name)
		}
		values[spec.Name] = value
	}
	return nil
}
