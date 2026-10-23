package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/backup"
	"github.com/duongle-steven/homelab/cli/internal/rotate"
	"github.com/duongle-steven/homelab/cli/internal/secrets"
	"github.com/duongle-steven/homelab/cli/internal/workload"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func newSecretsCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "secrets",
		Short: "Secrets rotation: the catalog, its check, list and rotate",
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

	list := &cobra.Command{
		Use:   "list",
		Short: "List every secret: its method, class, period, last rotation and next due date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := rotate.Load()
			if err != nil {
				return err
			}
			rot, err := newRotation(cmd, entries)
			if err != nil {
				return err
			}
			defer rot.close(cmd)
			return printList(cmd.Context(), cmd.OutOrStdout(), rot.Rotator, time.Now())
		},
	}
	list.Flags().String("env", "production", "production, or sandbox")
	group.AddCommand(list)

	rotateCmd := &cobra.Command{
		Use:   "rotate <name>... | --all | --scheduled",
		Short: "Replace secrets by their official method, resuming an interrupted rotation",
		Long: "Prints the plan (what restarts, marking workloads with one replica, which stop briefly, and who must act), " +
			"asks, then rotates one entry after another and stops at the first failure. It first wants a completed " +
			"scheduled off-site backup from the last 24 hours. --scheduled (the CronJob) rotates only the due entries, " +
			"never manual ones, without asking. It fails when an entry failed, was named but not rotated, or is more " +
			"than 14 days past its due date.",
		Example: "  lab secrets rotate grafana.admin\n  lab secrets rotate --all\n  lab secrets rotate --scheduled",
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			scheduled, _ := cmd.Flags().GetBool("scheduled")
			yes, _ := cmd.Flags().GetBool("yes")
			skip, _ := cmd.Flags().GetString("skip-backup-check")
			if cmd.Flags().Changed("skip-backup-check") && strings.TrimSpace(skip) == "" {
				return errors.New("--skip-backup-check needs a reason, e.g. --skip-backup-check='leaked token'")
			}
			entries, err := rotate.Load()
			if err != nil {
				return err
			}
			o := rotate.Options{
				All: all, Scheduled: scheduled, Yes: yes || scheduled,
				Interactive:     !scheduled && stdinIsTerminal(cmd),
				SkipBackupCheck: strings.TrimSpace(skip),
				Now:             time.Now(),
				By:              who(),
			}
			// The catalog alone answers a mistyped name or bad flags, before reaching any cluster (only the due
			// entries of --scheduled need the records)
			if !scheduled || len(args) > 0 {
				if _, err := (&rotate.Rotator{Entries: entries}).Plan(cmd.Context(), args, o); err != nil {
					return err
				}
			}
			rot, err := newRotation(cmd, entries)
			if err != nil {
				return err
			}
			defer rot.close(cmd)
			ctx := cmd.Context()
			plan, err := rot.Rotator.Plan(ctx, args, o)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(plan) == 0 {
				_, err := fmt.Fprintln(out, "Nothing is due.")
				return err
			}
			printPlan(ctx, out, rot, plan, o)
			if !scheduled {
				if err := confirm(cmd, fmt.Sprintf("Rotate %d secrets?", len(plan))); err != nil {
					return err
				}
			}
			results, err := rot.Rotator.Run(ctx, plan, o)
			if len(results) > 0 {
				fmt.Fprintln(out, "\nResults:")
			}
			for _, r := range results {
				line := fmt.Sprintf("  %s: %s", r.Name, r.Outcome)
				if r.Err != nil {
					line += ": " + r.Err.Error()
				}
				if r.Overdue {
					line += " (overdue)"
				}
				fmt.Fprintln(out, line)
			}
			return err
		},
	}
	rotateCmd.Flags().String("env", "production", "production, or sandbox")
	rotateCmd.Flags().Bool("all", false, "rotate every catalog entry")
	rotateCmd.Flags().Bool("scheduled", false, "rotate only the due entries, never manual ones, without asking (implies --yes)")
	rotateCmd.Flags().Bool("yes", false, "do not ask for confirmation (required without a terminal)")
	rotateCmd.Flags().String("skip-backup-check", "", "rotate without a recent off-site backup; the reason is recorded")
	group.AddCommand(rotateCmd)
	return group
}

// rotation is a Rotator on one cluster, with what the plan shows
type rotation struct {
	Rotator *rotate.Rotator
	// Replicas is a workload's desired replicas
	Replicas func(ctx context.Context, namespace string, w workload.Ref) (int32, error)
	Close    func(ctx context.Context) error
}

func (r *rotation) close(cmd *cobra.Command) {
	if r.Close == nil {
		return
	}
	if err := r.Close(context.WithoutCancel(cmd.Context())); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", err)
	}
}

