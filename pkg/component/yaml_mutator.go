package component

import (
	"bytes"
	"container/list"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/mikefarah/yq/v4/pkg/yqlib"
	yaml "gopkg.in/yaml.v3"
)

var yqlibExpressionParserOnce sync.Once

// YAMLDocument is an editable, formatting-preserving YAML document.
type YAMLDocument struct {
	node *yqlib.CandidateNode
}

// LoadYAMLDocument parses YAML into a mutable document.
func LoadYAMLDocument(data []byte) (*YAMLDocument, error) {
	doc := &YAMLDocument{}
	if len(bytes.TrimSpace(data)) == 0 {
		doc.node = newYAMLMappingCandidate()
		return doc, nil
	}
	decoder := yqlib.NewYamlDecoder(yamlPreferences())
	if err := decoder.Init(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("decode yaml document: %w", err)
	}
	node, err := decoder.Decode()
	if err != nil {
		return nil, fmt.Errorf("decode yaml document: %w", err)
	}
	if node == nil {
		node = newYAMLMappingCandidate()
	}
	doc.node = node
	return doc, nil
}

// Bytes marshals the YAML document back to bytes.
func (d *YAMLDocument) Bytes() ([]byte, error) {
	if d == nil || d.node == nil {
		return nil, fmt.Errorf("marshal yaml document: document is not initialized")
	}
	node := d.node.Copy()
	stripImplicitMergeTags(node)
	prefs := yamlPreferences()
	prefs.UnwrapScalar = false
	encoder := yqlib.NewYamlEncoder(prefs)
	var buf bytes.Buffer
	if err := encoder.PrintLeadingContent(&buf, node.LeadingContent); err != nil {
		return nil, fmt.Errorf("marshal yaml document leading content: %w", err)
	}
	if err := encoder.Encode(&buf, node); err != nil {
		return nil, fmt.Errorf("marshal yaml document: %w", err)
	}
	return buf.Bytes(), nil
}

// DeletePath removes a yq path when present.
func (d *YAMLDocument) DeletePath(path string) error {
	if err := validateNonRootYAMLPath(path, "delete", "deletion"); err != nil {
		return err
	}
	context, err := d.existingYAMLNodes(path)
	if err != nil {
		return fmt.Errorf("delete path %q: %w", path, err)
	}
	if _, err := evaluateYAMLContext(context, `del(.)`, nil); err != nil {
		return fmt.Errorf("delete path %q: %w", path, err)
	}
	return nil
}

// HasPath reports whether a yq path matches at least one node.
func (d *YAMLDocument) HasPath(path string) (bool, error) {
	if isRootYAMLPath(path) {
		return d != nil && d.node != nil && d.node.Kind == yqlib.MappingNode, nil
	}
	if err := validateYAMLPath(path); err != nil {
		return false, err
	}
	context, err := d.existingYAMLNodes(path)
	if err != nil {
		return false, fmt.Errorf("read path %q: %w", path, err)
	}
	return context.MatchingNodes.Len() > 0, nil
}

// SetString writes a string scalar at a yq path.
func (d *YAMLDocument) SetString(path, value string) error {
	if err := validateNonRootYAMLPath(path, "set", "assignment"); err != nil {
		return err
	}
	style := yqlib.DoubleQuotedStyle
	if keys, ok := literalYAMLPathKeys(path); ok {
		var existing *yqlib.CandidateNode
		if d != nil {
			existing = yamlCandidateAtKeys(d.node, keys...)
		}
		if existing != nil &&
			existing.Kind == yqlib.ScalarNode && existing.Style != 0 {
			style = existing.Style
		}
		if err := d.assignYAMLKeys(keys, newYAMLStringCandidate(value, style)); err != nil {
			return fmt.Errorf("set path %q: %w", path, err)
		}
		return nil
	}
	if err := d.assignExistingOrCreate(path, newYAMLStringCandidate(value, style)); err != nil {
		return fmt.Errorf("set path %q: %w", path, err)
	}
	return nil
}

