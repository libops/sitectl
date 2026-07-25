package component

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

const (
	// DesiredStateAPIVersion identifies the desired-state contract understood by sitectl.
	DesiredStateAPIVersion = "sitectl.libops.io/v1alpha1"
	// DesiredStateKind identifies a sitectl desired-state document.
	DesiredStateKind = "SiteDesiredState"
	// DesiredStatePath is the project-relative desired-state document location.
	DesiredStatePath     = ".libops/site.yaml"
	maxDesiredStateBytes = 1 << 20
)

// ComponentSelection records the operator's durable intent for one component.
type ComponentSelection struct {
	Disposition Disposition       `json:"disposition" yaml:"disposition"`
	Settings    map[string]string `json:"settings,omitempty" yaml:"settings,omitempty"`
}

// DesiredStateSpec contains the component goals rendered by the active plugin.
type DesiredStateSpec struct {
	Plugin     string                        `json:"plugin" yaml:"plugin"`
	Components map[string]ComponentSelection `json:"components" yaml:"components"`
}

// DesiredState is the versioned, reviewable source of component intent.
type DesiredState struct {
	APIVersion string           `json:"apiVersion" yaml:"apiVersion"`
	Kind       string           `json:"kind" yaml:"kind"`
	Schema     int              `json:"schema" yaml:"schema"`
	Spec       DesiredStateSpec `json:"spec" yaml:"spec"`
}

// NewDesiredState creates an empty desired-state document for a plugin.
func NewDesiredState(pluginName string) DesiredState {
	return DesiredState{
		APIVersion: DesiredStateAPIVersion,
		Kind:       DesiredStateKind,
		Schema:     1,
		Spec: DesiredStateSpec{
			Plugin:     strings.TrimSpace(pluginName),
			Components: map[string]ComponentSelection{},
		},
	}
}

// DesiredStateFile resolves the fixed desired-state location for a context.
func DesiredStateFile(ctx *config.Context) (string, error) {
	if ctx == nil || strings.TrimSpace(ctx.ProjectDir) == "" {
		return "", errors.New("component desired state requires a project directory")
	}
	return filepath.Join(ctx.ProjectDir, filepath.FromSlash(DesiredStatePath)), nil
}

// LoadDesiredState reads and strictly validates the desired state.
func LoadDesiredState(ctx *config.Context) (DesiredState, error) {
	path, err := DesiredStateFile(ctx)
	if err != nil {
		return DesiredState{}, err
	}
	data, err := ctx.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return DesiredState{}, fmt.Errorf("desired state %s is missing: run component set for each managed component: %w", DesiredStatePath, err)
		}
		return DesiredState{}, err
	}
	if len(data) > maxDesiredStateBytes {
		return DesiredState{}, fmt.Errorf("desired state %s exceeds %d bytes", DesiredStatePath, maxDesiredStateBytes)
	}
	var state DesiredState
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&state); err != nil {
		return DesiredState{}, fmt.Errorf("parse desired state %s: %w", DesiredStatePath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return DesiredState{}, fmt.Errorf("desired state %s must contain exactly one YAML document", DesiredStatePath)
	} else if !errors.Is(err, io.EOF) {
		return DesiredState{}, fmt.Errorf("parse trailing content in desired state %s: %w", DesiredStatePath, err)
	}
	if err := state.Validate(ctx.Plugin); err != nil {
		return DesiredState{}, fmt.Errorf("validate desired state %s: %w", DesiredStatePath, err)
	}
	return state, nil
}

// SaveDesiredState validates and atomically publishes desired state through the context accessor.
func SaveDesiredState(ctx *config.Context, state DesiredState) error {
	if err := state.Validate(ctx.Plugin); err != nil {
		return err
	}
	path, err := DesiredStateFile(ctx)
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal desired state: %w", err)
	}
	if err := ctx.WriteFile(path, data); err != nil {
		return fmt.Errorf("publish desired state: %w", err)
	}
	return nil
}

// Validate rejects ambiguous or unsupported desired-state input.
func (s DesiredState) Validate(pluginName string) error {
	if s.APIVersion != DesiredStateAPIVersion || s.Kind != DesiredStateKind || s.Schema != 1 {
		return fmt.Errorf("unsupported contract %q kind %q schema %d", s.APIVersion, s.Kind, s.Schema)
	}
	if strings.TrimSpace(s.Spec.Plugin) == "" {
		return errors.New("spec.plugin is required")
	}
	if expected := strings.TrimSpace(pluginName); expected != "" && expected != "core" && s.Spec.Plugin != expected {
		return fmt.Errorf("spec.plugin %q does not match context plugin %q", s.Spec.Plugin, expected)
	}
	if s.Spec.Components == nil {
		return errors.New("spec.components is required")
	}
	for name, selection := range s.Spec.Components {
		if strings.TrimSpace(name) == "" {
			return errors.New("component names must not be empty")
		}
		if _, err := ParseDisposition(string(selection.Disposition)); err != nil {
			return fmt.Errorf("component %q: %w", name, err)
		}
		for key := range selection.Settings {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("component %q has an empty setting name", name)
			}
		}
	}
	return nil
}

// Set records one component goal after validating it against the definition.
func (s *DesiredState) Set(def Definition, disposition Disposition, settings map[string]string) error {
	if s == nil {
		return errors.New("desired state is nil")
	}
	resolved, err := ResolveAllowedDisposition(def.AllowedDispositions, disposition)
	if err != nil {
		return fmt.Errorf("component %q: %w", def.Name, err)
	}
	if s.Spec.Components == nil {
		s.Spec.Components = map[string]ComponentSelection{}
	}
	copied := make(map[string]string, len(settings))
	for key, value := range settings {
		copied[key] = value
	}
	s.Spec.Components[def.Name] = ComponentSelection{Disposition: resolved, Settings: copied}
	return nil
}

// DesiredStateFromDecisions converts reviewed create choices into durable intent.
func DesiredStateFromDecisions(pluginName string, definitions []Definition, decisions map[string]ReviewDecision) (DesiredState, error) {
	state := NewDesiredState(pluginName)
	for _, def := range definitions {
		decision, ok := decisions[def.Name]
		if !ok {
			disposition := def.DefaultDisposition
			if disposition == "" {
				disposition = StateToDisposition(def.DefaultState)
			}
			decision = ReviewDecision{Disposition: disposition}
		}
		disposition := decision.Disposition
		if disposition == "" {
			disposition = StateToDisposition(decision.State)
		}
		if err := state.Set(def, disposition, decision.Options); err != nil {
			return DesiredState{}, err
		}
	}
	known := make(map[string]bool, len(definitions))
	for _, def := range definitions {
		known[def.Name] = true
	}
	names := make([]string, 0, len(decisions))
	for name := range decisions {
		if !known[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return DesiredState{}, fmt.Errorf("desired component %q is not registered by plugin %q", names[0], pluginName)
	}
	return state, nil
}

// LoadOrInitializeDesiredState loads durable intent or initializes every registered component from its default.
func LoadOrInitializeDesiredState(ctx *config.Context, definitions []Definition) (DesiredState, error) {
	state, err := LoadDesiredState(ctx)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return DesiredState{}, err
	}
	return DesiredStateFromDecisions(ctx.Plugin, definitions, nil)
}
