package component

import "fmt"

type RepoSource struct {
	Repo string `json:"repo,omitempty" yaml:"repo,omitempty"`
	Ref  string `json:"ref,omitempty" yaml:"ref,omitempty"`
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
}

type RuleOp string

const (
	OpSet         RuleOp = "set"
	OpDelete      RuleOp = "delete"
	OpRestore     RuleOp = "restore"
	OpReplace     RuleOp = "replace"
	OpContains    RuleOp = "contains"
	OpNotContains RuleOp = "not_contains"
)

type YAMLRule struct {
	Files       []string `json:"files,omitempty" yaml:"files,omitempty"`
	Exclude     []string `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	SourceFiles []string `json:"source_files,omitempty" yaml:"source_files,omitempty"`
	Op          RuleOp   `json:"op,omitempty" yaml:"op,omitempty"`
	Path        string   `json:"path,omitempty" yaml:"path,omitempty"`
	Value       any      `json:"value,omitempty" yaml:"value,omitempty"`
	Old         any      `json:"old,omitempty" yaml:"old,omitempty"`
}

type YAMLStateSpec struct {
	Canonical []RepoSource `json:"canonical,omitempty" yaml:"canonical,omitempty"`
	Rules     []YAMLRule   `json:"rules,omitempty" yaml:"rules,omitempty"`
}

// FileRule describes one project-file mutation or state check.
type FileRule struct {
	Files       []string `json:"files,omitempty" yaml:"files,omitempty"`
	Op          RuleOp   `json:"op,omitempty" yaml:"op,omitempty"`
	Path        string   `json:"path,omitempty" yaml:"path,omitempty"`
	Value       any      `json:"value,omitempty" yaml:"value,omitempty"`
	StartMarker string   `json:"start_marker,omitempty" yaml:"start_marker,omitempty"`
	EndMarker   string   `json:"end_marker,omitempty" yaml:"end_marker,omitempty"`
	Content     string   `json:"content,omitempty" yaml:"content,omitempty"`
}

// FileStateSpec groups project-file rules for a component state.
type FileStateSpec struct {
	Rules []FileRule `json:"rules,omitempty" yaml:"rules,omitempty"`
}

type DrupalModuleDependencyMode string

const (
	// DrupalModuleDependencyStrict means the module is part of the component's
	// core contract and should stay aligned with the component state.
	DrupalModuleDependencyStrict DrupalModuleDependencyMode = "strict"
	// DrupalModuleDependencyEnableOnly means the module must exist when the
	// component is enabled, but disabling the component does not imply removing
	// or uninstalling the module.
	DrupalModuleDependencyEnableOnly DrupalModuleDependencyMode = "enable_only"
)

type DrupalModuleDependency struct {
	Module          string                     `json:"module,omitempty" yaml:"module,omitempty"`
	ComposerPackage string                     `json:"composer_package,omitempty" yaml:"composer_package,omitempty"`
	Mode            DrupalModuleDependencyMode `json:"mode,omitempty" yaml:"mode,omitempty"`
}

type Dependencies struct {
	DrupalModules []DrupalModuleDependency `json:"drupal_modules,omitempty" yaml:"drupal_modules,omitempty"`
}

// FollowUpSpec describes a non-secret option collected after a component
// disposition decision. Follow-up values can be forwarded through plugin RPC
// passthrough args and may be visible in process listings during interactive
// calls; do not use follow-ups for tokens, passwords, secret keys, or other
// sensitive values.
type FollowUpSpec struct {
	Name                 string      `json:"name,omitempty" yaml:"name,omitempty"`
	Label                string      `json:"label,omitempty" yaml:"label,omitempty"`
	FlagName             string      `json:"flag_name,omitempty" yaml:"flag_name,omitempty"`
	FlagUsage            string      `json:"flag_usage,omitempty" yaml:"flag_usage,omitempty"`
	Question             string      `json:"question,omitempty" yaml:"question,omitempty"`
	Choices              []Choice    `json:"choices,omitempty" yaml:"choices,omitempty"`
	DefaultValue         string      `json:"default_value,omitempty" yaml:"default_value,omitempty"`
	BoolValue            bool        `json:"bool_value,omitempty" yaml:"bool_value,omitempty"`
	MultiValue           bool        `json:"multi_value,omitempty" yaml:"multi_value,omitempty"`
	Required             bool        `json:"required,omitempty" yaml:"required,omitempty"`
	PromptOnCreate       bool        `json:"prompt_on_create,omitempty" yaml:"prompt_on_create,omitempty"`
	AppliesTo            State       `json:"applies_to,omitempty" yaml:"applies_to,omitempty"`
	AppliesToDisposition Disposition `json:"applies_to_disposition,omitempty" yaml:"applies_to_disposition,omitempty"`
	CustomPrompt         string      `json:"custom_prompt,omitempty" yaml:"custom_prompt,omitempty"`
}

type DataMigrationRequirement string

const (
	DataMigrationNone     DataMigrationRequirement = "none"
	DataMigrationBackfill DataMigrationRequirement = "backfill"
	DataMigrationHard     DataMigrationRequirement = "hard"
)

type TransitionBehavior struct {
	DataMigration DataMigrationRequirement `json:"data_migration,omitempty" yaml:"data_migration,omitempty"`
	Summary       string                   `json:"summary,omitempty" yaml:"summary,omitempty"`
}

type Behavior struct {
	Idempotent bool               `json:"idempotent,omitempty" yaml:"idempotent,omitempty"`
	Enable     TransitionBehavior `json:"enable,omitempty" yaml:"enable,omitempty"`
	Disable    TransitionBehavior `json:"disable,omitempty" yaml:"disable,omitempty"`
}

type DomainSpec struct {
	Compose YAMLStateSpec `json:"compose,omitempty" yaml:"compose,omitempty"`
	Drupal  YAMLStateSpec `json:"drupal,omitempty" yaml:"drupal,omitempty"`
	Files   FileStateSpec `json:"files,omitempty" yaml:"files,omitempty"`
}

type Definition struct {
	Name                string         `json:"name" yaml:"name"`
	DefaultState        State          `json:"default_state,omitempty" yaml:"default_state,omitempty"`
	DefaultDisposition  Disposition    `json:"default_disposition,omitempty" yaml:"default_disposition,omitempty"`
	AllowedDispositions []Disposition  `json:"allowed_dispositions,omitempty" yaml:"allowed_dispositions,omitempty"`
	Guidance            StateGuidance  `json:"guidance,omitempty" yaml:"guidance,omitempty"`
	PromptOnCreate      bool           `json:"prompt_on_create,omitempty" yaml:"prompt_on_create,omitempty"`
	FollowUps           []FollowUpSpec `json:"follow_ups,omitempty" yaml:"follow_ups,omitempty"`
	Gates               GateSpec       `json:"gates,omitempty" yaml:"gates,omitempty"`
	Dependencies        Dependencies   `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	Behavior            Behavior       `json:"behavior,omitempty" yaml:"behavior,omitempty"`
	On                  DomainSpec     `json:"on,omitempty" yaml:"on,omitempty"`
	Off                 DomainSpec     `json:"off,omitempty" yaml:"off,omitempty"`
}

