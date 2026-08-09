package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

const composeCreateDirectoryReadBatch = 256

// readLocalComposeCreateDirectory bounds directory metadata retained in memory.
// It reads one entry beyond maximum only to report that the limit was exceeded.
func readLocalComposeCreateDirectory(directory string, maximum int) (entries []os.FileInfo, exceeded bool, returnErr error) {
	if maximum < 0 {
		return nil, false, fmt.Errorf("directory entry limit cannot be negative")
	}
	directoryHandle, err := os.Open(directory) // #nosec G304 -- directory is a caller-selected project path or a validated child of one.
	if err != nil {
		return nil, false, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directoryHandle.Close())
	}()

	capacity := maximum
	if capacity > composeCreateDirectoryReadBatch {
		capacity = composeCreateDirectoryReadBatch
	}
	entries = make([]os.FileInfo, 0, capacity)
	for {
		remaining := maximum + 1 - len(entries)
		if remaining > composeCreateDirectoryReadBatch {
			remaining = composeCreateDirectoryReadBatch
		}
		batch, readErr := directoryHandle.Readdir(remaining)
		entries = append(entries, batch...)
		if len(entries) > maximum {
			return entries[:maximum], true, nil
		}
		if errors.Is(readErr, io.EOF) {
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			return entries, false, nil
		}
		if readErr != nil {
			return nil, false, readErr
		}
	}
}

func localComposeCreateDirectoryHasEntries(directory string) (bool, error) {
	entries, exceeded, err := readLocalComposeCreateDirectory(directory, 1)
	if err != nil {
		return false, err
	}
	return len(entries) > 0 || exceeded, nil
}

func readRemoteComposeCreateDirectory(runCtx context.Context, connection remoteTemplateConnection, directory string, maximum int) ([]os.FileInfo, bool, error) {
	if maximum < 0 {
		return nil, false, fmt.Errorf("directory entry limit cannot be negative")
	}
	entries, exceeded, err := connection.ReadDirLimit(runCtx, directory, maximum)
	if err != nil {
		return nil, false, remoteTemplateContextError(runCtx, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, exceeded, nil
}
