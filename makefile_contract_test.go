package main

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultMakeTargetsDoNotRewriteDependencies(t *testing.T) {
	contents, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(contents)
	for _, required := range []string{
		"deps:\n\tgo mod download\n",
		"deps-update:\n\tgo get -u ./...\n\tgo mod tidy\n",
		"test: deps\n\tgo test -v -race ./...\n",
		"work:\n\tbash ./scripts/use-go-work.sh\n",
		"mod-check:\n\tgo mod tidy -diff\n",
	} {
		if !strings.Contains(makefile, required) {
			t.Errorf("Makefile is missing non-mutating workflow contract %q", required)
		}
	}
	if strings.Contains(makefile, "test: build") {
		t.Error("test must not inherit binary or plugin build side effects")
	}
}
