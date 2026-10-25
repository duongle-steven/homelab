// Package cli is the lab command (upstream nixos's CLI layout; its other commands come in a later step).
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "lab",
		Short:         "Homelab tools",
		Example:       "  lab test scope --base origin/dev\n  lab infra state ensure --bucket homelab-production-tfstate\n  lab infra forgejo push\n  lab backup list\n  lab secrets rotate grafana.admin",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTestCmd())
	root.AddCommand(newBackupCmd())
	root.AddCommand(newSecretsCmd())
	infra := &cobra.Command{Use: "infra", Short: "Manage infrastructure and deploy to production"}
	infra.AddCommand(newInfraStateCmd())
	infra.AddCommand(newInfraForgejoCmd())
	root.AddCommand(infra)
	return root
}

// Execute runs the lab CLI and exits on failure.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		// A diagnostic-write failure cannot change the already-failed exit status.
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
