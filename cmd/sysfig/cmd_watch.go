package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// ── watch ─────────────────────────────────────────────────────────────────────

// watchRun is the shared foreground-watcher logic used by both
// `sysfig watch` (bare) and `sysfig watch run`.
func watchRun(baseDir, sysRoot string, debounce time.Duration, dryRun, push bool) error {
	clrBold.Println("Watching tracked files for changes  (Ctrl-C to stop)")
	fmt.Printf("  %s %s\n", clrDim.Sprint("base-dir:"), clrDim.Sprint(baseDir))
	fmt.Printf("  %s %v\n", clrDim.Sprint("debounce:"), debounce)
	if push {
		fmt.Printf("  %s %s\n", clrDim.Sprint("push:"), clrOK.Sprint("enabled"))
	}
	divider()

	stop := make(chan struct{})

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println()
		info("Stopping watch.")
		close(stop)
	}()

	onEvent := func(path string, info core.ChangeInfo, result *core.SyncResult, err error) {
		ts := info.ChangedAt.Format("15:04:05")
		if dryRun {
			fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrInfo.Sprint("[dry-run]"), path)
			if actor := actorLine(info); actor != "" {
				fmt.Printf("            %s  %s\n", clrDim.Sprint("actor"), clrDim.Sprint(actor))
			}
			return
		}
		if err != nil {
			fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrErr.Sprint("error"), err)
			return
		}
		if result == nil || !result.Committed {
			return
		}
		fmt.Printf("  %s  %s  %s\n", clrDim.Sprint(ts), clrWarn.Sprint("changed"), path)
		if actor := actorLine(info); actor != "" {
			fmt.Printf("            %s  %s\n", clrDim.Sprint("actor"), clrDim.Sprint(actor))
		}
		if len(result.CommittedFiles) > 0 {
			for _, f := range result.CommittedFiles {
				fmt.Printf("            %s %s\n", clrOK.Sprint("committed"), clrDim.Sprint(f))
			}
		}
		if result.Message != "" {
			fmt.Printf("            %s\n", clrDim.Sprint(result.Message))
		}
	}

	return core.Watch(core.WatchOptions{
		BaseDir:  baseDir,
		SysRoot:  resolveSysRoot(sysRoot),
		Debounce: debounce,
		DryRun:   dryRun,
		Push:     push,
		OnEvent:  onEvent,
	}, stop)
}