// SetValue writes an arbitrary YAML value at a yq path.
func (d *YAMLDocument) SetValue(path string, value any) error {
	if err := validateNonRootYAMLPath(path, "set", "assignment"); err != nil {
		return err
	}
	candidate, err := yamlCandidateForValue(value)
	if err != nil {
		return fmt.Errorf("set path %q: %w", path, err)
	}
	if err := d.assignExistingOrCreate(path, candidate); err != nil {
		return fmt.Errorf("set path %q: %w", path, err)
	}
	return nil
}

// AppendUniqueString appends value to each scalar or sequence at path.
func (d *YAMLDocument) AppendUniqueString(path, value string) error {
	if err := validateNonRootYAMLPath(path, "append", "assignment"); err != nil {
		return err
	}
	keys, literalPath := literalYAMLPathKeys(path)
	context, err := d.existingYAMLNodes(path)
	if err != nil {
		return fmt.Errorf("append path %q: %w", path, err)
	}
	if context.MatchingNodes.Len() == 0 {
		if literalPath {
			sequence := &yqlib.CandidateNode{Kind: yqlib.SequenceNode, Tag: "!!seq", Content: []*yqlib.CandidateNode{
				newYAMLStringCandidate(value, 0),
			}}
			if err := d.assignYAMLKeys(keys, sequence); err != nil {
				return fmt.Errorf("append path %q: %w", path, err)
			}
			return nil
		}
		expression := `( (` + path + `) | select(tag == "!!null") ) = [$value]`
		if _, err := d.evaluateWithValues(expression, true, map[string]any{"value": value}); err != nil {
			return fmt.Errorf("append path %q: %w", path, err)
		}
		if err := d.requireYAMLPath(path); err != nil {
			return fmt.Errorf("append path %q: %w", path, err)
		}
		return nil
	}
	for element := context.MatchingNodes.Front(); element != nil; element = element.Next() {
		target := element.Value.(*yqlib.CandidateNode)
		if target.Kind != yqlib.ScalarNode && target.Kind != yqlib.SequenceNode {
			return fmt.Errorf("append path %q: target is not a sequence", path)
		}
		if target.Kind == yqlib.ScalarNode && isBlockScalar(target) && !scalarStringContains(target.Value, value) {
			target.Value = appendScalarString(target.Value, value)
		}
	}
	expression := `
(. | select(kind == "seq" and (any_c((to_string | @base64) == ($value | @base64)) | not))) += [$value] |
(. | select(kind == "scalar" and tag != "!!null" and style != "folded" and style != "literal" and (to_string | @base64) != ($value | @base64))) |= [(to_string), $value] |
(. | select(tag == "!!null")) = [$value]`
	if _, err := evaluateYAMLContext(context, expression, map[string]*yqlib.CandidateNode{
		"value": newYAMLStringCandidate(value, 0),
	}); err != nil {
		return fmt.Errorf("append path %q: %w", path, err)
	}
	return nil
}

// RemoveString removes value from each scalar or sequence at path.
func (d *YAMLDocument) RemoveString(path, value string) error {
	if err := validateNonRootYAMLPath(path, "remove", "deletion"); err != nil {
		return err
	}
	context, err := d.existingYAMLNodes(path)
	if err != nil {
		return fmt.Errorf("remove path %q: %w", path, err)
	}
	var emptyBlocks []*yqlib.CandidateNode
	for element := context.MatchingNodes.Front(); element != nil; element = element.Next() {
		target := element.Value.(*yqlib.CandidateNode)
		if target.Kind != yqlib.ScalarNode || !isBlockScalar(target) {
			continue
		}
		updated, changed := removeScalarString(target.Value, value)
		if changed {
			target.Value = updated
			if strings.TrimSpace(updated) == "" {
				emptyBlocks = append(emptyBlocks, target)
			}
		}
	}
	if err := deleteYAMLCandidates(emptyBlocks...); err != nil {
		return fmt.Errorf("remove path %q: %w", path, err)
	}
	expression := `
del(. | select(kind == "scalar" and style != "folded" and style != "literal" and (to_string | @base64) == ($value | @base64))) |
(. | select(kind == "seq")) |= map(select(kind != "scalar" or (to_string | @base64) != ($value | @base64))) |
del(. | select(kind == "seq" and length == 0))`
	if _, err := evaluateYAMLContext(context, expression, map[string]*yqlib.CandidateNode{
		"value": newYAMLStringCandidate(value, 0),
	}); err != nil {
		return fmt.Errorf("remove path %q: %w", path, err)
	}
	return nil
}

