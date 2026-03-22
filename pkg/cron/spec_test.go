package cron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestPruneArtifactsWithSynctest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()

		for day := 0; day < 31; day++ {
			now := time.Now().UTC()
			name := now.Format("20060102.15.04.05") + ".gz"
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if err := os.Chtimes(path, now, now); err != nil {
				t.Fatalf("Chtimes() error = %v", err)
			}
			time.Sleep(24 * time.Hour)
			synctest.Wait()
		}

		if err := PruneArtifacts(root, time.Now(), 14, true); err != nil {
			t.Fatalf("PruneArtifacts() error = %v", err)
		}

		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("ReadDir() error = %v", err)
		}
		if got, want := len(entries), 15; got != want {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("len(entries) = %d, want %d; remaining=%v", got, want, names)
		}

		var foundMonthly bool
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "20000101.") {
				foundMonthly = true
				break
			}
		}
		if !foundMonthly {
			t.Fatal("expected preserved monthly artifact from 2000-01-01")
		}
	})
}
