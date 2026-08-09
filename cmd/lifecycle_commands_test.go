package cmd

import (
	"reflect"
	"strings"
	"testing"

	corelifecycle "github.com/libops/sitectl/internal/lifecycle"
	"github.com/libops/sitectl/pkg/config"
)

func TestParseLifecycleCommandListSupportsOnlyLogicalLists(t *testing.T) {
	t.Parallel()

	got, err := parseLifecycleCommandList("docker compose pull --quiet || docker compose pull && true")
	if err != nil {
		t.Fatalf("parseLifecycleCommandList() error = %v", err)
	}
	wantCommands := []string{"docker compose pull --quiet", "docker compose pull", "true"}
	wantOperators := []string{"||", "&&"}
	if !reflect.DeepEqual(got.commands, wantCommands) || !reflect.DeepEqual(got.operators, wantOperators) {
		t.Fatalf("parseLifecycleCommandList() = %#v, want commands=%v operators=%v", got, wantCommands, wantOperators)
	}
}

func TestLifecycleCommandArgvKeepsQuotedMetacharactersInOneArgument(t *testing.T) {
	t.Parallel()

	list, err := parseLifecycleCommandList(`program 'institution; rm -rf /'`)
	if err != nil {
		t.Fatalf("parseLifecycleCommandList() error = %v", err)
	}
	got, composeUp, err := lifecycleCommandArgv(&config.Context{}, list.commands[0])
	if err != nil {
		t.Fatalf("lifecycleCommandArgv() error = %v", err)
	}
	want := []string{"program", "institution; rm -rf /"}
	if !reflect.DeepEqual(got, want) || composeUp {
		t.Fatalf("lifecycleCommandArgv() = %v, composeUp=%t; want %v, false", got, composeUp, want)
	}
}

func TestLifecycleCommandArgvResolvesContextOwnedPWDWithoutShell(t *testing.T) {
	t.Parallel()

	projectDir := "/srv/sites/institution archive"
	command := `docker compose run --rm --no-deps --user "$(id -u):$(id -g)" --volume "$PWD:/workspace:z" --workdir /workspace --entrypoint composer wp require vendor/package`
	got, _, err := lifecycleCommandArgv(&config.Context{DockerHostType: config.ContextLocal, ProjectDir: projectDir}, command)
	if err != nil {
		t.Fatalf("lifecycleCommandArgv() error = %v", err)
	}
	volumeIndex := -1
	for index, argument := range got {
		if argument == "--volume" {
			volumeIndex = index
			break
		}
	}
	if volumeIndex < 0 || volumeIndex+1 >= len(got) {
		t.Fatalf("lifecycle argv is missing --volume: %v", got)
	}
	if want := projectDir + ":/workspace:z"; got[volumeIndex+1] != want {
		t.Fatalf("lifecycle volume = %q, want %q", got[volumeIndex+1], want)
	}
	for _, argument := range got {
		if strings.ContainsRune(argument, '$') {
			t.Fatalf("lifecycle argv retained shell expansion in %q", argument)
		}
	}
}

func TestLifecycleCommandArgvPreservesDockerGlobalOptionsForCompose(t *testing.T) {
	t.Parallel()

	ctx := &config.Context{ProjectDir: "/srv/project", ComposeFile: []string{"compose.yaml"}}
	got, composeUp, err := lifecycleCommandArgv(ctx, `docker --context production compose up -d`)
	if err != nil {
		t.Fatalf("lifecycleCommandArgv() error = %v", err)
	}
	want := []string{"docker", "--context", "production", "compose", "-f", "compose.yaml", "up", "-d"}
	if !reflect.DeepEqual(got, want) || !composeUp {
		t.Fatalf("lifecycleCommandArgv() = %v, composeUp=%t; want %v, true", got, composeUp, want)
	}
}