// RemoveMatchingString removes string values matching match at path.
func (d *YAMLDocument) RemoveMatchingString(path string, match func(string) bool) (bool, error) {
	if match == nil {
		return false, fmt.Errorf("remove path %q: nil string matcher", path)
	}
	if err := validateNonRootYAMLPath(path, "remove", "deletion"); err != nil {
		return false, err
	}
	context, err := d.existingYAMLNodes(path)
	if err != nil {
		return false, fmt.Errorf("remove path %q: %w", path, err)
	}
	changed := false
	var deleteNodes []*yqlib.CandidateNode
	for element := context.MatchingNodes.Front(); element != nil; element = element.Next() {
		target := element.Value.(*yqlib.CandidateNode)
		if target.Kind == yqlib.ScalarNode {
			if !isBlockScalar(target) && match(target.Value) {
				deleteNodes = append(deleteNodes, target)
				changed = true
				continue
			}
			updated, matched := removeScalarStringsMatching(target.Value, match)
			if matched {
				changed = true
				target.Value = updated
				if strings.TrimSpace(updated) == "" {
					deleteNodes = append(deleteNodes, target)
				}
			}
			continue
		}
		if target.Kind != yqlib.SequenceNode {
			continue
		}
		var matches []*yqlib.CandidateNode
		for _, child := range target.Content {
			if child.Kind == yqlib.ScalarNode && match(child.Value) {
				matches = append(matches, child)
			}
		}
		if len(matches) == 0 {
			continue
		}
		changed = true
		if len(matches) == len(target.Content) {
			deleteNodes = append(deleteNodes, target)
		} else {
			deleteNodes = append(deleteNodes, matches...)
		}
	}
	if err := deleteYAMLCandidates(deleteNodes...); err != nil {
		return changed, fmt.Errorf("remove path %q: %w", path, err)
	}
	return changed, nil
}

func scalarStringContains(existing, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return strings.Contains(existing, value)
	}
	for _, field := range strings.Fields(existing) {
		if field == value {
			return true
		}
	}
	return false
}

func appendScalarString(existing, value string) string {
	if strings.TrimSpace(existing) == "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimRight(existing, "\r\n") + "\n" + strings.TrimSpace(value)
}

func removeScalarString(existing, value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return existing, false
	}
	if strings.ContainsAny(value, " \t\r\n") {
		if !strings.Contains(existing, value) {
			return existing, false
		}
		return strings.Replace(existing, value, "", 1), true
	}
	return removeScalarStringsMatching(existing, func(candidate string) bool { return candidate == value })
}

func removeScalarStringsMatching(existing string, match func(string) bool) (string, bool) {
	separator := " "
	parts := strings.Fields(existing)
	if strings.ContainsAny(existing, "\r\n") {
		separator = "\n"
		parts = strings.Split(strings.ReplaceAll(existing, "\r\n", "\n"), "\n")
	}
	filtered := parts[:0]
	changed := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if match(part) {
			changed = true
		} else {
			filtered = append(filtered, part)
		}
	}
	if !changed {
		return existing, false
	}
	return strings.Join(filtered, separator), true
}

func (d *YAMLDocument) assignExistingOrCreate(path string, value *yqlib.CandidateNode) error {
	if keys, ok := literalYAMLPathKeys(path); ok {
		return d.assignYAMLKeys(keys, value)
	}
	existing, err := d.matchingExistingNodes(path)
	if err != nil {
		return err
	}
	if _, err := d.evaluateWithCandidates(`(`+path+`) = $sitectl_value`, true,
		map[string]*yqlib.CandidateNode{"sitectl_value": value}); err != nil {
		return err
	}
	if existing.MatchingNodes.Len() > 0 {
		return nil
	}
	return d.requireYAMLPath(path)
}

func (d *YAMLDocument) requireYAMLPath(path string) error {
	context, err := d.matchingExistingNodes(path)
	if err != nil {
		return err
	}
	if context.MatchingNodes.Len() == 0 {
		return fmt.Errorf("path was not created")
	}
	return nil
}

