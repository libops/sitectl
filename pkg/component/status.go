package component

import (
	"bytes"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/libops/sitectl/pkg/config"
	yaml "gopkg.in/yaml.v3"
)

type DetectedState string

const StateDrifted DetectedState = "drifted"

type DetectOptions struct {
	ComposeRoot  string
	DrupalRootfs string
	DrupalRoot   string
}

type RuleCheckResult struct {
	Domain string
	File   string
	Op     RuleOp
	Path   string
	Match  bool
	Detail string
}

type StateCheck struct {
	State   State
	Passed  int
	Failed  int
	Results []RuleCheckResult
}

type ComponentStatus struct {
	Name  string
	State DetectedState
	On    StateCheck
	Off   StateCheck
}

func DetectComponentStatus(ctx *config.Context, projectRoot string, def Definition, opts DetectOptions) (ComponentStatus, error) {
	composeRoot := opts.ComposeRoot
	if composeRoot == "" {
		composeRoot = projectRoot
	}
	drupalRoot := opts.DrupalRoot
	if opts.DrupalRootfs != "" {
		drupalRoot = ResolveDrupalLayout(projectRoot, opts.DrupalRootfs).ConfigSyncDir()
	} else if drupalRoot == "" {
		drupalRoot = ResolveDrupalLayout(projectRoot, "").ConfigSyncDir()
	}

	on, err := evaluateDomainSpec(ctx, composeRoot, drupalRoot, StateOn, def.On)
	if err != nil {
		return ComponentStatus{}, err
	}
	off, err := evaluateDomainSpec(ctx, composeRoot, drupalRoot, StateOff, def.Off)
	if err != nil {
		return ComponentStatus{}, err
	}

	status := ComponentStatus{
		Name: def.Name,
		On:   on,
		Off:  off,
	}

	switch {
	case on.Failed == 0 && off.Failed > 0:
		status.State = DetectedState(StateOn)
	case off.Failed == 0 && on.Failed > 0:
		status.State = DetectedState(StateOff)
	default:
		status.State = StateDrifted
	}

	return status, nil
}

func DetectComponentStatuses(ctx *config.Context, projectRoot string, opts DetectOptions, defs ...Definition) ([]ComponentStatus, error) {
	statuses := make([]ComponentStatus, 0, len(defs))
	for _, def := range defs {
		status, err := DetectComponentStatus(ctx, projectRoot, def, opts)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Name < statuses[j].Name
	})
	return statuses, nil
}

func evaluateDomainSpec(ctx *config.Context, composeRoot, drupalRoot string, state State, spec DomainSpec) (StateCheck, error) {
	results, err := evaluateYAMLState(ctx, composeRoot, "compose", spec.Compose)
	if err != nil {
		return StateCheck{}, err
	}
	drupalResults, err := evaluateYAMLState(ctx, drupalRoot, "drupal", spec.Drupal)
	if err != nil {
		return StateCheck{}, err
	}
	results = append(results, drupalResults...)

	check := StateCheck{State: state, Results: results}
	for _, result := range results {
		if result.Match {
			check.Passed++
		} else {
			check.Failed++
		}
	}
	return check, nil
}

func evaluateYAMLState(ctx *config.Context, root, domain string, spec YAMLStateSpec) ([]RuleCheckResult, error) {
	if len(spec.Rules) == 0 {
		return nil, nil
	}

	availableFiles, err := listYAMLFiles(ctx, root)
	if err != nil {
		return nil, err
	}

	cache := map[string][]byte{}
	results := []RuleCheckResult{}
	for _, rule := range spec.Rules {
		matched := matchRuleFiles(availableFiles, rule.Files, rule.Exclude)
		if len(matched) == 0 {
			matched = syntheticRuleTargets(rule.Files)
		}
		for _, rel := range matched {
			ok, detail, err := evaluateRuleForFile(ctx, root, rel, rule, cache)
			if err != nil {
				return nil, err
			}
			results = append(results, RuleCheckResult{
				Domain: domain,
				File:   rel,
				Op:     rule.Op,
				Path:   rule.Path,
				Match:  ok,
				Detail: detail,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Domain != results[j].Domain {
			return results[i].Domain < results[j].Domain
		}
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		if results[i].Path != results[j].Path {
			return results[i].Path < results[j].Path
		}
		return results[i].Op < results[j].Op
	})

	return results, nil
}

func listYAMLFiles(ctx *config.Context, root string) ([]string, error) {
	files, err := ctx.ListFiles(root)
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") {
			return nil, nil
		}
		return nil, fmt.Errorf("list yaml files under %q: %w", root, err)
	}

	out := []string{}
	for _, rel := range files {
		normalized := filepath.ToSlash(rel)
		ext := filepath.Ext(normalized)
		if ext == ".yml" || ext == ".yaml" || normalized == "docker-compose.yml" {
			out = append(out, normalized)
		}
	}
	return out, nil
}

