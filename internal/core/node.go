package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// NodeAddOptions configures a node add operation.
type NodeAddOptions struct {
	BaseDir   string
	Name      string
	PublicKey string            // age X25519 public key string (age1…)
	Variables map[string]string // optional per-node template variables
}

// NodeAddResult reports what happened.
type NodeAddResult struct {
	Node *types.Node
}

// NodeAdd registers a new remote node with its age public key.
// Returns an error if a node with that name already exists or the key is invalid.
func NodeAdd(opts NodeAddOptions) (*NodeAddResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: node add: BaseDir must not be empty")
	}
	if opts.Name == "" {
		return nil, fmt.Errorf("core: node add: Name must not be empty")
	}
	if strings.ContainsAny(opts.Name, " /\\") {
		return nil, fmt.Errorf("core: node add: Name must not contain spaces or slashes")
	}

	// Validate the public key by parsing it.
	if _, err := age.ParseX25519Recipient(opts.PublicKey); err != nil {
		return nil, fmt.Errorf("core: node add: invalid age public key: %w", err)
	}

	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	var added *types.Node
	if err := sm.WithLock(func(s *types.State) error {
		if s.Nodes == nil {
			s.Nodes = make(map[string]*types.Node)
		}
		if _, exists := s.Nodes[opts.Name]; exists {
			return fmt.Errorf("core: node add: node %q already exists", opts.Name)
		}
		node := &types.Node{
			Name:      opts.Name,
			PublicKey: opts.PublicKey,
			Variables: opts.Variables,
			AddedAt:   time.Now(),
		}
		s.Nodes[opts.Name] = node
		added = node
		return nil
	}); err != nil {
		return nil, err
	}

	return &NodeAddResult{Node: added}, nil
}

// NodeListOptions configures a node list operation.
type NodeListOptions struct {
	BaseDir string
}

// NodeList returns all registered nodes sorted by name.
func NodeList(opts NodeListOptions) ([]*types.Node, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: node list: BaseDir must not be empty")
	}
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	s, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: node list: %w", err)
	}

	nodes := make([]*types.Node, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

// NodeRemoveOptions configures a node remove operation.
type NodeRemoveOptions struct {
	BaseDir string
	Name    string
}

// NodeRemove deletes a node by name. Returns an error if the node does not exist.
func NodeRemove(opts NodeRemoveOptions) error {
	if opts.BaseDir == "" {
		return fmt.Errorf("core: node remove: BaseDir must not be empty")
	}
	if opts.Name == "" {
		return fmt.Errorf("core: node remove: Name must not be empty")
	}
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	return sm.WithLock(func(s *types.State) error {
		if s.Nodes == nil || s.Nodes[opts.Name] == nil {
			return fmt.Errorf("core: node remove: node %q not found", opts.Name)
		}
		delete(s.Nodes, opts.Name)
		return nil
	})
}

// NodeRecipients parses all registered nodes' public keys into age.Recipient
// values. Invalid keys are skipped with a warning returned per entry.
// Used by sync to build the multi-recipient list.
func NodeRecipients(nodes map[string]*types.Node) ([]age.Recipient, []string) {
	recipients := make([]age.Recipient, 0, len(nodes))
	var warnings []string
	for name, n := range nodes {
		r, err := age.ParseX25519Recipient(n.PublicKey)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("node %q: invalid public key: %v", name, err))
			continue
		}
		recipients = append(recipients, r)
	}
	return recipients, warnings
}
