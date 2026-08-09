package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"
)

func init() { RootCmd.AddCommand(certsCommand()) }

func certsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "certs", Short: "Manage the local certificate authority used by site ingress", GroupID: "ops"}
	cmd.AddCommand(certsRegenerateCommand(), certsTrustCommand())
	return cmd
}

func certsRegenerateCommand() *cobra.Command {
	opts := struct{ service, caSubject, altNames string }{service: "init", caSubject: "/CN=LibOps Local Development CA", altNames: "DNS:localhost,IP:127.0.0.1,IP:::1"}
	cmd := &cobra.Command{
		Use: "regenerate", Short: "Create a new local CA and ingress certificate when either is missing", Args: cobra.NoArgs,
		Long: "Run the shared certificate generator in the Compose init service. Existing keys and certificates are retained; remove them explicitly before requesting replacement.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			return runContextCompose(cmd, *ctx, []string{"run", "--rm", "--entrypoint", "generate-certs.sh", "-e", "CERT_DIR=/work/certs", "-e", "CA_SUBJECT=" + opts.caSubject, "-e", "SUBJECT_ALT_NAMES=" + opts.altNames, opts.service})
		},
	}
	cmd.Flags().StringVar(&opts.service, "service", "init", "Compose service that mounts the project's certificate directory and contains the generator.")
	cmd.Flags().StringVar(&opts.caSubject, "ca-subject", opts.caSubject, "OpenSSL subject assigned to a newly generated local certificate authority.")
	cmd.Flags().StringVar(&opts.altNames, "subject-alt-names", opts.altNames, "Comma-separated OpenSSL DNS and IP subject alternative names placed on the leaf certificate.")
	return cmd
}

func certsTrustCommand() *cobra.Command {
	return &cobra.Command{
		Use: "trust", Short: "Add the site's local CA to this workstation's operating-system trust store", Args: cobra.NoArgs,
		Long: "Trust certs/rootCA.pem from the active site on this workstation. This changes the local OS trust store and may request administrator privileges on Linux.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := resolveCurrentContext(cmd)
			if err != nil {
				return err
			}
			data, err := ctx.ReadFile(filepath.Join(ctx.ProjectDir, "certs", "rootCA.pem"))
			if err != nil {
				return fmt.Errorf("read site CA: %w", err)
			}
			temp, err := os.CreateTemp("", "sitectl-rootCA-*.pem")
			if err != nil {
				return err
			}
			path := temp.Name()
			defer os.Remove(path)
			if _, err = temp.Write(data); err != nil {
				_ = temp.Close()
				return err
			}
			if err = temp.Close(); err != nil {
				return err
			}
			home := ""
			if runtime.GOOS == "darwin" {
				home, err = os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("resolve user home for login keychain: %w", err)
				}
			}
			commands, err := certificateTrustCommandArgs(runtime.GOOS, home, path)
			if err != nil {
				return err
			}
			for index, args := range commands {
				trust := exec.CommandContext(cmd.Context(), args[0], args[1:]...) // #nosec G204 -- platform-specific executable is fixed and paths remain distinct argv entries.
				trust.Stdout = cmd.OutOrStdout()
				trust.Stderr = cmd.ErrOrStderr()
				trust.Stdin = cmd.InOrStdin()
				if err := trust.Run(); err != nil {
					return fmt.Errorf("trust local CA (step %d of %d): %w", index+1, len(commands), err)
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Local certificate authority trusted. Restart browsers that cache certificate stores.")
			return err
		},
	}
}

func certificateTrustCommandArgs(goos, home, certificatePath string) ([][]string, error) {
	switch goos {
	case "darwin":
		if home == "" {
			return nil, fmt.Errorf("user home cannot be empty on darwin")
		}
		return [][]string{{
			"security", "add-trusted-cert", "-d", "-r", "trustRoot", "-k",
			filepath.Join(home, "Library/Keychains/login.keychain-db"), certificatePath,
		}}, nil
	case "linux":
		return [][]string{
			{"sudo", "install", "-m", "0644", certificatePath, "/usr/local/share/ca-certificates/libops-local.crt"},
			{"sudo", "update-ca-certificates"},
		}, nil
	case "windows":
		return [][]string{{"certutil", "-addstore", "-user", "Root", certificatePath}}, nil
	default:
		return nil, fmt.Errorf("automatic trust is not supported on %s; import certs/rootCA.pem manually", goos)
	}
}