func matchRuleFiles(available, patterns, exclude []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	matches := []string{}
	seen := map[string]bool{}
	for _, file := range available {
		if !matchesAnyPattern(file, patterns) || matchesAnyPattern(file, exclude) {
			continue
		}
		if !seen[file] {
			seen[file] = true
			matches = append(matches, file)
		}
	}
	return matches
}

func syntheticRuleTargets(patterns []string) []string {
	out := []string{}
	for _, pattern := range patterns {
		if pattern == "" || hasGlob(pattern) {
			continue
		}
		out = append(out, filepath.ToSlash(pattern))
	}
	return out
}

func matchesAnyPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		ok, err := filepath.Match(filepath.ToSlash(pattern), name)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func hasGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func evaluateRuleForFile(ctx *config.Context, root, rel string, rule YAMLRule, cache map[string][]byte) (bool, string, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	data, exists, err := readCachedFile(ctx, path, cache)
	if err != nil {
		return false, "", err
	}

	if rule.Path == "." {
		switch rule.Op {
		case OpDelete:
			if exists {
				return false, "expected file to be absent", nil
			}
			return true, "file absent", nil
		case OpRestore:
			if !exists {
				return false, "expected file to exist", nil
			}
			return true, "file exists", nil
		default:
			return false, "unsupported whole-file operation", nil
		}
	}

	if !exists {
		switch rule.Op {
		case OpDelete:
			return true, "file absent, path absent", nil
		default:
			return false, "file missing", nil
		}
	}

	switch {
	case rule.Op == OpReplace && rule.Path == ".**":
		oldValue := fmt.Sprint(rule.Old)
		if oldValue == "" {
			return true, "no old value specified", nil
		}
		if bytes.Contains(data, []byte(oldValue)) {
			return false, fmt.Sprintf("found unreplaced value %q", oldValue), nil
		}
		return true, fmt.Sprintf("value %q not present", oldValue), nil
	case strings.HasPrefix(rule.Path, ".**."):
		key := strings.TrimPrefix(rule.Path, ".**.")
		found, err := yamlPathKeyExists(data, key)
		if err != nil {
			return false, "", fmt.Errorf("evaluate wildcard path %q in %q: %w", rule.Path, rel, err)
		}
		switch rule.Op {
		case OpDelete:
			if found {
				return false, fmt.Sprintf("found key %q", key), nil
			}
			return true, fmt.Sprintf("key %q absent", key), nil
		case OpRestore:
			if !found {
				return false, fmt.Sprintf("expected key %q", key), nil
			}
			return true, fmt.Sprintf("key %q present", key), nil
		default:
			return false, "unsupported wildcard operation", nil
		}
	default:
		value, found, err := yamlPathValue(data, rule.Path)
		if err != nil {
			return false, "", fmt.Errorf("evaluate path %q in %q: %w", rule.Path, rel, err)
		}
		switch rule.Op {
		case OpSet:
			if !found {
				return false, "expected path to exist", nil
			}
			if reflect.DeepEqual(value, rule.Value) || fmt.Sprint(value) == fmt.Sprint(rule.Value) {
				return true, fmt.Sprintf("path set to %v", rule.Value), nil
			}
			return false, fmt.Sprintf("expected %v, got %v", rule.Value, value), nil
		case OpDelete:
			if found {
				return false, "expected path to be absent", nil
			}
			return true, "path absent", nil
		case OpRestore:
			if !found {
				return false, "expected path to exist", nil
			}
			return true, "path exists", nil
		case OpReplace:
			oldValue := fmt.Sprint(rule.Old)
			if found && fmt.Sprint(value) == oldValue {
				return false, fmt.Sprintf("found unreplaced value %q", oldValue), nil
			}
			return true, fmt.Sprintf("value %q not present at path", oldValue), nil
		default:
			return false, "unsupported operation", nil
		}
	}
}

func readCachedFile(ctx *config.Context, path string, cache map[string][]byte) ([]byte, bool, error) {
	if data, ok := cache[path]; ok {
		if data == nil {
			return nil, false, nil
		}
		return data, true, nil
	}
	data, err := ctx.ReadFile(path)
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") {
			cache[path] = nil
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read file %q: %w", path, err)
	}
	cache[path] = data
	return data, true, nil
}

func yamlPathKeyExists(data []byte, target string) (bool, error) {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false, err
	}
	return recursiveKeyExists(root, target), nil
}

func recursiveKeyExists(node any, target string) bool {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if key == target || recursiveKeyExists(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if recursiveKeyExists(child, target) {
				return true
			}
		}
	}
	return false
}

func yamlPathValue(data []byte, path string) (any, bool, error) {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false, err
	}
	if path == "." {
		return root, true, nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "."), ".")
	current := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		next, ok := m[part]
		if !ok {
			return nil, false, nil
		}
		current = next
	}
	return current, true, nil
}
