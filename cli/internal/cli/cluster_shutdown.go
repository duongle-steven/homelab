package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/cluster"
	"github.com/spf13/cobra"
)

func newClusterShutdownCmd() *cobra.Command {
	var environment string
	var jobWait, timeout time.Duration
	command := &cobra.Command{
		Use:     "shutdown",
		Short:   "Stop the cluster in order and power its nodes off",
		Long:    "Record what it changes in the ConfigMap kube-system/cluster-shutdown, stop Argo CD's application controller, suspend every CronJob not already suspended (except those labelled homelab/power-keep), wait for running Jobs, set Ceph's noout and power every node off at once. The nodes stay off until someone starts them; when they boot, the unit cluster-startup undoes all of it. A failure leaves the nodes up: run lab cluster startup to undo it, or this command again.",
		Example: "  lab cluster shutdown --environment testbed",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			if err := cluster.Shutdown(ctx, environment, jobWait, cmd.ErrOrStderr()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Every node was told to power off.")
			return err
		},
	}
	command.Flags().StringVar(&environment, "environment", "", "Environment to shut down: testbed or production (required)")
	command.Flags().DurationVar(&jobWait, "job-wait", 5*time.Minute, "How long to wait for running Jobs before going on without them")
	command.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "Maximum time for the whole shutdown")
	requireFlags(command, "environment")
	return command
}
