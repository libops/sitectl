package job

import (
	"testing"
)

func TestConfirmDatabaseReplacement_YoloSkipsPrompt(t *testing.T) {
	ok, err := ConfirmDatabaseReplacement("ctx", "drupal", "/tmp/dump.sql.gz", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected yolo=true to return ok=true without prompting")
	}
}