// newRotation reaches the environment's cluster and OpenBao (tests replace it).
var newRotation = func(cmd *cobra.Command, entries []rotate.Entry) (*rotation, error) {
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
	store, err := secrets.Open(cmd.Context(), config)
	if err != nil {
		return nil, err
	}
	b := backup.New(dyn, kube)
	return &rotation{
		Rotator: &rotate.Rotator{
			Entries: entries,
			// Each kind's method comes with its own task (docs/superpowers/plans/2026-10-24-secrets-rotation.md)
			Methods: map[rotate.Kind]rotate.Method{},
			Store:   rotate.OpenBaoRecords{Store: store},
			Backups: b.Runs,
			Idle:    func(ctx context.Context, what string) (bool, error) { return idle(ctx, kube, what) },
			Out:     cmd.OutOrStdout(),
			Poll:    15 * time.Second,
			Wait:    30 * time.Minute,
		},
		Replicas: func(ctx context.Context, namespace string, w workload.Ref) (int32, error) {
			return replicas(ctx, kube, namespace, w)
		},
		Close: store.Close,
	}, nil
}

// idle reports whether what has nothing running that a restart would break.
func idle(ctx context.Context, kube kubernetes.Interface, what string) (bool, error) {
	switch what {
	case "woodpecker":
		// A running step pod carries the label woodpecker-ci.org/step (Task 1's finding).
		// TODO(Task 8): Woodpecker's method may also read the server's queue (GET /api/queue/info).
		pods, err := kube.CoreV1().Pods("woodpecker").List(ctx, metav1.ListOptions{LabelSelector: "woodpecker-ci.org/step"})
		if err != nil {
			return false, fmt.Errorf("list Woodpecker's step pods: %w", err)
		}
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodPending || p.Status.Phase == corev1.PodRunning {
				return false, nil
			}
		}
		return true, nil
	}
	// TODO(Task 5): paperless's idle check (no queued consumption task) comes with the generated secrets' method
	return false, fmt.Errorf("no idle check for %s yet", what)
}

func replicas(ctx context.Context, kube kubernetes.Interface, namespace string, w workload.Ref) (int32, error) {
	var n *int32
	switch w.Kind {
	case "Deployment":
		d, err := kube.AppsV1().Deployments(namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		n = d.Spec.Replicas
	case "StatefulSet":
		s, err := kube.AppsV1().StatefulSets(namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		n = s.Spec.Replicas
	default:
		return 0, fmt.Errorf("unknown workload kind %s", w.Kind)
	}
	if n == nil {
		return 1, nil
	}
	return *n, nil
}

// who is the person or job a rotation records: user@host, or the pod's name in the cluster
func who() string {
	host, _ := os.Hostname()
	if user := os.Getenv("USER"); user != "" {
		return user + "@" + host
	}
	return host
}

// printPlan shows each entry's effect: what restarts (one replica stops briefly), who must act, or why it is skipped
func printPlan(ctx context.Context, out io.Writer, rot *rotation, plan []rotate.Entry, o rotate.Options) {
	fmt.Fprintf(out, "Plan (%d secrets, in this order):\n", len(plan))
	for _, e := range plan {
		if skip := rot.Rotator.Skip(e, o); skip != "" {
			fmt.Fprintf(out, "  %s: skipped: %s\n", e.Name, skip)
			continue
		}
		fmt.Fprintf(out, "  %s: %s, class %d: %s\n", e.Name, e.Kind, e.Class, e.Reason)
		if e.Idle != "" {
			fmt.Fprintf(out, "    waits until %s is idle\n", e.Idle)
		}
		for _, r := range e.Readers {
			if r.People {
				fmt.Fprintln(out, "    people must act: they re-enter the new value")
				continue
			}
			for _, w := range r.Workloads {
				note := ""
				if n, err := rot.Replicas(ctx, r.Namespace, w); err != nil {
					note = " (replicas unknown)"
				} else if n <= 1 {
					note = " (one replica: it stops briefly)"
				}
				fmt.Fprintf(out, "    restarts %s %s/%s%s\n", w.Kind, r.Namespace, w.Name, note)
			}
		}
		if e.Kind == rotate.KindManual {
			fmt.Fprintf(out, "    a person acts: %s\n", e.Steps)
		}
	}
}

func printList(ctx context.Context, out io.Writer, r *rotate.Rotator, now time.Time) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tCLASS\tPERIOD\tLAST ROTATION\tNEXT DUE")
	for _, e := range r.Entries {
		class := fmt.Sprint(e.Class)
		if e.Kind == rotate.KindNever || e.Kind == rotate.KindAutomatic {
			class = "-"
		}
		period := "command"
		if e.Period > 0 {
			period = fmt.Sprintf("%dd", int(e.Period.Hours()/24))
		}
		last, next := "-", "-"
		if e.Kind != rotate.KindNever && e.Kind != rotate.KindAutomatic {
			rec, err := r.Store.Get(ctx, e)
			if err != nil {
				return err
			}
			last = "never"
			if !rec.At.IsZero() {
				last = rec.At.Local().Format(time.DateTime) + " by " + rec.By
			}
			if rec.Phase != "" && rec.Phase != rotate.PhaseDone {
				last += " (interrupted after " + rec.Phase + ": rotate it again)"
			}
			if due, ok := rotate.Due(e, rec); ok {
				switch {
				case due.IsZero():
					next = "now"
				default:
					next = due.Local().Format(time.DateOnly)
				}
				if now.Sub(due) > rotate.OverdueMargin {
					next += " (overdue)"
				}
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, e.Kind, class, period, last, next)
	}
	return w.Flush()
}
