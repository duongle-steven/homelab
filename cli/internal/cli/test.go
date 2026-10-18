package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/duongle-steven/homelab/cli/internal/scope"
)

func newTestCmd() *cobra.Command {
	test := &cobra.Command{Use: "test", Short: "Choose and check the sandbox tests"}
	test.AddCommand(newTestScopeCmd())
	return test
}

func newTestScopeCmd() *cobra.Command {
	var base, tests, config string
	var alone, full, check bool
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "Print the go test -run regex of the sandbox tests a change can affect",
		Long: "Reads test/scopes.yaml and the files changed since --base, and prints one line: the regex of the main run, " +
			"or with --alone of the tests that run after it; nothing when every change is skipped. --check verifies " +
			"that the map covers every component and names existing tests.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := scope.Root()
			if err != nil {
				return err
			}
			if !filepath.IsAbs(config) {
				config = filepath.Join(root, config)
			}
			c, err := scope.Load(config)
			if err != nil {
				return err
			}
			if check {
				tracked, err := scope.Tracked(root)
				if err != nil {
					return err
				}
				names, err := scope.AllTests(root)
				if err != nil {
					return err
				}
				problems := c.Check(tracked, names)
				for _, p := range problems {
					fmt.Fprintln(cmd.ErrOrStderr(), p)
				}
				if len(problems) > 0 {
					return fmt.Errorf("test/scopes.yaml: %d problem(s)", len(problems))
				}
				return nil
			}
			var s scope.Selection
			switch {
			case tests != "":
				s = scope.ManualSelection(tests)
			case full:
				s = scope.Selection{Full: true}
			default:
				files, err := scope.Changed(root, base)
				if err != nil {
					return err
				}
				if s, err = c.Select(files, scope.FileTests(root)); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), c.Regex(s, alone))
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&base, "base", "origin/dev", "compare HEAD with this ref (merge base)")
	flags.BoolVar(&alone, "alone", false, "print the tests that run alone, after the main run")
	flags.BoolVar(&full, "full", false, "the whole suite")
	flags.StringVar(&tests, "tests", "", "exactly these tests (a go test -run regex)")
	flags.BoolVar(&check, "check", false, "verify the map instead of printing a selection")
	flags.StringVar(&config, "config", "test/scopes.yaml", "the map, relative to the repository root")
	cmd.MarkFlagsMutuallyExclusive("full", "tests", "check")
	return cmd
}
