package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func newBackupCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "backup",
		Short: "Off-site backups with Velero: list, enable, push one app, restore",
		Long: "Velero copies the volumes labelled homelab/backup off-site (docs/how-to-guides/backup-and-restore.md). Every new " +
			"cluster starts read-only: nothing is written or deleted until lab backup enable.",
	}
	group.PersistentFlags().String("env", "production", "production, or sandbox")
	group.PersistentFlags().Bool("yes", false, "do not ask for confirmation (required without a terminal)")
	group.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the off-site backups, newest first, and whether the location is writable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newBackupClient(cmd)
			if err != nil {
				return err
			}
			readOnly, err := c.Mode(cmd.Context())
			if err != nil {
				return err
			}
			runs, err := c.Runs(cmd.Context())
			if err != nil {
				return err
			}
			mode := "writable, schedules on"
			if readOnly {
				mode = "read-only, schedules paused (lab backup enable)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "location default: %s\n\n", mode)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "BACKUP\tOF\tPHASE\tSTARTED")
			for _, r := range runs {
				of := "schedule " + r.Schedule
				if r.App != "" {
					of = "app " + r.App
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, of, r.Phase, r.Started.Local().Format(time.DateTime))
			}
			return w.Flush()
		},
	})
	group.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Make the location writable and start the schedules (a brand-new homelab, or a checked restore)",
		Long: "Velero then writes a daily copy and the prune job keeps the newest 30 of each schedule and app. Run it once on a " +
			"brand-new homelab, or after a restore was checked: a restore needs the location read-only.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newBackupClient(cmd)
			if err != nil {
				return err
			}
			runs, err := c.Runs(cmd.Context())
			if err != nil {
				return err
			}
			newest := "no backup yet"
			if len(runs) > 0 {
				newest = fmt.Sprintf("the newest backup is %s (%s, %s)", runs[0].Name, runs[0].Phase, runs[0].Started.Local().Format(time.DateTime))
			}
			if err := confirm(cmd, fmt.Sprintf("Location default becomes writable and the schedules start; %s. Continue?", newest)); err != nil {
				return err
			}
			if err := c.SetMode(cmd.Context(), false); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Backups enabled.")
			return err
		},
	})
	group.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Make the location read-only and pause the schedules (before a restore)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newBackupClient(cmd)
			if err != nil {
				return err
			}
			if err := c.SetMode(cmd.Context(), true); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Backups disabled: read-only, schedules paused.")
			return err
		},
	})
	return group
}

// newBackupClient reaches the environment's cluster (tests replace it).
var newBackupClient = func(cmd *cobra.Command) (*backup.Client, error) {
	e, err := forgejoEnv(cmd)
	if err != nil {
		return nil, err
	}
	config, err := clientcmd.BuildConfigFromFlags("", e.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", e.Kubeconfig, err)
	}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return backup.New(dyn, kube), nil
}

// stdinIsTerminal reports whether a person can answer on stdin (tests replace it).
var stdinIsTerminal = func(cmd *cobra.Command) bool {
	f, ok := cmd.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// confirm asks question on a terminal and fails unless the answer is yes; without a terminal it fails unless --yes.
func confirm(cmd *cobra.Command, question string) error {
	if yes, _ := cmd.Flags().GetBool("yes"); yes {
		return nil
	}
	if !stdinIsTerminal(cmd) {
		return fmt.Errorf("%s\nno terminal to answer on: pass --yes", question)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N] ", question)
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return fmt.Errorf("stopped: nothing changed")
}
