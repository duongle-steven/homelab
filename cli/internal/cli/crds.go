package cli

import (
	"github.com/spf13/cobra"

	"github.com/duongle-steven/homelab/cli/internal/crds"
	"github.com/duongle-steven/homelab/cli/internal/scope"
)

func newCRDsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "crds",
		Short: "Choose the CRDs each environment applies (system/crds/environments/<env>.txt)",
	}
	group.AddCommand(&cobra.Command{
		Use:   "generate",
		Short: "Write each environment's list: its components' CRDs and those of the kinds its renders use",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := scope.Root()
			if err != nil {
				return err
			}
			return crds.Write(root)
		},
	}, &cobra.Command{
		Use:   "check",
		Short: "Fail when an environment's list differs from what generate writes",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := scope.Root()
			if err != nil {
				return err
			}
			return crds.Check(root)
		},
	})
	return group
}
