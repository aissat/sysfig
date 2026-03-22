package main

import (
	"fmt"

	"github.com/aissat/sysfig/internal/core"
	"github.com/spf13/cobra"
)

// ── node ─────────────────────────────────────────────────────────────────────

func newNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node <subcommand>",
		Short: "Manage remote nodes (multi-recipient encryption)",
		Long: `Nodes represent remote machines that should be able to decrypt
encrypted config files. Each node is identified by a name and an age X25519
public key. When you run 'sysfig sync', every encrypted file is re-encrypted
to your local master key AND all registered node public keys — so each machine
can decrypt its own copy independently.

Examples:
  sysfig node add laptop age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
  sysfig node list
  sysfig node remove laptop`,
	}
	cmd.AddCommand(newNodeAddCmd(), newNodeListCmd(), newNodeRemoveCmd())
	return cmd
}

func newNodeAddCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "add <name> <age-public-key>",
		Short: "Register a remote node by name and age public key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			fixSudoOwnership(baseDir)
			name, pubkey := args[0], args[1]
			result, err := core.NodeAdd(core.NodeAddOptions{
				BaseDir:   baseDir,
				Name:      name,
				PublicKey: pubkey,
			})
			if err != nil {
				return err
			}
			ok("Node %q registered", result.Node.Name)
			info("Public key: %s", clrDim.Sprint(result.Node.PublicKey))
			info("Re-run 'sysfig sync' to re-encrypt tracked files for this node.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newNodeListCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered nodes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			fixSudoOwnership(baseDir)
			nodes, err := core.NodeList(core.NodeListOptions{BaseDir: baseDir})
			if err != nil {
				return err
			}
			if len(nodes) == 0 {
				info("No nodes registered. Use 'sysfig node add <name> <age-pubkey>'.")
				return nil
			}
			nameW := len("NAME")
			keyW := len("PUBLIC KEY")
			for _, n := range nodes {
				if len(n.Name) > nameW {
					nameW = len(n.Name)
				}
				if len(n.PublicKey) > keyW {
					keyW = len(n.PublicKey)
				}
			}
			nameW += 2
			keyW += 2

			divider()
			fmt.Printf("  %s  %s  %s\n",
				clrBold.Sprint(pad("NAME", nameW)),
				clrBold.Sprint(pad("PUBLIC KEY", keyW)),
				clrBold.Sprint("ADDED"))
			divider()
			for _, n := range nodes {
				fmt.Printf("  %s  %s  %s\n",
					pad(n.Name, nameW),
					clrDim.Sprint(pad(n.PublicKey, keyW)),
					n.AddedAt.Format("2006-01-02"))
			}
			divider()
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

func newNodeRemoveCmd() *cobra.Command {
	var baseDir string
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir = resolveBaseDir(baseDir)
			fixSudoOwnership(baseDir)
			if err := core.NodeRemove(core.NodeRemoveOptions{
				BaseDir: baseDir,
				Name:    args[0],
			}); err != nil {
				return err
			}
			ok("Node %q removed", args[0])
			info("Re-run 'sysfig sync' to re-encrypt tracked files without this node.")
			return nil
		},
	}
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "sysfig data directory")
	return cmd
}