func (d Definition) DrupalModulesForEnable() []DrupalModuleDependency {
	return d.Dependencies.DrupalModulesForEnable()
}

func (d Definition) StrictDrupalModules() []DrupalModuleDependency {
	return d.Dependencies.StrictDrupalModules()
}

func (d Definition) ComposerPackagesForEnable() []string {
	return d.Dependencies.ComposerPackagesForEnable()
}

func (d Definition) StrictComposerPackages() []string {
	return d.Dependencies.StrictComposerPackages()
}

func (d Definition) CreateOption() CreateOption {
	return CreateOption{
		Name:                d.Name,
		Default:             d.DefaultState,
		DefaultDisposition:  d.DefaultDisposition,
		AllowedDispositions: append([]Disposition{}, d.AllowedDispositions...),
		Guidance:            d.Guidance,
		PromptOnCreate:      d.PromptOnCreate,
		FollowUps:           d.FollowUps,
	}
}

func (d Definition) FollowUpsForState(state State) []FollowUpSpec {
	return d.FollowUpsForDisposition(StateToDisposition(state))
}

func (d Definition) FollowUpsForDisposition(disposition Disposition) []FollowUpSpec {
	if len(d.FollowUps) == 0 {
		return nil
	}
	out := make([]FollowUpSpec, 0, len(d.FollowUps))
	for _, spec := range d.FollowUps {
		if spec.Name == "" {
			continue
		}
		if spec.AppliesToDisposition != "" && normalizeDisposition(spec.AppliesToDisposition) != normalizeDisposition(disposition) {
			continue
		}
		if spec.AppliesToDisposition == "" && spec.AppliesTo != "" && normalizeState(spec.AppliesTo) != normalizeState(DispositionToState(disposition)) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func ParseStateOverrides(values map[string]string) (map[string]State, error) {
	out := make(map[string]State, len(values))
	for name, value := range values {
		state, err := ParseState(value)
		if err != nil {
			return nil, fmt.Errorf("parse component %q state: %w", name, err)
		}
		out[name] = state
	}
	return out, nil
}

func (d Dependencies) DrupalModulesForEnable() []DrupalModuleDependency {
	if len(d.DrupalModules) == 0 {
		return nil
	}

	out := make([]DrupalModuleDependency, 0, len(d.DrupalModules))
	for _, module := range d.DrupalModules {
		if module.Module == "" {
			continue
		}
		switch module.Mode {
		case "", DrupalModuleDependencyStrict, DrupalModuleDependencyEnableOnly:
			out = append(out, module)
		}
	}
	return out
}

func (d Dependencies) StrictDrupalModules() []DrupalModuleDependency {
	if len(d.DrupalModules) == 0 {
		return nil
	}

	out := make([]DrupalModuleDependency, 0, len(d.DrupalModules))
	for _, module := range d.DrupalModules {
		if module.Module == "" || module.Mode != DrupalModuleDependencyStrict {
			continue
		}
		out = append(out, module)
	}
	return out
}

func (d Dependencies) ComposerPackagesForEnable() []string {
	return composerPackages(d.DrupalModules, false)
}

func (d Dependencies) StrictComposerPackages() []string {
	return composerPackages(d.DrupalModules, true)
}

func composerPackages(modules []DrupalModuleDependency, strictOnly bool) []string {
	if len(modules) == 0 {
		return nil
	}

	out := make([]string, 0, len(modules))
	seen := map[string]bool{}
	for _, module := range modules {
		if module.ComposerPackage == "" {
			continue
		}
		if strictOnly && module.Mode != DrupalModuleDependencyStrict {
			continue
		}
		if !strictOnly {
			switch module.Mode {
			case "", DrupalModuleDependencyStrict, DrupalModuleDependencyEnableOnly:
			default:
				continue
			}
		}
		if seen[module.ComposerPackage] {
			continue
		}
		seen[module.ComposerPackage] = true
		out = append(out, module.ComposerPackage)
	}
	return out
}
