package component

import (
	"strings"
	"testing"
)

func TestYAMLDocumentPreservesComposeAnchorsAndComments(t *testing.T) {
	t.Parallel()

	input := `---
# Common to all services
x-common: &common
  restart: unless-stopped
  tty: true # keep tty comment
services:
  alpaca:
    <<: *common
    environment:
      ALPACA_FCREPO_INDEXER_ENABLED: "true"
  fcrepo:
    <<: *common
    image: islandora/fcrepo6
volumes:
  fcrepo-data: {}
`

	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.DeletePath(".services.fcrepo"); err != nil {
		t.Fatalf("DeletePath() error = %v", err)
	}
	if err := doc.SetString(".services.alpaca.environment.ALPACA_FCREPO_INDEXER_ENABLED", "false"); err != nil {
		t.Fatalf("SetString() error = %v", err)
	}
	if err := doc.DeletePath(".volumes.fcrepo-data"); err != nil {
		t.Fatalf("DeletePath(volume) error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)

	if !strings.HasPrefix(rendered, "---\n") {
		t.Fatalf("expected explicit document start preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "x-common: &common") {
		t.Fatalf("expected anchor preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<<: *common") {
		t.Fatalf("expected merge key preserved, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "# keep tty comment") {
		t.Fatalf("expected comment preserved, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "services:\n  fcrepo:") || strings.Contains(rendered, "\n  fcrepo:\n") {
		t.Fatalf("expected fcrepo removed, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "fcrepo-data") {
		t.Fatalf("expected fcrepo-data removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, `ALPACA_FCREPO_INDEXER_ENABLED: "false"`) {
		t.Fatalf("expected updated env string, got:\n%s", rendered)
	}
}

func TestYAMLDocumentSetStringPreservesExistingOrder(t *testing.T) {
	t.Parallel()

	input := "settings:\n  target_type: file\n  uri_scheme: fedora\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.SetString(".settings.uri_scheme", "private"); err != nil {
		t.Fatalf("SetString() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "target_type: file\n  uri_scheme: \"private\"") {
		t.Fatalf("expected key order preserved, got:\n%s", rendered)
	}
}

func TestYAMLDocumentSetStringPreservesScalarStyle(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte("single: 'old' # keep comment\nliteral: |-\n  old value\nplain: old\n"))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	for path, value := range map[string]string{
		".single":  "new",
		".literal": "new value",
		".plain":   "new",
	} {
		if err := doc.SetString(path, value); err != nil {
			t.Fatalf("SetString(%q) error = %v", path, err)
		}
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	for _, expected := range []string{
		"single: 'new' # keep comment",
		"literal: |-\n  new value",
		`plain: "new"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected styled value %q preserved, got:\n%s", expected, rendered)
		}
	}
}

func TestYAMLDocumentAppendUniqueStringCreatesAndDeduplicatesSequence(t *testing.T) {
	t.Parallel()

	input := "services:\n  traefik:\n    image: traefik\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.AppendUniqueString(".services.traefik.command", "--entrypoints.web.address=:80"); err != nil {
		t.Fatalf("AppendUniqueString() error = %v", err)
	}
	if err := doc.AppendUniqueString(".services.traefik.command", "--entrypoints.web.address=:80"); err != nil {
		t.Fatalf("AppendUniqueString(duplicate) error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.Count(rendered, "--entrypoints.web.address=:80") != 1 {
		t.Fatalf("expected command value once, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "command:\n      - --entrypoints.web.address=:80") {
		t.Fatalf("expected command sequence, got:\n%s", rendered)
	}
}

func TestYAMLDocumentAppendUniqueStringConvertsScalar(t *testing.T) {
	t.Parallel()

	input := "services:\n  traefik:\n    command: --api.dashboard=true\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.AppendUniqueString(".services.traefik.command", "--entrypoints.web.address=:80"); err != nil {
		t.Fatalf("AppendUniqueString() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	for _, want := range []string{
		"      - --api.dashboard=true",
		"      - --entrypoints.web.address=:80",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected converted sequence to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestYAMLDocumentAppendUniqueStringPreservesFoldedScalar(t *testing.T) {
	t.Parallel()

	input := `services:
  traefik:
    command: >-
      --ping=true
      --log.level=INFO
`
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	value := "--experimental.localPlugins.captcha-protect.modulename=github.com/libops/captcha-protect"
	if err := doc.AppendUniqueString(".services.traefik.command", value); err != nil {
		t.Fatalf("AppendUniqueString() error = %v", err)
	}
	if err := doc.AppendUniqueString(".services.traefik.command", value); err != nil {
		t.Fatalf("AppendUniqueString(duplicate) error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "command: >-") {
		t.Fatalf("expected folded command scalar to remain folded, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "command:\n") {
		t.Fatalf("expected folded command scalar not to become a sequence, got:\n%s", rendered)
	}
	if strings.Count(rendered, value) != 1 {
		t.Fatalf("expected command value once, got:\n%s", rendered)
	}

	if err := doc.RemoveString(".services.traefik.command", value); err != nil {
		t.Fatalf("RemoveString() error = %v", err)
	}
	out, err = doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(after remove) error = %v", err)
	}
	rendered = string(out)
	if strings.Contains(rendered, value) {
		t.Fatalf("expected folded command scalar value removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "--ping=true") || !strings.Contains(rendered, "--log.level=INFO") {
		t.Fatalf("expected original folded command values to remain, got:\n%s", rendered)
	}
}

func TestYAMLDocumentRemoveStringRemovesEmptySequence(t *testing.T) {
	t.Parallel()

	input := "services:\n  traefik:\n    volumes:\n      - ./certs:/certs:ro\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.RemoveString(".services.traefik.volumes", "./certs:/certs:ro"); err != nil {
		t.Fatalf("RemoveString() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "volumes:") || strings.Contains(rendered, "./certs:/certs:ro") {
		t.Fatalf("expected empty volumes sequence removed, got:\n%s", rendered)
	}
}

func TestYAMLDocumentDoesNotAddExplicitMergeTagWhenOriginalDidNotUseIt(t *testing.T) {
	t.Parallel()

	input := "x-common: &common\n  restart: unless-stopped\nservices:\n  alpaca:\n    <<: *common\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.HasPrefix(rendered, "---\n") {
		t.Fatalf("expected implicit document start to remain implicit, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "!!merge <<:") {
		t.Fatalf("expected implicit merge key to stay untagged, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "<<: *common") {
		t.Fatalf("expected merge key preserved, got:\n%s", rendered)
	}
}

func TestYAMLDocumentPreservesExplicitMergeTagWhenOriginalUsedIt(t *testing.T) {
	t.Parallel()

	input := "x-common: &common\n  restart: unless-stopped\nservices:\n  alpaca:\n    !!merge <<: *common\n"
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "!!merge <<: *common") {
		t.Fatalf("expected explicit merge tag preserved, got:\n%s", rendered)
	}
}

func TestYAMLDocumentSupportsArrayIndexPaths(t *testing.T) {
	t.Parallel()

	input := `items:
  - name: first
    obsolete: true
  - name: second
`
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.SetString(".items[1].name", "updated"); err != nil {
		t.Fatalf("SetString(array index) error = %v", err)
	}
	if err := doc.DeletePath(".items[0].obsolete"); err != nil {
		t.Fatalf("DeletePath(array index) error = %v", err)
	}
	hasUpdatedName, err := doc.HasPath(".items[1].name")
	if err != nil {
		t.Fatalf("HasPath(array index) error = %v", err)
	}
	if !hasUpdatedName {
		t.Fatal("expected indexed name path to exist")
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, `name: "updated"`) {
		t.Fatalf("expected indexed item to be updated, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "obsolete:") {
		t.Fatalf("expected indexed field to be deleted, got:\n%s", rendered)
	}
}

func TestYAMLDocumentOutOfRangeIndexReadsAndDeletesDoNotMutate(t *testing.T) {
	t.Parallel()

	input := `items: &items
  - name: first
alias: *items
`
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	before, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(before) error = %v", err)
	}

	for _, path := range []string{".items[4]", ".alias[4]"} {
		hasPath, hasErr := doc.HasPath(path)
		if hasErr != nil {
			t.Fatalf("HasPath(%q) error = %v", path, hasErr)
		}
		if hasPath {
			t.Fatalf("HasPath(%q) = true, want false", path)
		}
	}
	if err := doc.DeletePath(".items[4].obsolete"); err != nil {
		t.Fatalf("DeletePath(out-of-range index) error = %v", err)
	}

	after, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(after) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("read/delete padded an out-of-range sequence:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestYAMLDocumentMutatesCustomTaggedSequence(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte("values: !sitectl [one]\n"))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.AppendUniqueString(".values", "two"); err != nil {
		t.Fatalf("AppendUniqueString() error = %v", err)
	}
	if err := doc.AppendUniqueString(".values", "two"); err != nil {
		t.Fatalf("AppendUniqueString(duplicate) error = %v", err)
	}
	if err := doc.RemoveString(".values", "one"); err != nil {
		t.Fatalf("RemoveString() error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if !strings.Contains(rendered, "!sitectl") || strings.Contains(rendered, "one") || strings.Count(rendered, "two") != 1 {
		t.Fatalf("expected tagged sequence containing only two, got:\n%s", rendered)
	}
}

func TestYAMLDocumentTreatsGlobCharactersAsLiteralSequenceValues(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte("values: [keep]\n"))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.AppendUniqueString(".values", "*"); err != nil {
		t.Fatalf("AppendUniqueString() error = %v", err)
	}
	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(after append) error = %v", err)
	}
	if !strings.Contains(string(out), "*") {
		t.Fatalf("expected literal wildcard value appended, got:\n%s", out)
	}

	if err := doc.RemoveString(".values", "*"); err != nil {
		t.Fatalf("RemoveString() error = %v", err)
	}
	out, err = doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(after remove) error = %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, "*") || !strings.Contains(rendered, "keep") {
		t.Fatalf("expected only the literal wildcard value removed, got:\n%s", rendered)
	}
}

func TestYAMLDocumentRejectsAssignmentsThroughScalarIntermediates(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte("settings: scalar\n"))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	before, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(before) error = %v", err)
	}

	operations := []struct {
		name string
		run  func() error
	}{
		{name: "set string", run: func() error { return doc.SetString(".settings.child", "value") }},
		{name: "set value", run: func() error { return doc.SetValue(".settings.child", true) }},
		{name: "append", run: func() error { return doc.AppendUniqueString(".settings.child", "value") }},
	}
	for _, operation := range operations {
		if err := operation.run(); err == nil {
			t.Fatalf("%s through scalar intermediate succeeded", operation.name)
		}
	}

	after, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(after) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed assignments mutated the document:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestYAMLDocumentSetStringSupportsValueSelectorPaths(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte("items: [old, keep]\n"))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.SetString(`.items[] | select(. == "old")`, "new"); err != nil {
		t.Fatalf("SetString(selector) error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	if rendered := string(out); !strings.Contains(rendered, `["new", keep]`) {
		t.Fatalf("expected selector assignment to update the matching value, got:\n%s", rendered)
	}
}

func TestYAMLDocumentBytesDoesNotChangeSubsequentMutationSemantics(t *testing.T) {
	t.Parallel()

	input := []byte("x-common: &common\n  restart: unless-stopped\nservices:\n  alpaca:\n    <<: *common\n")
	mutate := func(callBytesFirst bool) string {
		t.Helper()
		doc, err := LoadYAMLDocument(input)
		if err != nil {
			t.Fatalf("LoadYAMLDocument() error = %v", err)
		}
		if callBytesFirst {
			if _, err := doc.Bytes(); err != nil {
				t.Fatalf("Bytes(before mutation) error = %v", err)
			}
		}
		if err := doc.SetString(".services.alpaca.restart", "always"); err != nil {
			t.Fatalf("SetString() error = %v", err)
		}
		out, err := doc.Bytes()
		if err != nil {
			t.Fatalf("Bytes(after mutation) error = %v", err)
		}
		return string(out)
	}

	withoutPriorEncode := mutate(false)
	withPriorEncode := mutate(true)
	if withPriorEncode != withoutPriorEncode {
		t.Fatalf("Bytes changed later mutation behavior:\nwithout prior encode:\n%s\nwith prior encode:\n%s", withoutPriorEncode, withPriorEncode)
	}
}

func TestYAMLDocumentLiteralPathsDoNotMutateMergedAnchors(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte(`x-common: &common
  restart: unless-stopped
services:
  alpaca:
    <<: *common
  fcrepo:
    <<: *common
`))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	hasInherited, err := doc.HasPath(".services.alpaca.restart")
	if err != nil {
		t.Fatalf("HasPath(inherited key) error = %v", err)
	}
	if hasInherited {
		t.Fatal("expected an inherited-only key to remain absent from the literal mapping path")
	}
	if err := doc.DeletePath(".services.fcrepo.restart"); err != nil {
		t.Fatalf("DeletePath(inherited key) error = %v", err)
	}
	if err := doc.SetString(".services.alpaca.restart", "always"); err != nil {
		t.Fatalf("SetString(inherited key) error = %v", err)
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if strings.Count(rendered, "restart: unless-stopped") != 1 {
		t.Fatalf("expected shared anchor to stay unchanged, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "alpaca:\n    <<: *common\n    restart: \"always\"") {
		t.Fatalf("expected a service-local override, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "fcrepo:\n    <<: *common") {
		t.Fatalf("expected inherited-only delete to preserve the merge, got:\n%s", rendered)
	}
}

func TestYAMLDocumentSupportsWildcardPaths(t *testing.T) {
	t.Parallel()

	input := `services:
  alpaca:
    obsolete: true
  fcrepo:
    obsolete: true
`
	doc, err := LoadYAMLDocument([]byte(input))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	if err := doc.SetString(".services.*.restart", "unless-stopped"); err != nil {
		t.Fatalf("SetString(wildcard) error = %v", err)
	}
	if err := doc.AppendUniqueString(".services.*.command", "--ping=true"); err != nil {
		t.Fatalf("AppendUniqueString(wildcard) error = %v", err)
	}
	if err := doc.DeletePath(".services.*.obsolete"); err != nil {
		t.Fatalf("DeletePath(wildcard) error = %v", err)
	}
	hasRestart, err := doc.HasPath(".services.*.restart")
	if err != nil {
		t.Fatalf("HasPath(wildcard) error = %v", err)
	}
	if !hasRestart {
		t.Fatal("expected wildcard restart path to match")
	}

	out, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes() error = %v", err)
	}
	rendered := string(out)
	if got := strings.Count(rendered, `restart: "unless-stopped"`); got != 2 {
		t.Fatalf("expected wildcard assignment to update two services, got %d:\n%s", got, rendered)
	}
	if got := strings.Count(rendered, "- --ping=true"); got != 2 {
		t.Fatalf("expected wildcard append to update two services, got %d:\n%s", got, rendered)
	}
	if strings.Contains(rendered, "obsolete:") {
		t.Fatalf("expected wildcard deletion to remove both fields, got:\n%s", rendered)
	}
}

func TestYAMLDocumentRejectsNonTraversalPathExpressions(t *testing.T) {
	t.Parallel()

	doc, err := LoadYAMLDocument([]byte("safe: value\n"))
	if err != nil {
		t.Fatalf("LoadYAMLDocument() error = %v", err)
	}
	before, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(before) error = %v", err)
	}
	for _, path := range []string{
		`.safe | load("/etc/passwd")`,
		`.safe | env(HOME)`,
		`.safe[0:1]`,
		`.safe | "synthetic"`,
		`.safe | ["synthetic"]`,
	} {
		if _, err := doc.HasPath(path); err == nil {
			t.Fatalf("HasPath(%q) expected traversal validation error", path)
		}
		if err := doc.DeletePath(path); err == nil {
			t.Fatalf("DeletePath(%q) expected traversal validation error", path)
		}
		if err := doc.SetString(path, "changed"); err == nil {
			t.Fatalf("SetString(%q) expected traversal validation error", path)
		}
	}
	after, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(after) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("rejected paths mutated the document:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
