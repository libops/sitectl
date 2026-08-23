package hostruntime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func SetRuntimeEnv(path, name, value string) error {
	if !envNamePattern.MatchString(name) {
		return fmt.Errorf("invalid environment name %q", name)
	}
	lines, err := readSafeLines(path, true)
	if err != nil {
		return err
	}
	output := make([]string, 0, len(lines)+1)
	prefix := name + "="
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			output = append(output, line)
		}
	}
	output = append(output, name+"="+quoteEnv(value))
	return writeAtomic(path, []byte(strings.Join(output, "\n")+"\n"), 0o640)
}

func SetComposeEnv(path, name, value, markerPrefix string) error {
	if !envNamePattern.MatchString(name) {
		return fmt.Errorf("invalid environment name %q", name)
	}
	lines, err := readSafeLines(path, true)
	if err != nil {
		return err
	}
	marker := markerPrefix + name
	output := make([]string, 0, len(lines)+2)
	for index := 0; index < len(lines); index++ {
		if lines[index] == marker {
			index++
			if index >= len(lines) || !strings.HasPrefix(lines[index], name+"=") {
				return fmt.Errorf("incomplete managed assignment for %s", name)
			}
			continue
		}
		output = append(output, lines[index])
	}
	output = append(output, marker, name+"="+quoteEnv(value))
	return writeAtomic(path, []byte(strings.Join(output, "\n")+"\n"), 0o640)
}

func SyncComposeEnv(path, jsonPath string) error {
	info, err := os.Lstat(jsonPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("application environment file is missing or unsafe")
	}
	contents, err := os.ReadFile(jsonPath)
	if err != nil {
		return err
	}
	values := map[string]string{}
	if err := json.Unmarshal(contents, &values); err != nil {
		return fmt.Errorf("decode application environment: %w", err)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("invalid application environment name %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	lines, err := readSafeLines(path, true)
	if err != nil {
		return err
	}
	output := make([]string, 0, len(lines)+len(names)*2)
	for index := 0; index < len(lines); index++ {
		if strings.HasPrefix(lines[index], "# cloud-compose application: ") {
			index++
			if index >= len(lines) {
				return fmt.Errorf("incomplete managed application assignment")
			}
			continue
		}
		output = append(output, lines[index])
	}
	for _, name := range names {
		output = append(output, "# cloud-compose application: "+name, name+"="+quoteEnv(values[name]))
	}
	return writeAtomic(path, []byte(strings.Join(output, "\n")+"\n"), 0o640)
}

func readSafeLines(path string, missingOK bool) ([]string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && missingOK {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe environment path %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

func quoteEnv(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "$$", "\n", "\\n", "\r", "\\r", "\t", "\\t")
	return "\"" + replacer.Replace(value) + "\""
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".sitectl-host-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
