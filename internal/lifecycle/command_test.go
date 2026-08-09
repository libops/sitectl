package lifecycle

import (
	"reflect"
	"testing"

	"github.com/libops/sitectl/pkg/config"
)

func TestExpandLocalHostIdentityUsesDockerCompatibleRootWhenNumericIdentityUnavailable(t *testing.T) {
	original := resolveLocalComposeHostNumericIdentity
	t.Cleanup(func() { resolveLocalComposeHostNumericIdentity = original })
	resolveLocalComposeHostNumericIdentity = func() (string, string, bool, error) {
		return "", "", false, nil
	}

	fields := []string{"docker", "compose", "run", "--user", "$(id -u):$(id -g)", "init"}
	got, err := expandLocalHostIdentity(&config.Context{DockerHostType: config.ContextLocal}, fields)
	if err != nil {
		t.Fatalf("expandLocalHostIdentity() error = %v", err)
	}
	want := []string{"docker", "compose", "run", "--user", "0:0", "init"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded lifecycle fields = %#v, want %#v", got, want)
	}
}
