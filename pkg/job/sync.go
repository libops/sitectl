package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libops/sitectl/pkg/config"
)

func ResolveContextPair(sourceName, targetName string) (*config.Context, *config.Context, error) {
	if strings.TrimSpace(sourceName) == "" {
		return nil, nil, fmt.Errorf("--source is required")
	}
	if strings.TrimSpace(targetName) == "" {
		return nil, nil, fmt.Errorf("--target is required")
	}
	if sourceName == targetName {
		return nil, nil, fmt.Errorf("--source and --target must be different contexts")
	}

	sourceCtx, err := config.GetContext(sourceName)
	if err != nil {
		return nil, nil, fmt.Errorf("load source context %q: %w", sourceName, err)
	}
	targetCtx, err := config.GetContext(targetName)
	if err != nil {
		return nil, nil, fmt.Errorf("load target context %q: %w", targetName, err)
	}

	return &sourceCtx, &targetCtx, nil
}

func SyncArtifactName(prefix, suffix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "sitectl-sync"
	}
	return fmt.Sprintf("%s-%d-%s", prefix, time.Now().UnixNano(), suffix)
}

func StageArtifactBetweenContexts(runCtx context.Context, sourceCtx, targetCtx *config.Context, sourcePath, localWorkDir, targetSuffix, prefix string) (string, func(), error) {
	if sourceCtx == nil {
		return "", nil, fmt.Errorf("source context is required")
	}
	if targetCtx == nil {
		return "", nil, fmt.Errorf("target context is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return "", nil, fmt.Errorf("source path is required")
	}
	if strings.TrimSpace(localWorkDir) == "" {
		return "", nil, fmt.Errorf("local work dir is required")
	}

	localArtifactPath := filepath.Join(localWorkDir, filepath.Base(sourcePath))
	if err := DownloadContextFile(sourceCtx, sourcePath, localArtifactPath); err != nil {
		return "", nil, err
	}

	targetHostPath := filepath.ToSlash(filepath.Join("/tmp", SyncArtifactName(prefix, targetSuffix)))
	if err := targetCtx.UploadFile(localArtifactPath, targetHostPath); err != nil {
		return "", nil, err
	}

	cleanup := func() {
		RemoveContextHostPath(runCtx, targetCtx, targetHostPath)
	}
	return targetHostPath, cleanup, nil
}

func MakeTempWorkDir(pattern string) (string, func(), error) {
	workDir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	return workDir, func() {
		_ = os.RemoveAll(workDir)
	}, nil
}
