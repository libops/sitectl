package cmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/libops/sitectl/pkg/docker"
	"github.com/spf13/cobra"
)

func TestMariaDBUpgradeCommandUsesDefaultServiceFlag(t *testing.T) {
	t.Parallel()

	cmd := mariaDBUpgradeCommand()
	flag := cmd.Flags().Lookup("service")
	if flag == nil {
		t.Fatal("expected --service flag")
	}
	if flag.DefValue != defaultMariaDBService {
		t.Fatalf("--service default = %q, want %q", flag.DefValue, defaultMariaDBService)
	}
}

func TestRunMariaDBUpgradeSkipsUpgradeWhenProbeReportsNoAction(t *testing.T) {
	t.Parallel()

	var commands [][]string
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := mariaDBUpgradeTestCommand(stdout, stderr)
	execute := func(_ context.Context, _ *docker.DockerClient, container string, command []string) (mariaDBUpgradeResult, error) {
		if container != "database-1" {
			t.Fatalf("container = %q, want database-1", container)
		}
		commands = append(commands, append([]string{}, command...))
		switch len(commands) {
		case 1:
			return mariaDBUpgradeResult{stdout: "  --check-if-upgrade-is-needed"}, nil
		case 2:
			return mariaDBUpgradeResult{exitCode: 1, stderr: "already current"}, nil
		default:
			t.Fatalf("unexpected command %v", command)
			return mariaDBUpgradeResult{}, nil
		}
	}

	err := runMariaDBUpgradeWithExecutor(cmd, &serviceContainer{containerName: "database-1"}, "mariadb-upgrade", execute)
	if err != nil {
		t.Fatalf("runMariaDBUpgradeWithExecutor() error = %v", err)
	}
	wantCommands := [][]string{
		{"mariadb-upgrade", "--help"},
		{"mariadb-upgrade", "--check-if-upgrade-is-needed"},
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if got := stdout.String(); !strings.Contains(got, "already up to date; no upgrade needed") {
		t.Fatalf("stdout = %q, want no-op status", got)
	}
	if got := stderr.String(); got != "already current\n" {
		t.Fatalf("stderr = %q, want preserved probe stderr", got)
	}
}

func TestRunMariaDBUpgradeRunsWhenProbeReportsUpgradeNeeded(t *testing.T) {
	t.Parallel()

	results := []mariaDBUpgradeResult{
		{stdout: "--check-if-upgrade-is-needed"},
		{exitCode: 0},
		{exitCode: 0, stdout: "tables upgraded", stderr: "upgrade warning"},
	}
	var commands [][]string
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd := mariaDBUpgradeTestCommand(stdout, stderr)
	execute := func(_ context.Context, _ *docker.DockerClient, _ string, command []string) (mariaDBUpgradeResult, error) {
		commands = append(commands, append([]string{}, command...))
		return results[len(commands)-1], nil
	}

	err := runMariaDBUpgradeWithExecutor(cmd, &serviceContainer{containerName: "database-1"}, "mariadb-upgrade", execute)
	if err != nil {
		t.Fatalf("runMariaDBUpgradeWithExecutor() error = %v", err)
	}
	wantCommands := [][]string{
		{"mariadb-upgrade", "--help"},
		{"mariadb-upgrade", "--check-if-upgrade-is-needed"},
		{"mariadb-upgrade"},
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if got := stdout.String(); got != "tables upgraded\nMariaDB upgrade complete.\n" {
		t.Fatalf("stdout = %q, want upgrade output and completion status", got)
	}
	if got := stderr.String(); got != "upgrade warning\n" {
		t.Fatalf("stderr = %q, want preserved upgrade stderr", got)
	}
}

func TestRunMariaDBUpgradeFallsBackForOlderClients(t *testing.T) {
	t.Parallel()

	var commands [][]string
	stdout := &bytes.Buffer{}
	cmd := mariaDBUpgradeTestCommand(stdout, io.Discard)
	execute := func(_ context.Context, _ *docker.DockerClient, _ string, command []string) (mariaDBUpgradeResult, error) {
		commands = append(commands, append([]string{}, command...))
		if len(commands) == 1 {
			return mariaDBUpgradeResult{exitCode: 0, stdout: "legacy client help"}, nil
		}
		return mariaDBUpgradeResult{exitCode: 0, stdout: "legacy upgrade complete"}, nil
	}

	err := runMariaDBUpgradeWithExecutor(cmd, &serviceContainer{containerName: "database-1"}, "mysql_upgrade", execute)
	if err != nil {
		t.Fatalf("runMariaDBUpgradeWithExecutor() error = %v", err)
	}
	wantCommands := [][]string{{"mysql_upgrade", "--help"}, {"mysql_upgrade"}}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", commands, wantCommands)
	}
	if got := stdout.String(); !strings.Contains(got, "legacy upgrade complete") {
		t.Fatalf("stdout = %q, want legacy upgrade output", got)
	}
}

func TestRunMariaDBUpgradePreservesProbeFailure(t *testing.T) {
	t.Parallel()

	cmd := mariaDBUpgradeTestCommand(io.Discard, io.Discard)
	call := 0
	execute := func(_ context.Context, _ *docker.DockerClient, _ string, _ []string) (mariaDBUpgradeResult, error) {
		call++
		if call == 1 {
			return mariaDBUpgradeResult{stdout: "--check-if-upgrade-is-needed"}, nil
		}
		return mariaDBUpgradeResult{exitCode: 2, stderr: "cannot connect to server"}, nil
	}

	err := runMariaDBUpgradeWithExecutor(cmd, &serviceContainer{containerName: "database-1"}, "mariadb-upgrade", execute)
	if err == nil {
		t.Fatal("expected probe failure")
	}
	if got := err.Error(); !strings.Contains(got, "exit code 2") || !strings.Contains(got, "cannot connect to server") {
		t.Fatalf("error = %q, want exit code and stderr", got)
	}
}

func TestRunMariaDBUpgradePreservesExecError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("docker connection lost")
	execute := func(_ context.Context, _ *docker.DockerClient, _ string, _ []string) (mariaDBUpgradeResult, error) {
		return mariaDBUpgradeResult{exitCode: -1, stderr: "socket closed"}, wantErr
	}

	err := runMariaDBUpgradeWithExecutor(
		mariaDBUpgradeTestCommand(io.Discard, io.Discard),
		&serviceContainer{containerName: "database-1"},
		"mariadb-upgrade",
		execute,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapping %v", err, wantErr)
	}
	if got := err.Error(); !strings.Contains(got, "socket closed") {
		t.Fatalf("error = %q, want preserved stderr", got)
	}
}

func mariaDBUpgradeTestCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "upgrade"}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetContext(context.Background())
	return cmd
}

func TestMariaDBDumpArgsDefaultsToAllDatabases(t *testing.T) {
	t.Parallel()

	got := mariaDBDumpArgs("mariadb-dump", mariaDBBackupOptions{})
	want := []string{"mariadb-dump", "--single-transaction", "--quick", "--routines", "--triggers", "--user=root", "--all-databases"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mariaDBDumpArgs() = %v, want %v", got, want)
	}
}

func TestMariaDBDumpArgsUsesDatabase(t *testing.T) {
	t.Parallel()

	got := mariaDBDumpArgs("mysqldump", mariaDBBackupOptions{database: "appdb"})
	want := []string{"mysqldump", "--single-transaction", "--quick", "--routines", "--triggers", "--user=root", "appdb"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mariaDBDumpArgs() = %v, want %v", got, want)
	}
}

func TestMariaDBBackupOptionsRejectAmbiguousDatabaseSelection(t *testing.T) {
	t.Parallel()

	if err := validateMariaDBBackupOptions(mariaDBBackupOptions{service: "mariadb", allDatabases: true, database: "appdb"}); err == nil {
		t.Fatal("expected --all-databases with database name to fail")
	}
	if err := validateMariaDBBackupOptions(mariaDBBackupOptions{service: "mariadb", database: "--events"}); err == nil {
		t.Fatal("expected option-like database name to fail")
	}
}

func TestValidateMariaDBDatabaseName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		database string
		wantErr  string
	}{
		{name: "empty"},
		{name: "simple", database: "appdb"},
		{name: "trimmed", database: " appdb "},
		{name: "backticks allowed for identifier escaping", database: "app`db"},
		{name: "leading dash", database: "-appdb", wantErr: "cannot start with '-'"},
		{name: "trimmed leading dash", database: " -appdb ", wantErr: "cannot start with '-'"},
		{name: "nul byte", database: "app\x00db", wantErr: "cannot contain NUL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateMariaDBDatabaseName(tc.database)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("validateMariaDBDatabaseName(%q) error = %v, want containing %q", tc.database, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateMariaDBDatabaseName(%q) error = %v", tc.database, err)
			}
		})
	}
}

func TestMariaDBIdentifierEscapesBackticks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  string
	}{
		{value: "appdb", want: "`appdb`"},
		{value: "app`db", want: "`app``db`"},
		{value: "-dash", want: "`-dash`"},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()

			if got := mariaDBIdentifier(tc.value); got != tc.want {
				t.Fatalf("mariaDBIdentifier(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestMaybeGzipReader(t *testing.T) {
	t.Parallel()

	payload := []byte("CREATE DATABASE appdb;\n")
	gzipped := gzipBytes(t, payload)

	tests := []struct {
		name string
		data []byte
	}{
		{name: "plain sql", data: payload},
		{name: "gzip sql", data: gzipped},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file := tempMariaDBInputFile(t, tc.data)
			reader, cleanup, err := maybeGzipReader(file)
			if err != nil {
				t.Fatalf("maybeGzipReader() error = %v", err)
			}
			t.Cleanup(func() {
				if err := cleanup(); err != nil {
					t.Fatalf("cleanup() error = %v", err)
				}
			})

			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("maybeGzipReader() read %q, want %q", got, payload)
			}
		})
	}
}

func TestMariaDBArtifactNameIncludesDatabase(t *testing.T) {
	t.Parallel()

	if got, want := mariaDBArtifactName("app/db"), "mariadb-app-db.sql.gz"; got != want {
		t.Fatalf("mariaDBArtifactName() = %q, want %q", got, want)
	}
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("gzip write error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}
	return buf.Bytes()
}

func tempMariaDBInputFile(t *testing.T, data []byte) *os.File {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "mariadb-input-*")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek() error = %v", err)
	}
	t.Cleanup(func() {
		_ = file.Close()
	})
	return file
}
