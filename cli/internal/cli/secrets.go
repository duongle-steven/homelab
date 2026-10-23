package cli

import (
	"fmt"

	"github.com/duongle-steven/homelab/cli/internal/rotate"
	"github.com/spf13/cobra"
)

func newSecretsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "secrets",
		Short: "Secrets rotation: the catalog and its check",
		Long: "The catalog (cli/internal/rotate/catalog.yaml) lists every secret and how it is replaced " +
			"(docs/superpowers/specs/2026-10-24-secrets-rotation-design.md).",
	}
	check := &cobra.Command{
		Use:   "check",
		Short: "Fail when a secret the repository creates has no catalog entry",
		Long: "Reads the generated secrets, the infra-secrets paths, the confidential Kanidm clients (rendered with " +
			"scripts/render-chart) and the Forgejo tokens under --root.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := rotate.Load()
			if err != nil {
				return err
			}
			root, err := cmd.Flags().GetString("root")
			if err != nil {
				return err
			}
			problems := rotate.Check(root, entries)
			for _, p := range problems {
				fmt.Fprintln(cmd.ErrOrStderr(), "FAIL", p)
			}
			if len(problems) > 0 {
				return fmt.Errorf("%d secrets the catalog does not know", len(problems))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secrets check: all secrets are in the catalog (%d entries)\n", len(entries))
			return nil
		},
	}
	check.Flags().String("root", ".", "the repository's checkout")
	group.AddCommand(check)
	return group
}
