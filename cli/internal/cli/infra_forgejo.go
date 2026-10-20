package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/duongle-steven/homelab/cli/internal/env"
	"github.com/duongle-steven/homelab/cli/internal/guard"
	"github.com/duongle-steven/homelab/cli/internal/scope"
	"github.com/spf13/cobra"
)

func newInfraForgejoCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "forgejo",
		Short: "Deploy to the GitOps repository ops/homelab on Forgejo",
		Long:  "Production's Argo CD syncs ops/homelab on Forgejo, which changes only through push. --env sandbox targets the sandbox's Forgejo (the tests) and skips the guards.",
	}
	group.PersistentFlags().String("env", "production", "production, or sandbox (the tests)")
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Check that HEAD may be deployed to production",
		Long:  "Pass only when the checkout is origin/master without uncommitted changes, its check checks succeeded, and the pull request merged into master as HEAD has a succeeded check sandbox on its head. A running check or an API error fails.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := forgejoEnv(cmd)
			if err != nil {
				return err
			}
			if e.Name != "production" {
				return fmt.Errorf("verify guards production only")
			}
			head, err := verifyProduction(cmd.Context(), e)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%.12s may be deployed.\n", head)
			return err
		},
	}
	group.AddCommand(verify)
	return group
}

// forgejoEnv loads the environment --env names, from the repository lab runs in.
func forgejoEnv(cmd *cobra.Command) (env.Env, error) {
	name, err := cmd.Flags().GetString("env")
	if err != nil {
		return env.Env{}, err
	}
	root, err := scope.Root()
	if err != nil {
		return env.Env{}, err
	}
	return env.Load(root, name)
}

func verifyProduction(ctx context.Context, e env.Env) (string, error) {
	repository, err := e.GitHubRepository()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return guard.Verify(ctx, e.Root, repository, guard.GitHub{BaseURL: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}})
}
