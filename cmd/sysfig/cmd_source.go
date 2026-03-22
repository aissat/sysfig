package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// ── source command ────────────────────────────────────────────────────────────

func newSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Manage Config Source template catalogs",
		Long: `Config Sources let you consume shared config templates from a remote bundle,
inject per-machine variables, and commit the rendered output as ordinary tracked files.

Workflow:
  sysfig source add corporate bundle+local:///mnt/nfs/corp-configs.bundle
  sysfig source list corporate
  sysfig source use corporate/system-proxy
  sysfig source render
  sysfig diff && sysfig apply`,
	}
	cmd.AddCommand(
		newSourceAddCmd(),
		newSourceListCmd(),
		newSourceUseCmd(),
		newSourceRenderCmd(),
		newSourcePullCmd(),
	)
	return cmd
}

func newSourceAddCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Register a new config source bundle",
		Args:  cobra.ExactArgs(2),
		Example: `  sysfig source add corporate bundle+local:///mnt/corp-nfs/corp-configs.bundle
  sysfig source add community bundle+ssh://backup@fileserver/srv/sysfig/community.bundle`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			name, sourceURL := args[0], args[1]
			if err := core.SourceAdd(baseDir, name, sourceURL); err != nil {
				return err
			}
			ok("Source %q registered", name)
			info("Run 'sysfig source list %s' to see available profiles.", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newSourceListCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "list <source>",
		Short: "List profiles available in a source bundle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			sourceName := args[0]
			entries, err := core.SourceList(baseDir, sourceName)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				info("No profiles found in source %q", sourceName)
				return nil
			}
			divider()
			fmt.Printf("  %-28s  %-5s  %s\n",
				clrBold.Sprint("PROFILE"), clrBold.Sprint("FILES"), clrBold.Sprint("DESCRIPTION"))
			divider()
			for _, e := range entries {
				fmt.Printf("  %-28s  %-5d  %s\n", e.Name, e.Files, e.Description)
			}
			divider()
			fmt.Printf("\n  To activate a profile: sysfig source use %s/<profile>\n\n", sourceName)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newSourceUseCmd() *cobra.Command {
	var baseDir string
	var varFlags []string
	var valuesFile string
	cmd := &cobra.Command{
		Use:   "use <source/profile>",
		Short: "Activate a profile with per-machine variables",
		Long: `Activate a profile from a registered source.

Supply variables with --values values.yaml (a flat YAML map of key: value
pairs) and/or --var key=value flags. --var takes precedence over --values.
Any variable not supplied is prompted interactively when stdin is a TTY, or
read line-by-line in alphabetical order when piped.

After activation, run 'sysfig source render' to commit the rendered files.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			sourceProfile := args[0]

			parts := strings.SplitN(sourceProfile, "/", 2)

			if len(parts) != 2 {
				return fmt.Errorf("invalid source reference %q — expected <source>/<profile>", sourceProfile)
			}
			sourceName, profileName := parts[0], parts[1]

			// Ensure source is pulled so we can read profile.yaml.
			if err := core.SourcePull(baseDir, sourceName); err != nil {
				return fmt.Errorf("pull source %q: %w", sourceName, err)
			}

			profile, err := core.ReadProfileYAML(baseDir, sourceName, profileName)
			if err != nil {
				return err
			}

			if valuesFile != "" && len(varFlags) > 0 {
				return fmt.Errorf("--values and --var are mutually exclusive — use one or the other")
			}

			// Seed variables from --values file or --var flags.
			variables := make(map[string]string, len(profile.Variables))
			if valuesFile != "" {
				data, err := os.ReadFile(valuesFile)
				if err != nil {
					return fmt.Errorf("--values: %w", err)
				}
				var fileVars map[string]string
				if err := yaml.Unmarshal(data, &fileVars); err != nil {
					return fmt.Errorf("--values %q: %w", valuesFile, err)
				}
				for k, v := range fileVars {
					variables[k] = v
				}
			}

			// --var flags override values from file.
			for _, kv := range varFlags {
				idx := strings.IndexByte(kv, '=')
				if idx < 1 {
					return fmt.Errorf("--var %q: expected key=value", kv)
				}
				variables[kv[:idx]] = kv[idx+1:]
			}

			// Collect variables still needed (not provided via --var).
			varNames := make([]string, 0, len(profile.Variables))
			for k := range profile.Variables {
				varNames = append(varNames, k)
			}
			sort.Strings(varNames)

			// Build the list of variables that still need a value.
			var needPrompt []string
			for _, varName := range varNames {
				if _, provided := variables[varName]; !provided {
					needPrompt = append(needPrompt, varName)
				}
			}

			// Prompt / read from stdin only for variables not already set.
			if len(needPrompt) > 0 {
				scanner := bufio.NewScanner(os.Stdin)
				isTTY := term.IsTerminal(int(os.Stdin.Fd()))

				for _, varName := range needPrompt {
					decl := profile.Variables[varName]
					prompt := "  " + varName
					if decl.Required {
						prompt += " (required)"
					}
					if decl.Default != "" {
						prompt += " [" + decl.Default + "]"
					}
					prompt += ": "

					if isTTY {
						fmt.Print(prompt)
					}
					if scanner.Scan() {
						val := strings.TrimSpace(scanner.Text())
						if val == "" && decl.Default != "" {
							val = decl.Default
						}
						if val == "" && decl.Required {
							return fmt.Errorf("variable %q is required", varName)
						}
						if val != "" {
							variables[varName] = val
						}
					}
				}
			}

			if err := core.SourceUse(core.SourceUseOptions{
				BaseDir:       baseDir,
				SourceProfile: sourceProfile,
				Variables:     variables,
			}); err != nil {
				return err
			}

			ok("Profile %q added to sources.yaml", sourceProfile)
			info("Run 'sysfig source render' to commit the rendered files.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "set a variable (key=value, repeatable)")
	cmd.Flags().StringVar(&valuesFile, "values", "", "YAML file of variable values (key: value)")
	return cmd
}

func newSourceRenderCmd() *cobra.Command {
	var baseDir string
	var force, dryRun bool
	var renderProfile string
	var valuesFile string
	var varFlags []string
	cmd := &cobra.Command{
		Use:   "render [--profile <source/profile>] [--values values.yaml] [--var key=value] [--force] [--dry-run]",
		Short: "Render activated profiles into the local repo",
		Long: `Render all activated profiles (or one specific profile) by:
  1. Fetching each source bundle into the local cache
  2. Reading profile.yaml and validating variables
  3. Rendering each template and committing to track/* branches
  4. Updating state.json with source ownership

--values values.yaml sets variables for multiple profiles at once (mutually
exclusive with --var):

  corp/system-proxy:
    proxy_url: http://proxy.corp.com:3128
    bypass_list: 10.0.0.0/8,localhost
  corp/dns-resolvers:
    primary_dns: 10.0.0.53

--var sets variables inline for a single profile (mutually exclusive with
--values). Format: key=value or source/profile:key=value.

After rendering, run 'sysfig diff' and 'sysfig apply' to write files to disk.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)

			if valuesFile != "" && len(varFlags) > 0 {
				return fmt.Errorf("--values and --var are mutually exclusive — use one or the other")
			}

			// Parse --var flags into per-profile variable maps.
			// Format: "corp/system-proxy:proxy_url=value" or "proxy_url=value" (global).
			profileOverrides := map[string]map[string]string{}
			globalOverrides := map[string]string{}
			for _, kv := range varFlags {
				// Check for scoped format: contains "/" before ":"
				colonIdx := strings.IndexByte(kv, ':')
				eqIdx := strings.IndexByte(kv, '=')
				if eqIdx < 1 {
					return fmt.Errorf("--var %q: expected key=value or profile:key=value", kv)
				}
				if colonIdx > 0 && colonIdx < eqIdx {
					// Scoped: "corp/system-proxy:proxy_url=value"
					profile := kv[:colonIdx]
					rest := kv[colonIdx+1:]
					eqIdx2 := strings.IndexByte(rest, '=')
					if eqIdx2 < 1 {
						return fmt.Errorf("--var %q: expected profile:key=value", kv)
					}
					if profileOverrides[profile] == nil {
						profileOverrides[profile] = map[string]string{}
					}
					profileOverrides[profile][rest[:eqIdx2]] = rest[eqIdx2+1:]
				} else {
					// Global: "proxy_url=value"
					globalOverrides[kv[:eqIdx]] = kv[eqIdx+1:]
				}
			}

			// If --values supplied, build per-profile variable maps, then apply overrides.
			if valuesFile != "" {
				data, err := os.ReadFile(valuesFile)
				if err != nil {
					return fmt.Errorf("--values: %w", err)
				}
				var multiVars map[string]map[string]string
				if err := yaml.Unmarshal(data, &multiVars); err != nil {
					return fmt.Errorf("--values %q: %w", valuesFile, err)
				}
				// Merge global and scoped --var overrides into the file values.
				for profile, vars := range multiVars {
					if vars == nil {
						vars = map[string]string{}
						multiVars[profile] = vars
					}
					for k, v := range globalOverrides {
						vars[k] = v
					}
					for k, v := range profileOverrides[profile] {
						vars[k] = v
					}
				}
				// Activate profiles in sorted order for deterministic output.
				profiles := make([]string, 0, len(multiVars))
				for p := range multiVars {
					profiles = append(profiles, p)
				}
				sort.Strings(profiles)
				for _, sourceProfile := range profiles {
					vars := multiVars[sourceProfile]
					if err := core.SourceUse(core.SourceUseOptions{
						BaseDir:       baseDir,
						SourceProfile: sourceProfile,
						Variables:     vars,
					}); err != nil {
						return fmt.Errorf("values: activate %q: %w", sourceProfile, err)
					}
					ok("Activated: %s", sourceProfile)
				}
				if len(profiles) > 0 {
					fmt.Println()
				}
			}

			result, err := core.SourceRender(core.RenderOptions{
				BaseDir: baseDir,
				Profile: renderProfile,
				Force:   force,
				DryRun:  dryRun,
			})

			if result != nil {
				for _, p := range result.Rendered {
					if dryRun {
						info("[dry-run] Would render: %s", p)
					} else {
						ok("Rendered: %s", p)
					}
				}
				for _, p := range result.Skipped {
					clrDim.Printf("  · Unchanged: %s\n", p)
				}
				for _, c := range result.Conflicts {
					fail("Conflict: %s", c.SystemPath)
					fmt.Printf("      currently owned by: %s\n", c.CurrentOwner)
					fmt.Printf("      requested by:        %s\n", c.RequestedBy)
				}
				if len(result.Conflicts) > 0 {
					fmt.Println()
					warn("Re-run with --force to transfer ownership.")
				}
				for _, hookErr := range result.HookErrors {
					warn("Hook error (non-fatal): %v", hookErr)
				}
			}

			if err != nil {
				return err
			}

			if result != nil && len(result.Rendered) > 0 && !dryRun {
				fmt.Println()
				info("Run 'sysfig diff' to review, then 'sysfig apply' to write files to disk.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	cmd.Flags().StringVar(&renderProfile, "profile", "", "limit render to one profile (e.g. corporate/system-proxy)")
	cmd.Flags().StringVar(&valuesFile, "values", "", "YAML file with per-profile variables (profile: {key: value})")
	cmd.Flags().StringArrayVar(&varFlags, "var", nil, "override a variable: key=value or profile:key=value (repeatable)")
	cmd.Flags().BoolVar(&force, "force", false, "transfer ownership of conflicting files")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be rendered without writing")
	return cmd
}

func newSourcePullCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "pull <source>",
		Short: "Fetch the latest source bundle into the local cache",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			name := args[0]
			if err := core.SourcePull(baseDir, name); err != nil {
				return err
			}
			ok("Source %q updated", name)
			info("Run 'sysfig source render' to re-render with the updated templates.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

