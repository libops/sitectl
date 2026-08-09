package cmd

import (
	"reflect"
	"testing"
)

func TestCertificateTrustCommandArgsLinuxUsesOrderedDirectCommands(t *testing.T) {
	t.Parallel()

	got, err := certificateTrustCommandArgs("linux", "", "/tmp/root CA.pem")
	if err != nil {
		t.Fatalf("certificateTrustCommandArgs() error = %v", err)
	}
	want := [][]string{
		{"sudo", "install", "-m", "0644", "/tmp/root CA.pem", "/usr/local/share/ca-certificates/libops-local.crt"},
		{"sudo", "update-ca-certificates"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("certificateTrustCommandArgs() = %v, want %v", got, want)
	}
	for _, args := range got {
		if args[0] == "sh" || args[0] == "bash" {
			t.Fatalf("trust command unexpectedly invokes a shell: %v", args)
		}
	}
}

func TestCertificateTrustCommandArgsDarwinUsesProvidedHome(t *testing.T) {
	t.Parallel()

	got, err := certificateTrustCommandArgs("darwin", "/Users/archivist", "/tmp/rootCA.pem")
	if err != nil {
		t.Fatalf("certificateTrustCommandArgs() error = %v", err)
	}
	want := [][]string{{
		"security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k",
		"/Users/archivist/Library/Keychains/login.keychain-db", "/tmp/rootCA.pem",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("certificateTrustCommandArgs() = %v, want %v", got, want)
	}
}
