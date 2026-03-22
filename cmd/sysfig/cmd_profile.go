package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// ── profile ────────────────────────────────────────────────────────────────────

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile <subcommand>",
		Short: "Manage sysfig profiles (isolated config sets)",
		Long: `Profiles let you maintain separate sets of tracked files, each with their
own git repo, state, keys, and snapshots.

Examples:
  sysfig profile list
  sysfig profile create work
  sysfig --profile work track /etc/nginx/nginx.conf
  sysfig --profile work sync
  sysfig profile delete personal`,
	}
	cmd.AddCommand(
		newProfileListCmd(),
		newProfileCreateCmd(),
		newProfileDeleteCmd(),
	)
	return cmd
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			home := sysfigHome()
			pDir := profilesDir()

			active := globalProfile
			if active == "" {
				active = os.Getenv("SYSFIG_PROFILE")
			}

			// Collect rows first so we can compute column width.
			type profileRow struct{ name, path, marker string }
			rows := []profileRow{}
			defaultMarker := ""
			if active == "" {
				defaultMarker = clrOK.Sprint(" ◀ active")
			}
			rows = append(rows, profileRow{"default", home, defaultMarker})

			entries, err := os.ReadDir(pDir)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				marker := ""
				if name == active {
					marker = clrOK.Sprint(" ◀ active")
				}
				rows = append(rows, profileRow{name, filepath.Join(pDir, name), marker})
			}

			nameW := len("NAME")
			for _, r := range rows {
				if len(r.name) > nameW {
					nameW = len(r.name)
				}
			}
			nameW += 2

			fmt.Printf("  %s  %s\n", clrBold.Sprint(pad("NAME", nameW)), clrBold.Sprint("PATH"))
			divider()
			for _, r := range rows {
				fmt.Printf("  %s  %s%s\n", clrBold.Sprint(pad(r.name, nameW)), clrDim.Sprint(r.path), r.marker)
			}
			return nil
		},
	}
}

func newProfileCreateCmd() *cobra.Command {
	var setupURL string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create and initialise a new profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "default" {
				return fmt.Errorf("'default' is reserved — it refers to ~/.sysfig")
			}

			baseDir := filepath.Join(profilesDir(), name)
			if _, err := os.Stat(baseDir); err == nil {
				return fmt.Errorf("profile %q already exists at %s", name, baseDir)
			}

			clrBold.Printf("Creating profile %q\n\n", name)

			if setupURL != "" {
				// Clone from remote.
				result, err := core.Clone(core.CloneOptions{
					BaseDir:   baseDir,
					RemoteURL: setupURL,
				})
				if err != nil {
					return err
				}
				ok("Cloned:  %s", clrDim.Sprint(result.RepoDir))
			} else {
				// Fresh init.
				result, err := core.Init(core.InitOptions{BaseDir: baseDir})
				if err != nil {
					return err
				}
				ok("Repo:    %s", clrDim.Sprint(result.RepoDir))
			}

			ok("Profile %q ready.", name)
			fmt.Println()
			info("To use this profile:")
			fmt.Printf("  %s\n", clrBold.Sprintf("sysfig --profile %s track <path>", name))
			fmt.Printf("  %s\n", clrBold.Sprintf("sysfig --profile %s sync", name))
			fmt.Printf("\n  or set: %s\n", clrDim.Sprintf("export SYSFIG_PROFILE=%s", name))
			return nil
		},
	}
	cmd.Flags().StringVar(&setupURL, "from", "", "clone from remote git URL instead of creating empty repo")
	return cmd
}

func newProfileDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a named profile and all its data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if name == "default" {
				return fmt.Errorf("cannot delete the default profile")
			}

			baseDir := filepath.Join(profilesDir(), name)
			if _, err := os.Stat(baseDir); os.IsNotExist(err) {
				return fmt.Errorf("profile %q does not exist", name)
			}

			if !force {
				return fmt.Errorf(
					"this will permanently delete %s\nRe-run with --force to confirm", baseDir)
			}

			if err := os.RemoveAll(baseDir); err != nil {
				return fmt.Errorf("delete profile: %w", err)
			}
			ok("Deleted profile %q (%s)", name, baseDir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion (required)")
	return cmd
}

