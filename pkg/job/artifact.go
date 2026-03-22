package job

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/libops/sitectl/pkg/config"
)

var defaultRecentArtifactOffsets = []time.Duration{0, -24 * time.Hour}

func DatedArtifactPath(rootDir, filename string, ts time.Time) string {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return filepath.Join(rootDir, ts.Format("2006"), ts.Format("01"), ts.Format("02"), filename)
}

func ResolveRecentArtifact(ctx *config.Context, rootDir, filename string, fresh bool, now time.Time, produce func(path string) error) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is required")
	}
	if filename == "" {
		return "", fmt.Errorf("filename is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if !fresh {
		for _, offset := range defaultRecentArtifactOffsets {
			path := DatedArtifactPath(rootDir, filename, now.Add(offset))
			exists, err := PathExistsOnContext(ctx, path)
			if err != nil {
				return "", err
			}
			if exists {
				return path, nil
			}
		}
	}

	path := DatedArtifactPath(rootDir, filename, now)
	if err := produce(path); err != nil {
		return "", err
	}
	return path, nil
}

func PathExistsOnContext(ctx *config.Context, path string) (bool, error) {
	accessor, err := config.NewFileAccessor(ctx)
	if err != nil {
		return false, err
	}
	defer accessor.Close()
	return accessor.FileExists(path)
}

func EnsurePathAbsentOnContext(ctx *config.Context, path string) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	exists, err := PathExistsOnContext(ctx, path)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("refusing to overwrite existing host file %q on context %q", path, ctx.Name)
	}
	return nil
}
