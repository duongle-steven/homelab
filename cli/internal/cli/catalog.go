package cli

import (
	"github.com/spf13/cobra"

	"github.com/duongle-steven/homelab/cli/internal/catalog"
	"github.com/duongle-steven/homelab/cli/internal/scope"
)

func newCatalogCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "catalog",
		Short: "Generate Backstage's catalog and homepage's services from each app's and component's description",
	}
	group.AddCommand(&cobra.Command{
		Use:   "generate",
		Short: "Write catalog/ and the services.yaml block of apps/homepage.yaml",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := scope.Root()
			if err != nil {
				return err
			}
			return catalog.Write(root)
		},
	}, &cobra.Command{
		Use:   "check",
		Short: "Fail when catalog/ or homepage's services differ from what generate writes",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := scope.Root()
			if err != nil {
				return err
			}
			return catalog.Check(root)
		},
	})
	return group
}
