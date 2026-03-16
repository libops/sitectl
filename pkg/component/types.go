package component

import "fmt"

type RepoSource struct {
	Repo string
	Ref  string
	Path string
}

type RuleOp string

const (
	OpSet     RuleOp = "set"
	OpDelete  RuleOp = "delete"
	OpRestore RuleOp = "restore"
	OpReplace RuleOp = "replace"
)

type YAMLRule struct {
	Files       []string
	Exclude     []string
	SourceFiles []string
	Op          RuleOp
	Path        string
	Value       any
	Old         any
}

type YAMLStateSpec struct {
	Canonical []RepoSource
	Rules     []YAMLRule
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
	Module          string
	ComposerPackage string
	Mode            DrupalModuleDependencyMode
}

type Dependencies struct {
	DrupalModules []DrupalModuleDependency
}

type FollowUpSpec struct {
	Name           string
	Label          string
	FlagName       string
	FlagUsage      string
	Question       string
	Choices        []Choice
	DefaultValue   string
	PromptOnCreate bool
	AppliesTo      State
	CustomPrompt   string
}

type DataMigrationRequirement string

const (
	DataMigrationNone     DataMigrationRequirement = "none"
	DataMigrationBackfill DataMigrationRequirement = "backfill"
	DataMigrationHard     DataMigrationRequirement = "hard"
)

type TransitionBehavior struct {
	DataMigration DataMigrationRequirement
	Summary       string
}

type Behavior struct {
	Idempotent bool
	Enable     TransitionBehavior
	Disable    TransitionBehavior
}

type DomainSpec struct {
	Compose YAMLStateSpec
	Drupal  YAMLStateSpec
}

type Definition struct {
	Name           string
	DefaultState   State
	Guidance       StateGuidance
	PromptOnCreate bool
	FollowUps      []FollowUpSpec
	Gates          GateSpec
	Dependencies   Dependencies
	Behavior       Behavior
	On             DomainSpec
	Off            DomainSpec
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
		Name:           d.Name,
		Default:        d.DefaultState,
		Guidance:       d.Guidance,
		PromptOnCreate: d.PromptOnCreate,
		FollowUps:      d.FollowUps,
	}
}

func (d Definition) FollowUpsForState(state State) []FollowUpSpec {
	if len(d.FollowUps) == 0 {
		return nil
	}
	out := make([]FollowUpSpec, 0, len(d.FollowUps))
	for _, spec := range d.FollowUps {
		if spec.Name == "" {
			continue
		}
		if spec.AppliesTo != "" && normalizeState(spec.AppliesTo) != normalizeState(state) {
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
