package cmd

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

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
