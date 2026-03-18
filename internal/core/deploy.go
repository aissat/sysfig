package core

import (
	"fmt"
	"os"
	"path/filepath"
)

// DeployOptions configures a sysfig deploy operation.
type DeployOptions struct {
	// RemoteURL is the git remote to clone from (SSH or HTTPS).
	// Required when the bare repo does not exist yet; ignored (no pull) when
	// --no-pull is set and the repo already exists.
	RemoteURL string

	// BaseDir is where sysfig stores its data (default: ~/.sysfig).
	BaseDir string

	// IDs limits apply to specific tracked IDs. Empty = apply all.
	IDs []string

	// DryRun prints what would happen without writing anything.
	DryRun bool

	// NoBackup skips the pre-apply backup step.
	NoBackup bool

	// SkipEncrypted silently skips encrypted files when no master key is present.
	SkipEncrypted bool

	// NoPull skips the pull step even if the repo already exists.
	// Useful when offline: deploy from local repo without touching the network.
	NoPull bool

	// Yes skips interactive confirmation prompts.
	Yes bool

	// SysRoot is prepended to all system paths (sandbox/testing override).
	SysRoot string
}

// DeployPhase describes which high-level step Deploy is reporting on.
type DeployPhase string

const (
	DeployPhaseSetup   DeployPhase = "setup"   // first-time clone
	DeployPhasePull    DeployPhase = "pull"     // pull on existing repo
	DeployPhaseApply   DeployPhase = "apply"    // applying files
	DeployPhaseSkipped DeployPhase = "skipped"  // pull skipped (--no-pull or offline)
)

// DeployResult is the full outcome of a Deploy call.
type DeployResult struct {
	Phase DeployPhase

	// Setup fields (populated when Phase == DeployPhaseSetup)
	CloneResult *CloneResult

	// Pull fields (populated when Phase == DeployPhasePull)
	PullResult *PullResult

	// Apply results — always populated (may be empty if nothing to apply)
	ApplyResults []ApplyResult

	// Applied is the count of files successfully applied.
	Applied int

	// Skipped is the count of files skipped (DryRun or already synced).
	Skipped int
}

// Deploy is the "one command to rule them all" entry point.
//
// Behaviour:
//
//	First-time machine (repo.git does not exist):
//	  1. Clone the remote repo (like `sysfig setup`)
//	  2. Seed state.json from the sysfig.yaml manifest
//	  3. Apply all tracked files immediately
//
//	Already set up machine (repo.git exists):
//	  1. Pull from remote (unless --no-pull)
//	  2. Apply all tracked files (or only those that are PENDING/MISSING)
//
// Deploy is idempotent: running it twice leaves the machine in the same state.
func Deploy(opts DeployOptions) (*DeployResult, error) {
	if opts.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("core: deploy: resolve home dir: %w", err)
		}
		opts.BaseDir = filepath.Join(home, ".sysfig")
	}

	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	result := &DeployResult{}

	alreadySetUp := isGitRepo(repoDir)

	if !alreadySetUp {
		// ── First-time bootstrap ────────────────────────────────────────────
		if opts.RemoteURL == "" {
			return nil, fmt.Errorf("core: deploy: remote URL is required on first run (repo not found at %q)", repoDir)
		}

		cloneResult, err := Clone(CloneOptions{
			RemoteURL:     opts.RemoteURL,
			BaseDir:       opts.BaseDir,
			SkipEncrypted: opts.SkipEncrypted,
			Yes:           opts.Yes,
		})
		if err != nil {
			return nil, fmt.Errorf("core: deploy: setup: %w", err)
		}
		result.Phase = DeployPhaseSetup
		result.CloneResult = cloneResult

	} else {
		// ── Already set up — optionally pull ───────────────────────────────
		if opts.NoPull {
			result.Phase = DeployPhaseSkipped
		} else {
			pullResult, err := Pull(PullOptions{BaseDir: opts.BaseDir})
			if err != nil {
				// Network failure is non-fatal when the repo already exists.
				// We can still apply from the local repo.
				result.Phase = DeployPhaseSkipped
			} else {
				result.Phase = DeployPhasePull
				result.PullResult = pullResult
			}
		}
	}

	// ── Apply ───────────────────────────────────────────────────────────────
	applyResults, err := Apply(ApplyOptions{
		BaseDir:  opts.BaseDir,
		IDs:      opts.IDs,
		DryRun:   opts.DryRun,
		NoBackup: opts.NoBackup,
		SysRoot:  opts.SysRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("core: deploy: apply: %w", err)
	}

	result.ApplyResults = applyResults
	for _, r := range applyResults {
		if r.Skipped {
			result.Skipped++
		} else {
			result.Applied++
		}
	}

	return result, nil
}