func TestLifecycleCommandsRejectInlineProgramsAndShellGrammar(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		`bash`,
		`/bin/sh -`,
		`sh -s`,
		`'C:\tools\bash.exe' -i`,
		`env SAFE=1 bash -s`,
		`env -S 'bash -c embedded'`,
		`busybox sh -c 'echo embedded'`,
		`sh -lc 'echo embedded'`,
		`bash --norc -O extglob -c 'echo embedded'`,
		`docker compose exec app bash -euc 'echo embedded'`,
		`docker compose exec app bash /dev/stdin`,
		`docker compose exec -T drupal drush php:eval '$scheme = compute();'`,
		`docker compose exec -T drupal drush --uri=https://example.org ev '$scheme = compute();'`,
		`php`,
		`/usr/bin/php /proc/self/fd/0`,
		`'C:\php\php.exe' -R 'print $argn;'`,
		`docker compose exec -T app php -d memory_limit=-1 -r 'print("embedded");'`,
		`python3`,
		`python -`,
		`python -i scripts/program.py`,
		`python -m module`,
		`docker compose exec -T app /usr/local/bin/python3.12 -I -c 'print("embedded")'`,
		`docker --context production compose exec -T app sh -c 'echo embedded'`,
		`docker --host=ssh://operator@example.org exec app python3 -c 'print("embedded")'`,
		`docker -H ssh://operator@example.org run --rm example/app sh -c 'echo embedded'`,
		`docker container exec app sh -c 'echo embedded'`,
		`docker --context production container run example/app python3 -c 'print("embedded")'`,
		`docker-compose exec -T app php -r 'print("embedded");'`,
		`docker --debug run --entrypoint python3 example/app -c 'print("embedded")'`,
		`docker run example/app /bin/sh -c 'echo embedded'`,
		`docker run example/shell-entrypoint -c 'echo embedded'`,
		`docker compose run shell-service -c 'echo embedded'`,
		`docker compose run --entrypoint 'sh -c' app 'echo embedded'`,
		`docker run --health-cmd 'sh -c embedded' example/app`,
		`node`,
		`node /dev/fd/0`,
		`docker compose exec -T app node --no-warnings --eval 'console.log("embedded")'`,
		`docker compose run --rm --entrypoint php app -r 'print("embedded");'`,
		`perl`,
		`perl -`,
		`perl -E 'say "embedded"'`,
		`ruby`,
		`ruby -e 'puts "embedded"'`,
		`awk`,
		`awk '{ print $1 }'`,
		`awk -f -`,
		`sed`,
		`sed -e 's/a/b/'`,
		`sed -f /dev/stdin`,
		`program > output`,
		`program; another`,
		`program "${UNRESOLVED}"`,
	} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			list, err := parseLifecycleCommandList(command)
			if err == nil {
				_, _, err = lifecycleCommandArgv(&config.Context{}, list.commands[0])
			}
			if err == nil {
				t.Fatalf("expected %q to be rejected", command)
			}
			if !strings.Contains(err.Error(), "script") && !strings.Contains(err.Error(), "operator") && !strings.Contains(err.Error(), "expansion") {
				t.Fatalf("rejection for %q is not actionable: %v", command, err)
			}
		})
	}
}

func TestLifecycleCommandsAllowCheckedProgramFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		command string
		script  string
	}{
		{command: `bash scripts/program.sh --flag -c`, script: "scripts/program.sh"},
		{command: `python3 scripts/program.py --config -c`, script: "scripts/program.py"},
		{command: `php -d memory_limit=-1 scripts/program.php -R`, script: "scripts/program.php"},
		{command: `node --no-warnings scripts/program.js -e`, script: "scripts/program.js"},
		{command: `perl -w scripts/program.pl -E`, script: "scripts/program.pl"},
		{command: `ruby -w scripts/program.rb -e`, script: "scripts/program.rb"},
		{command: `awk -f scripts/program.awk input.txt`, script: "scripts/program.awk"},
		{command: `sed -n -f scripts/program.sed input.txt`, script: "scripts/program.sed"},
		{command: `env SAFE=1 bash scripts/program.sh`, script: "scripts/program.sh"},
		{command: `busybox sh scripts/program.sh`, script: "scripts/program.sh"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.command, func(t *testing.T) {
			t.Parallel()
			argv, _, err := lifecycleCommandArgv(&config.Context{}, test.command)
			if err != nil {
				t.Fatalf("lifecycleCommandArgv() error = %v", err)
			}
			script, resolved, err := corelifecycle.ProjectScriptPath("/srv/project", argv)
			if err != nil {
				t.Fatalf("ProjectScriptPath() error = %v", err)
			}
			if script != test.script || resolved != "/srv/project/"+test.script {
				t.Fatalf("ProjectScriptPath() = %q, %q; want %q, %q", script, resolved, test.script, "/srv/project/"+test.script)
			}
		})
	}
}

func TestProjectScriptPathRejectsCrossPlatformAbsoluteAndAmbiguousPaths(t *testing.T) {
	t.Parallel()

	for _, script := range []string{
		"/tmp/repair.sh",
		`C:/temp/repair.sh`,
		`C:\temp\repair.sh`,
		`scripts\repair.sh`,
		"../repair.sh",
	} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			_, _, err := corelifecycle.ProjectScriptPath("/srv/project", []string{"sh", script})
			if err == nil || !strings.Contains(err.Error(), "project-relative") {
				t.Fatalf("ProjectScriptPath(%q) error = %v, want portable project-relative refusal", script, err)
			}
		})
	}
}

func TestLifecycleCommandsAllowCheckedContainerProgramFiles(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		`docker compose exec -T app sh /usr/local/libexec/repair.sh -c`,
		`docker compose exec -T app php /usr/local/libexec/repair.php -R`,
		`docker compose exec -T app python3 /usr/local/libexec/repair.py -c`,
		`docker compose run --rm --entrypoint php app /usr/local/libexec/repair.php -R`,
		`docker --context production compose exec -T app sh /usr/local/libexec/repair.sh -c`,
		`docker --host=ssh://operator@example.org exec app python3 /usr/local/libexec/repair.py -c`,
		`docker run --rm example/app sh /usr/local/libexec/repair.sh -c`,
		`docker container exec app sh /usr/local/libexec/repair.sh -c`,
		`docker-compose exec -T app php /usr/local/libexec/repair.php -R`,
		`docker run --entrypoint php example/app /usr/local/libexec/repair.php -R`,
		`docker compose up -d php node`,
	} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			if _, _, err := lifecycleCommandArgv(&config.Context{}, command); err != nil {
				t.Fatalf("lifecycleCommandArgv() error = %v", err)
			}
		})
	}
}
