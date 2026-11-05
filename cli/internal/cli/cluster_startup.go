package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/cluster"
	"github.com/spf13/cobra"
)

func newClusterStartupCmd() *cobra.Command {
	var environment string
	var timeout time.Duration
	command := &cobra.Command{
		Use:     "startup",
		Short:   "Undo lab cluster shutdown and wait for it",
		Long:    "Run cluster-startup on the first node that answers, without waiting for a majority of nodes on a new boot: resume the CronJobs the shutdown suspended, start Argo CD's application controller, unset Ceph's noout once every OSD is back (30 minutes at most) and remove the marker. The nodes do this themselves when they boot; this is for a shutdown that failed with the nodes up. With no marker it does nothing.",
		Example: "  lab cluster startup --environment testbed",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			if err := cluster.Startup(ctx, environment, cmd.ErrOrStderr()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Nothing left of a shutdown.")
			return err
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "Environment to start: testbed or production (required)")
	command.Flags().DurationVar(&timeout, "timeout", time.Hour, "Maximum time to wait")
	requireFlags(command, "environment")
	return command
}