func newWatchCmd() *cobra.Command {
	var (
		baseDir  string
		sysRoot  string
		debounce time.Duration
		dryRun   bool
		push     bool
	)

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Auto-sync tracked files when they change on disk",
		Long: `Watch monitors every tracked config file for changes and runs
'sysfig sync' automatically after a short debounce window.

Sub-commands:
  run       Run the watcher in the foreground (default)
  install   Install as a systemd user service
  uninstall Remove the systemd user service
  status    Show systemd service status

Press Ctrl-C to stop the foreground watcher.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return watchRun(baseDir, sysRoot, debounce, dryRun, push)
		},
	}

	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "wait this long after last change before syncing")
	f.BoolVar(&dryRun, "dry-run", false, "print detected changes without syncing")
	f.BoolVar(&push, "push", false, "push to remote after each successful sync")

	cmd.AddCommand(
		newWatchRunCmd(),
		newWatchInstallCmd(),
		newWatchUninstallCmd(),
		newWatchStatusCmd(),
	)
	return cmd
}

// ── watch run ─────────────────────────────────────────────────────────────────

func newWatchRunCmd() *cobra.Command {
	var (
		baseDir  string
		sysRoot  string
		debounce time.Duration
		dryRun   bool
		push     bool
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the file watcher in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			return watchRun(baseDir, sysRoot, debounce, dryRun, push)
		},
	}
	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.StringVar(&sysRoot, "sys-root", "", "prepend this path to all system paths (sandbox/testing override)")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "wait this long after last change before syncing")
	f.BoolVar(&dryRun, "dry-run", false, "print detected changes without syncing")
	f.BoolVar(&push, "push", false, "push to remote after each successful sync")
	return cmd
}

// ── watch install ─────────────────────────────────────────────────────────────

const watchServiceTemplate = `[Unit]
Description=sysfig config watcher — auto-sync tracked files
Documentation=https://github.com/aissat/sysfig
After=default.target

[Service]
Type=simple
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=%s watch --base-dir %s --debounce %s%s
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
`

func newWatchInstallCmd() *cobra.Command {
	var (
		baseDir  string
		debounce time.Duration
		enable   bool
		push     bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install sysfig-watch as a systemd user service",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			// Resolve binary path.
			binPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot resolve binary path: %w", err)
			}

			// Build service file content.
			extraFlags := ""
			if push {
				extraFlags = " --push"
			}
			content := fmt.Sprintf(watchServiceTemplate, binPath, baseDir, debounce, extraFlags)

			// Determine service file path: ~/.config/systemd/user/
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot resolve home directory: %w", err)
			}
			serviceDir := filepath.Join(homeDir, ".config", "systemd", "user")
			servicePath := filepath.Join(serviceDir, "sysfig-watch.service")

			if err := os.MkdirAll(serviceDir, 0o755); err != nil {
				return fmt.Errorf("create systemd user dir: %w", err)
			}
			if err := os.WriteFile(servicePath, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write service file: %w", err)
			}

			ok("Service file written: %s", clrDim.Sprint(servicePath))

			// Reload daemon so systemd picks up the new unit.
			if out, err := runSystemctl("--user", "daemon-reload"); err != nil {
				warn("daemon-reload failed: %s", out)
			} else {
				ok("systemctl --user daemon-reload")
			}

			if enable {
				if out, err := runSystemctl("--user", "enable", "--now", "sysfig-watch"); err != nil {
					return fmt.Errorf("enable service: %s: %w", out, err)
				}
				ok("Service enabled and started")
				fmt.Printf("  %s\n", clrDim.Sprint("systemctl --user status sysfig-watch"))
			} else {
				fmt.Println()
				info("To start the service now:")
				fmt.Printf("  %s\n", clrBold.Sprint("systemctl --user enable --now sysfig-watch"))
				fmt.Printf("  %s\n", clrBold.Sprint("sysfig watch status"))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&baseDir, "base-dir", "", "directory where sysfig stores its data")
	f.DurationVar(&debounce, "debounce", 2*time.Second, "debounce window written into the service file")
	f.BoolVar(&enable, "enable", false, "also run 'systemctl --user enable --now sysfig-watch'")
	f.BoolVar(&push, "push", false, "push to remote after each successful sync")
	return cmd
}

// ── watch uninstall ───────────────────────────────────────────────────────────

func newWatchUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the sysfig-watch systemd user service",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("cannot resolve home directory: %w", err)
			}
			servicePath := filepath.Join(homeDir, ".config", "systemd", "user", "sysfig-watch.service")

			// Stop + disable (best-effort).
			if out, err := runSystemctl("--user", "disable", "--now", "sysfig-watch"); err != nil {
				warn("disable failed (service may not be running): %s", out)
			} else {
				ok("Service stopped and disabled")
			}

			if err := os.Remove(servicePath); err != nil {
				if os.IsNotExist(err) {
					info("Service file not found — nothing to remove.")
					return nil
				}
				return fmt.Errorf("remove service file: %w", err)
			}
			ok("Removed: %s", clrDim.Sprint(servicePath))

			if _, err := runSystemctl("--user", "daemon-reload"); err == nil {
				ok("systemctl --user daemon-reload")
			}
			return nil
		},
	}
	return cmd
}

// ── watch status ──────────────────────────────────────────────────────────────

func newWatchStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the systemd service status for sysfig-watch",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := runSystemctl("--user", "status", "sysfig-watch")
			fmt.Print(out)
			return err
		},
	}
	return cmd
}

// runSystemctl runs a systemctl command and returns combined output + error.
func runSystemctl(args ...string) (string, error) {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// actorLine formats a one-line process attribution string for display.
// Returns "" when no attribution is available (Source == ChangeSourceUnknown).
// Prefix "maybe" and suffix "best-effort" when attribution is not reliable,
// so the user can calibrate trust accordingly.
func actorLine(info core.ChangeInfo) string {
	if info.Source == core.ChangeSourceUnknown {
		return ""
	}
	var parts []string
	if info.ProcName != "" {
		name := info.ProcName
		if !info.Reliable {
			name = "maybe " + name
		}
		parts = append(parts, name)
	}
	if info.PID != 0 {
		parts = append(parts, fmt.Sprintf("pid %d", info.PID))
	}
	if info.UserName != "" {
		parts = append(parts, "user "+info.UserName)
	} else if info.UID >= 0 {
		parts = append(parts, fmt.Sprintf("uid %d", info.UID))
	}
	if info.Reliable {
		parts = append(parts, "exact")
	} else {
		parts = append(parts, "best-effort")
	}
	return strings.Join(parts, " · ")
}