func (d *YAMLDocument) assignYAMLKeys(keys []string, value *yqlib.CandidateNode) error {
	if len(keys) == 0 {
		return fmt.Errorf("yaml key assignment requires at least one key")
	}
	if d == nil || d.node == nil {
		return fmt.Errorf("yaml document is not initialized")
	}
	if d.node.Kind != yqlib.MappingNode {
		d.node = d.node.CreateReplacementWithComments(yqlib.MappingNode, "!!map", 0)
	}

	mapping := d.node
	for _, key := range keys[:len(keys)-1] {
		node := yamlMappingValue(mapping, key)
		if node == nil {
			if err := setExactYAMLMapEntry(mapping, key, newYAMLMappingCandidate()); err != nil {
				return err
			}
			node = yamlMappingValue(mapping, key)
		}
		if node.Kind == yqlib.ScalarNode && node.Tag == "!!null" {
			if _, err := evaluateYAMLContext(yqlib.Context{MatchingNodes: node.AsList()}, `. = $sitectl_value`, map[string]*yqlib.CandidateNode{
				"sitectl_value": newYAMLMappingCandidate(),
			}); err != nil {
				return err
			}
		}
		if node.Kind != yqlib.MappingNode {
			return fmt.Errorf("segment %q is not a mapping", key)
		}
		mapping = node
	}
	return setExactYAMLMapEntry(mapping, keys[len(keys)-1], value)
}

func setExactYAMLMapEntry(mapping *yqlib.CandidateNode, key string, value *yqlib.CandidateNode) error {
	_, err := evaluateYAMLContext(yqlib.Context{MatchingNodes: mapping.AsList()}, yqSetExactMapEntry,
		map[string]*yqlib.CandidateNode{
			"sitectl_key":   newYAMLStringCandidate(key, 0),
			"sitectl_value": value,
		})
	return err
}

func (d *YAMLDocument) setYAMLValue(keys []string, value any) error {
	candidate, err := yamlCandidateForValue(value)
	if err != nil {
		return err
	}
	return d.assignYAMLKeys(keys, candidate)
}

func (d *YAMLDocument) setYAMLString(keys []string, value string) error {
	return d.assignYAMLKeys(keys, newYAMLStringCandidate(value, yqlib.DoubleQuotedStyle))
}

func (d *YAMLDocument) appendUniqueYAMLString(keys []string, value string) error {
	path := yamlPath(keys...)
	if yamlCandidateAtKeys(d.node, keys...) == nil {
		if err := d.assignYAMLKeys(keys, &yqlib.CandidateNode{
			Kind: yqlib.SequenceNode, Tag: "!!seq", Content: []*yqlib.CandidateNode{},
		}); err != nil {
			return err
		}
	}
	return d.AppendUniqueString(path, value)
}

const (
	yqSetExactMapEntry = `. = (with_entries(
  (select((.key | to_string | @base64) == ($sitectl_key | @base64)) | .value = $sitectl_value) // .
) * {$sitectl_key: $sitectl_value})`
)

func (d *YAMLDocument) evaluate(expression string, autoCreate bool) (yqlib.Context, error) {
	return d.evaluateWithCandidates(expression, autoCreate, nil)
}

func (d *YAMLDocument) evaluateWithValues(expression string, autoCreate bool, variables map[string]any) (yqlib.Context, error) {
	candidates := make(map[string]*yqlib.CandidateNode, len(variables))
	for name, value := range variables {
		candidate, err := yamlCandidateForValue(value)
		if err != nil {
			return yqlib.Context{}, fmt.Errorf("bind yq variable %q: %w", name, err)
		}
		candidates[name] = candidate
	}
	return d.evaluateWithCandidates(expression, autoCreate, candidates)
}

func (d *YAMLDocument) evaluateWithCandidates(expression string, autoCreate bool, variables map[string]*yqlib.CandidateNode) (yqlib.Context, error) {
	if d == nil || d.node == nil {
		return yqlib.Context{}, fmt.Errorf("yaml document is not initialized")
	}
	return evaluateYAMLContext(yqlib.Context{
		MatchingNodes: d.node.AsList(), DontAutoCreate: !autoCreate,
	}, expression, variables)
}

