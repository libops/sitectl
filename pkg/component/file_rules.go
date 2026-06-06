package component

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/libops/sitectl/pkg/config"
)

func applyFileState(ctx *config.Context, root string, spec FileStateSpec) error {
	if ctx == nil || len(spec.Rules) == 0 {
		return nil
	}
	for _, rule := range spec.Rules {
		targets, err := fileRuleTargets(ctx, root, rule)
		if err != nil {
			return err
		}
		for _, rel := range targets {
			if err := applyFileRule(ctx, filepath.Join(root, filepath.FromSlash(rel)), rule); err != nil {
				return fmt.Errorf("apply file rule %q to %q: %w", rule.Op, rel, err)
			}
		}
	}
	return nil
}

func applyFileRule(ctx *config.Context, path string, rule FileRule) error {
	data, err := ctx.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read file: %w", err)
		}
		if rule.Op == OpDelete {
			return nil
		}
		data = nil
	}

	var updated []byte
	if isJSONFileRule(rule) {
		updated, err = applyJSONFileRule(data, rule)
	} else {
		updated, err = applyTextFileRule(data, rule)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(data, updated) {
		return nil
	}
	if err := ctx.WriteFile(path, updated); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func applyJSONFileRule(data []byte, rule FileRule) ([]byte, error) {
	path, err := parseFileRulePath(rule.Path)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("json file rule path cannot be empty")
	}

	root := map[string]any{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("decode json: %w", err)
		}
	}

	switch rule.Op {
	case OpSet, OpRestore:
		if err := setJSONObjectPath(root, path, rule.Value); err != nil {
			return nil, err
		}
	case OpDelete:
		deleteJSONObjectPath(root, path)
	default:
		return nil, fmt.Errorf("unsupported json file rule op %q", rule.Op)
	}

	updated, err := json.MarshalIndent(root, "", "    ")
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return append(updated, '\n'), nil
}

func applyTextFileRule(data []byte, rule FileRule) ([]byte, error) {
	switch rule.Op {
	case OpSet, OpRestore:
		return []byte(ensureMarkedBlock(string(data), rule)), nil
	case OpDelete:
		updated, _ := removeMarkedBlock(string(data), rule)
		return []byte(updated), nil
	default:
		return nil, fmt.Errorf("unsupported text file rule op %q", rule.Op)
	}
}

func evaluateFileState(ctx *config.Context, root string, spec FileStateSpec) ([]RuleCheckResult, error) {
	if len(spec.Rules) == 0 {
		return nil, nil
	}

	cache := map[string][]byte{}
	results := []RuleCheckResult{}
	for _, rule := range spec.Rules {
		targets, err := fileRuleTargets(ctx, root, rule)
		if err != nil {
			return nil, err
		}
		for _, rel := range targets {
			ok, detail, err := evaluateFileRule(ctx, filepath.Join(root, filepath.FromSlash(rel)), rule, cache)
			if err != nil {
				return nil, fmt.Errorf("evaluate file rule %q in %q: %w", rule.Op, rel, err)
			}
			results = append(results, RuleCheckResult{
				Domain: "files",
				File:   rel,
				Op:     rule.Op,
				Path:   fileRuleDisplayPath(rule),
				Match:  ok,
				Detail: detail,
			})
		}
	}
	return results, nil
}

func evaluateFileRule(ctx *config.Context, path string, rule FileRule, cache map[string][]byte) (bool, string, error) {
	data, exists, err := readCachedFile(ctx, path, cache)
	if err != nil {
		return false, "", err
	}
	if !exists {
		if rule.Op == OpDelete {
			return true, "file absent", nil
		}
		return false, "file missing", nil
	}

	if isJSONFileRule(rule) {
		value, found, err := jsonFileRuleValue(data, rule.Path)
		if err != nil {
			return false, "", err
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
		case OpRestore:
			if !found {
				return false, "expected path to exist", nil
			}
			return true, "path exists", nil
		case OpDelete:
			if found {
				return false, "expected path to be absent", nil
			}
			return true, "path absent", nil
		default:
			return false, "unsupported json file operation", nil
		}
	}

	present := textFileRulePresent(string(data), rule)
	switch rule.Op {
	case OpSet, OpRestore:
		if present {
			return true, "marked block present", nil
		}
		return false, "expected marked block", nil
	case OpDelete:
		if present {
			return false, "expected marked block to be absent", nil
		}
		return true, "marked block absent", nil
	default:
		return false, "unsupported text file operation", nil
	}
}

