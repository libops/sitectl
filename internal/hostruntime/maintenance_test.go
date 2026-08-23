package hostruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPruneDockerPreservesNamedRollbackImages(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "docker.log")
	writeExecutable(t, filepath.Join(root, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$DOCKER_LOG\"\n")
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_LOG", logPath)
	if err := PruneDocker(context.Background(), "72h", filepath.Join(root, "prune.lock"), nil, nil); err != nil {
		t.Fatalf("PruneDocker() error = %v", err)
	}
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"container prune --force --filter until=72h",
		"network prune --force --filter until=72h",
		"image prune --force --filter until=72h",
		"builder prune --force --filter until=72h",
	}, "\n") + "\n"
	if string(contents) != want {
		t.Fatalf("Docker calls = %q, want %q", contents, want)
	}
}
