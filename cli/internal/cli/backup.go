package cli

import (
	"bufio"
	"context"
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
		Use:   "push <app>",
		Short: "Copy one app's labelled volumes off-site now, after showing their size",
		Long: "For the apps labelled manual (Actual Budget, paperless, OpenCloud), or before a risky change to a daily one. " +
			"It prints the volumes' size and the bucket's against R2's 10 GB free tier, asks, then waits for Velero (up to an hour).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := args[0]
			c, err := newBackupClient(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			readOnly, err := c.Mode(ctx)
			if err != nil {
				return err
			}
			if readOnly {
				return fmt.Errorf("location default is read-only: lab backup enable first")
			}
			volumes, err := c.VolumeBytes(ctx, app)
			if err != nil {
				return err
			}
			if volumes == 0 {
				return fmt.Errorf("%s has no mounted volume labelled homelab/backup daily or manual", app)
			}
			bucket, err := c.BucketBytes(ctx)
			if err != nil {
				return err
			}
			after := volumes + bucket
			fmt.Fprintf(cmd.OutOrStdout(), "%s: volumes %s, bucket %s now: after %s of 10 GB free on R2 (deduplication makes the real growth smaller)\n",
				app, gb(volumes), gb(bucket), gb(after))
			if after > backup.FreeTier {
				fmt.Fprintln(cmd.OutOrStdout(), "Over R2's free tier: each GB beyond it costs about $0.015 a month.")
			}
			if err := confirm(cmd, fmt.Sprintf("Copy %s's volumes off-site?", app)); err != nil {
				return err
			}
			run, err := c.Push(ctx, app, time.Hour)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Backup %s completed.\n", run.Name)
			return err
		},
	})
	restore := &cobra.Command{
		Use:   "restore [<app>]",
		Short: "Restore the whole cluster after a fresh install, or one app's volumes",
		Long: "Without an app: on a cluster Argo CD has built from git, with the location read-only, restore every app with a daily " +
			"volume from one off-site run (default: the newest that completed before this cluster was created), OpenBao first, " +
			"and each manual app from its newest push; then every reader restarts on the restored secrets. Check the data, then " +
			"lab backup enable.\n\nWith an app: replace its volumes from the newest local snapshot at or before --at (default " +
			"now), or --from offsite. Argo CD's sync of the app stays paused meanwhile.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newBackupClient(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			name, _ := cmd.Flags().GetString("backup")
			runs, err := c.Runs(ctx)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				created, err := c.ClusterCreated(ctx)
				if err != nil {
					return err
				}
				run, err := backup.PickRun(runs, created, name)
				if err != nil {
					return err
				}
				plan, err := c.PlanCluster(ctx, run)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "From %s (%s): %s\n", run.Name, run.Started.Local().Format(time.DateTime), strings.Join(plan.Apps, ", "))
				for app, r := range plan.Pushes {
					fmt.Fprintf(out, "From %s (%s): %s\n", r.Name, r.Started.Local().Format(time.DateTime), app)
				}
				if err := confirm(cmd, "Replace these apps' volumes, then restart every reader of a secret?"); err != nil {
					return err
				}
				if err := c.RestoreCluster(ctx, plan); err != nil {
					return err
				}
				_, err = fmt.Fprintln(out, "Restored. Check the apps (docs/how-to-guides/backup-and-restore.md), then lab backup enable.")
				return err
			}
			app := args[0]
			from, _ := cmd.Flags().GetString("from")
			atFlag, _ := cmd.Flags().GetString("at")
			var at time.Time
			if atFlag != "" {
				if at, err = time.Parse(time.RFC3339, atFlag); err != nil {
					return fmt.Errorf("--at: %w (RFC 3339, e.g. 2026-10-22T14:00:00+07:00)", err)
				}
			}
			var run backup.Run
			switch backup.Source(from) {
			case backup.Local:
			case backup.Offsite:
				if run, err = c.AppRun(ctx, runs, app, name); err != nil {
					return err
				}
			default:
				return fmt.Errorf("--from must be local or offsite, not %q", from)
			}
			what := "the newest local snapshot"
			if run.Name != "" {
				what = fmt.Sprintf("backup %s (%s)", run.Name, run.Started.Local().Format(time.DateTime))
			} else if !at.IsZero() {
				what += " at or before " + at.Local().Format(time.DateTime)
			}
			if err := confirm(cmd, fmt.Sprintf("Stop %s and replace its volumes with %s?", app, what)); err != nil {
				return err
			}
			if err := c.RestoreApp(ctx, app, backup.Source(from), at, run); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s restored and synced.\n", app)
			return err
		},
	}
	restore.Flags().String("backup", "", "the off-site run to restore from (lab backup list)")
	restore.Flags().String("from", "local", "one app: local (the hourly snapshots) or offsite")
	restore.Flags().String("at", "", "one app, local: the newest snapshot at or before this time (RFC 3339; default now)")
	group.AddCommand(restore)
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
	c := backup.New(dyn, kube)
	c.PortForward = func(ctx context.Context, namespace, service string, port int) (string, func(), error) {
		return portForward(ctx, config, kube, namespace, service, port)
	}
	return c, nil
}

// gb prints bytes in decimal gigabytes, as R2 bills
func gb(bytes int64) string {
	return fmt.Sprintf("%.1f GB", float64(bytes)/1e9)
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