func evaluateYAMLContext(context yqlib.Context, expression string, variables map[string]*yqlib.CandidateNode) (yqlib.Context, error) {
	expressionNode, err := parseYAMLExpression(expression)
	if err != nil {
		return yqlib.Context{}, err
	}
	for name, value := range variables {
		if value != nil {
			context.SetVariable(name, value.AsList())
		}
	}
	return yqlib.NewDataTreeNavigator().GetMatchingNodes(context, expressionNode)
}

// matchingExistingNodes works around yqlib v4.53.3 padding arrays during
// read-only indexed traversal. yq evaluates the path on a detached round trip,
// filters out paths that did not exist beforehand, then selects the live nodes.
func (d *YAMLDocument) matchingExistingNodes(path string) (yqlib.Context, error) {
	if d == nil || d.node == nil {
		return yqlib.Context{}, fmt.Errorf("yaml document is not initialized")
	}
	const expression = `
[.. | (path | to_json)] as $existing |
[(to_yaml | from_yaml) | eval($sitectl_path) | (path | to_json) |
  select(. as $p | $existing | any_c((. | @base64) == ($p | @base64)))] as $matches |
.. | select((path | to_json) as $p | $matches |
  any_c((. | @base64) == ($p | @base64)))`
	return d.evaluateWithCandidates(expression, false, map[string]*yqlib.CandidateNode{
		"sitectl_path": newYAMLStringCandidate(path, 0),
	})
}

func (d *YAMLDocument) existingYAMLNodes(path string) (yqlib.Context, error) {
	if d == nil || d.node == nil {
		return yqlib.Context{}, fmt.Errorf("yaml document is not initialized")
	}
	if keys, ok := literalYAMLPathKeys(path); ok {
		nodes := list.New()
		if target := yamlCandidateAtKeys(d.node, keys...); target != nil {
			nodes.PushBack(target)
		}
		return yqlib.Context{MatchingNodes: nodes, DontAutoCreate: true}, nil
	}
	return d.matchingExistingNodes(path)
}

func parseYAMLExpression(expression string) (*yqlib.ExpressionNode, error) {
	yqlibExpressionParserOnce.Do(func() {
		yqlib.ConfiguredYamlPreferences.FixMergeAnchorToSpec = true
		yqlib.StringInterpolationEnabled = false
		yqlib.InitExpressionParser()
	})
	return yqlib.ExpressionParser.ParseExpression(expression)
}

func validateYAMLPath(path string) error {
	if !strings.HasPrefix(path, ".") {
		return fmt.Errorf("invalid yaml path %q", path)
	}
	expression, err := parseYAMLExpression(path)
	if err != nil {
		return fmt.Errorf("invalid yaml path %q: %w", path, err)
	}
	if !isYAMLPathExpression(expression) {
		return fmt.Errorf("invalid yaml path %q: only traversal expressions are allowed", path)
	}
	return nil
}

func isYAMLPathExpression(expression *yqlib.ExpressionNode) bool {
	if expression == nil {
		return false
	}
	switch expression.Operation.OperationType.Type {
	case "PIPE", "SHORT_PIPE":
		return (expression.LHS == nil || isYAMLPathExpression(expression.LHS)) &&
			(expression.RHS == nil || isYAMLPathExpression(expression.RHS))
	case "TRAVERSE_PATH", "SELF":
		return expression.LHS == nil && expression.RHS == nil
	case "TRAVERSE_ARRAY":
		return isYAMLPathExpression(expression.LHS) && isSafeYAMLPathOperand(expression.RHS)
	case "SELECT":
		return expression.LHS == nil && isSafeYAMLPathOperand(expression.RHS)
	default:
		return false
	}
}

func isSafeYAMLPathOperand(expression *yqlib.ExpressionNode) bool {
	if expression == nil {
		return true
	}
	switch expression.Operation.OperationType.Type {
	case "PIPE", "SHORT_PIPE", "TRAVERSE_PATH", "TRAVERSE_ARRAY", "COLLECT", "UNION", "VALUE", "SELF", "STRING_INT", "EMPTY",
		"SELECT", "GET_KIND", "EQUALS", "GET_KEY", "TO_STRING", "ENCODE":
		return isSafeYAMLPathOperand(expression.LHS) && isSafeYAMLPathOperand(expression.RHS)
	default:
		return false
	}
}