func fileRuleTargets(ctx *config.Context, root string, rule FileRule) ([]string, error) {
	if len(rule.Files) == 0 {
		return nil, fmt.Errorf("file rule needs at least one target file")
	}

	files, err := ctx.ListFiles(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("list files under %q: %w", root, err)
	}
	available := make([]string, 0, len(files))
	for _, file := range files {
		available = append(available, filepath.ToSlash(file))
	}
	matched := matchRuleFiles(available, rule.Files, nil)
	if len(matched) > 0 {
		return matched, nil
	}
	return syntheticRuleTargets(rule.Files), nil
}

func isJSONFileRule(rule FileRule) bool {
	return strings.TrimSpace(rule.Path) != ""
}

func fileRuleDisplayPath(rule FileRule) string {
	if strings.TrimSpace(rule.Path) != "" {
		return rule.Path
	}
	if strings.TrimSpace(rule.StartMarker) != "" {
		return rule.StartMarker
	}
	return "."
}

func parseFileRulePath(path string) ([]string, error) {
	if path == "" || path == "." {
		return nil, nil
	}
	if !strings.HasPrefix(path, ".") {
		return nil, fmt.Errorf("invalid file rule path %q", path)
	}
	trimmed := strings.TrimPrefix(path, ".")
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, ".")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("invalid file rule path %q", path)
		}
	}
	return parts, nil
}

func setJSONObjectPath(root map[string]any, path []string, value any) error {
	current := root
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment]
		if !ok {
			child := map[string]any{}
			current[segment] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("json path segment %q is not an object", segment)
		}
		current = child
	}
	current[path[len(path)-1]] = value
	return nil
}

func deleteJSONObjectPath(root map[string]any, path []string) {
	current := root
	for _, segment := range path[:len(path)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, path[len(path)-1])
}

func jsonFileRuleValue(data []byte, path string) (any, bool, error) {
	segments, err := parseFileRulePath(path)
	if err != nil {
		return nil, false, err
	}
	if len(segments) == 0 {
		return nil, false, fmt.Errorf("json file rule path cannot be empty")
	}

	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("decode json: %w", err)
	}
	current := root
	for _, segment := range segments {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		next, ok := object[segment]
		if !ok {
			return nil, false, nil
		}
		current = next
	}
	return current, true, nil
}

func ensureMarkedBlock(data string, rule FileRule) string {
	without, _ := removeMarkedBlock(data, rule)
	content := strings.TrimRight(rule.Content, "\n") + "\n"
	block := rule.StartMarker + "\n" + content + rule.EndMarker + "\n"

	trimmed := strings.TrimSpace(without)
	if trimmed == "" {
		return block
	}
	return strings.TrimRight(without, "\n") + "\n\n" + block
}

func removeMarkedBlock(data string, rule FileRule) (string, bool) {
	start := strings.TrimSpace(rule.StartMarker)
	end := strings.TrimSpace(rule.EndMarker)
	if start == "" || end == "" {
		content := strings.TrimRight(rule.Content, "\n")
		if content == "" {
			return data, false
		}
		updated := strings.Replace(data, content, "", 1)
		return updated, updated != data
	}

	startAt := strings.Index(data, start)
	if startAt < 0 {
		return data, false
	}
	endAt := strings.Index(data[startAt:], end)
	if endAt < 0 {
		return data, false
	}
	endAt = startAt + endAt + len(end)
	for endAt < len(data) && (data[endAt] == '\r' || data[endAt] == '\n') {
		endAt++
	}

	prefix := strings.TrimRight(data[:startAt], "\r\n")
	suffix := strings.TrimLeft(data[endAt:], "\r\n")
	switch {
	case prefix == "":
		return suffix, true
	case suffix == "":
		return prefix + "\n", true
	default:
		return prefix + "\n\n" + suffix, true
	}
}

func textFileRulePresent(data string, rule FileRule) bool {
	start := strings.TrimSpace(rule.StartMarker)
	end := strings.TrimSpace(rule.EndMarker)
	content := strings.TrimRight(rule.Content, "\n")
	if start == "" || end == "" {
		return content == "" || strings.Contains(data, content)
	}
	startAt := strings.Index(data, start)
	if startAt < 0 {
		return false
	}
	endAt := strings.Index(data[startAt:], end)
	if endAt < 0 {
		return false
	}
	block := data[startAt : startAt+endAt+len(end)]
	return content == "" || strings.Contains(block, content)
}
