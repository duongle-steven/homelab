package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/cluster"
	"github.com/spf13/cobra"
)

func newClusterUpgradeCmd() *cobra.Command {
	var environment, flake string
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "upgrade",
		Short:   "Rebuild the installed nodes from the node flake, one at a time",
		Long:    "Rebuild every installed node whose system differs from what the node flake builds: drain it, nixos-rebuild switch over root SSH (rebooting when the kernel or initrd changed), wait for it to be Ready and the cluster healthy (Ceph HEALTH_OK once it exists), then uncordon it. The initializer goes first. The first failure stops the upgrade with that node cordoned.",
		Example: "  lab cluster upgrade --environment staging --flake infra/_modules/metal/flake",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			if err := cluster.Upgrade(ctx, environment, flake, cmd.ErrOrStderr()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Every node runs its flake's system.")
			return err
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "Environment to upgrade: staging or production (required)")
	command.Flags().StringVar(&flake, "flake", "", "The node flake's directory, with its hosts.json and cluster.json (required)")
	command.Flags().DurationVar(&timeout, "timeout", 2*time.Hour, "Maximum time for the whole upgrade")
	requireFlags(command, "environment", "flake")
	return command
}