func validateNonRootYAMLPath(path, operation, rootAction string) error {
	if isRootYAMLPath(path) {
		return fmt.Errorf("%s path %q: root %s is not supported", operation, path, rootAction)
	}
	return validateYAMLPath(path)
}

func isRootYAMLPath(path string) bool { return path == "" || path == "." }

// literalYAMLPathKeys recognizes the dotted mapping paths supported before
// yq expressions were added. Keeping those on exact map entries prevents a
// write from following a YAML merge alias into a shared anchor.
func literalYAMLPathKeys(path string) ([]string, bool) {
	trimmed := strings.TrimPrefix(path, ".")
	if trimmed == "" || strings.ContainsAny(trimmed, "[]*?|()=,\"' \t\r\n") {
		return nil, false
	}
	keys := strings.Split(trimmed, ".")
	for _, key := range keys {
		if key == "" {
			return nil, false
		}
	}
	return keys, true
}

// yamlPath builds an exact, non-creating yq path from mapping keys.
func yamlPath(keys ...string) string {
	var path strings.Builder
	path.WriteByte('.')
	for _, key := range keys {
		path.WriteString(` | select(kind == "map") | .[] | select((key | to_string | @base64) == (`)
		path.WriteString(yamlString(key))
		path.WriteString(` | @base64))`)
	}
	return path.String()
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func yamlCandidateForValue(value any) (*yqlib.CandidateNode, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal value: %w", err)
	}
	doc, err := LoadYAMLDocument(data)
	if err != nil {
		return nil, fmt.Errorf("unmarshal value node: %w", err)
	}
	doc.node.LeadingContent = ""
	return doc.node, nil
}

func yamlPreferences() yqlib.YamlPreferences {
	prefs := yqlib.NewDefaultYamlPreferences()
	prefs.Indent = 2
	prefs.ColorsEnabled = false
	return prefs
}

func newYAMLMappingCandidate() *yqlib.CandidateNode {
	return &yqlib.CandidateNode{Kind: yqlib.MappingNode, Tag: "!!map", Content: []*yqlib.CandidateNode{}}
}

func newYAMLStringCandidate(value string, style yqlib.Style) *yqlib.CandidateNode {
	return &yqlib.CandidateNode{Kind: yqlib.ScalarNode, Tag: "!!str", Value: value, Style: style}
}

func yamlMappingValue(mapping *yqlib.CandidateNode, key string) *yqlib.CandidateNode {
	if mapping == nil || mapping.Kind != yqlib.MappingNode {
		return nil
	}
	content := mapping.FilterMapContentByKey(func(candidate *yqlib.CandidateNode) bool {
		return candidate.Value == key
	})
	if len(content) == 2 {
		return content[1]
	}
	return nil
}

func yamlCandidateAtKeys(root *yqlib.CandidateNode, keys ...string) *yqlib.CandidateNode {
	candidate := root
	for _, key := range keys {
		candidate = yamlMappingValue(candidate, key)
		if candidate == nil {
			return nil
		}
	}
	return candidate
}

func (d *YAMLDocument) deleteYAMLKeys(keys ...string) error {
	candidate := yamlCandidateAtKeys(d.node, keys...)
	if candidate == nil {
		return nil
	}
	return deleteYAMLCandidates(candidate)
}

func isBlockScalar(node *yqlib.CandidateNode) bool {
	return node.Style == yqlib.FoldedStyle || node.Style == yqlib.LiteralStyle
}

func deleteYAMLCandidates(candidates ...*yqlib.CandidateNode) error {
	matchingNodes := list.New()
	for _, candidate := range candidates {
		if candidate != nil {
			matchingNodes.PushBack(candidate)
		}
	}
	if matchingNodes.Len() == 0 {
		return nil
	}
	_, err := evaluateYAMLContext(yqlib.Context{
		MatchingNodes: matchingNodes, DontAutoCreate: true,
	}, `del(.)`, nil)
	return err
}

func stripImplicitMergeTags(node *yqlib.CandidateNode) {
	if node == nil {
		return
	}
	if node.Kind == yqlib.ScalarNode && node.Value == "<<" && node.Tag == "!!merge" && node.Style != yqlib.TaggedStyle {
		node.Tag = ""
	}
	for _, child := range node.Content {
		stripImplicitMergeTags(child)
	}
}
