package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
	"gopkg.in/yaml.v3"
)

// Severity classifies how serious a doctor finding is.
type Severity string

const (
	SeverityOK   Severity = "ok"
	SeverityWarn Severity = "warn"
	SeverityFail Severity = "fail"
	SeverityInfo Severity = "info"
)

// DoctorFinding is a single check result.
type DoctorFinding struct {
	Category string
	Label    string
	Severity Severity
	Detail   string // extra context shown below the finding line
	Hint     string // what to do to fix it
}

// DoctorResult is the full report returned by Doctor.
type DoctorResult struct {
	Findings []DoctorFinding
	OK       int
	Warn     int
	Fail     int
}

// DoctorOptions configures a doctor run.
type DoctorOptions struct {
	BaseDir string // defaults to ~/.sysfig
	Network bool   // if true, also probe the configured remote
}

// Doctor runs all health checks and returns a DoctorResult.
// It never returns an error — every failure becomes a DoctorFinding so the
// caller always gets a complete picture.
func Doctor(opts DoctorOptions) *DoctorResult {
	if opts.BaseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		opts.BaseDir = filepath.Join(home, ".sysfig")
	}

	r := &DoctorResult{}
	add := func(f DoctorFinding) {
		r.Findings = append(r.Findings, f)
		switch f.Severity {
		case SeverityOK:
			r.OK++
		case SeverityWarn:
			r.Warn++
		case SeverityFail:
			r.Fail++
		}
	}

	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	keysDir := filepath.Join(opts.BaseDir, "keys")
	stateFile := filepath.Join(opts.BaseDir, "state.json")

	// ── 1. Prerequisites ────────────────────────────────────────────────────
	gitPath, gitVersion := doctorCheckGit()
	if gitPath == "" {
		add(DoctorFinding{
			Category: "prerequisites",
			Label:    "git binary",
			Severity: SeverityFail,
			Detail:   "git not found on $PATH",
			Hint:     "Install git (e.g. sudo pacman -S git)",
		})
	} else {
		add(DoctorFinding{
			Category: "prerequisites",
			Label:    "git binary",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%s  (%s)", gitPath, gitVersion),
		})
	}

	diffPath, _ := exec.LookPath("diff")
	if diffPath == "" {
		add(DoctorFinding{
			Category: "prerequisites",
			Label:    "diff binary",
			Severity: SeverityWarn,
			Detail:   "not found on $PATH — sysfig diff will not work",
			Hint:     "Install diffutils",
		})
	} else {
		add(DoctorFinding{
			Category: "prerequisites",
			Label:    "diff binary",
			Severity: SeverityOK,
			Detail:   diffPath,
		})
	}

	// ── 2. Base directory ───────────────────────────────────────────────────
	baseFi, err := os.Stat(opts.BaseDir)
	if err != nil {
		add(DoctorFinding{
			Category: "base directory",
			Label:    "exists",
			Severity: SeverityFail,
			Detail:   opts.BaseDir,
			Hint:     "Run: sysfig init",
		})
		return r // nothing else makes sense without a base dir
	}
	add(DoctorFinding{
		Category: "base directory",
		Label:    "exists",
		Severity: SeverityOK,
		Detail:   opts.BaseDir,
	})

	if perm := baseFi.Mode().Perm(); perm&0o077 != 0 {
		add(DoctorFinding{
			Category: "base directory",
			Label:    "permissions",
			Severity: SeverityWarn,
			Detail:   fmt.Sprintf("%04o — other users can read your configs (expected 0700)", perm),
			Hint:     fmt.Sprintf("chmod 0700 %s", opts.BaseDir),
		})
	} else {
		add(DoctorFinding{
			Category: "base directory",
			Label:    "permissions",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%04o", baseFi.Mode().Perm()),
		})
	}

	// ── 3. Bare git repo ────────────────────────────────────────────────────
	repoOK := gitBareExists(repoDir)
	if !repoOK {
		add(DoctorFinding{
			Category: "git repo",
			Label:    "repo exists",
			Severity: SeverityFail,
			Detail:   repoDir,
			Hint:     "Run: sysfig init  OR  sysfig setup <remote-url>",
		})
	} else {
		add(DoctorFinding{
			Category: "git repo",
			Label:    "repo exists",
			Severity: SeverityOK,
			Detail:   repoDir,
		})

		// HEAD resolvable?
		if headSHA := doctorHeadSHA(repoDir); headSHA == "" {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "HEAD resolves",
				Severity: SeverityWarn,
				Detail:   "no commits yet",
				Hint:     "Run: sysfig track <file> && sysfig sync",
			})
		} else {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "HEAD resolves",
				Severity: SeverityOK,
				Detail:   headSHA,
			})
		}

		// Uncommitted staged changes?
		if !isNothingToCommitBare(repoDir) {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "uncommitted changes",
				Severity: SeverityWarn,
				Detail:   "staged changes exist that have not been committed",
				Hint:     "Run: sysfig sync",
			})
		} else {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "uncommitted changes",
				Severity: SeverityOK,
				Detail:   "index is clean",
			})
		}

		// Remote configured?
		remoteName, remoteURL := doctorRemote(repoDir)
		if remoteName == "" {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "remote configured",
				Severity: SeverityWarn,
				Detail:   "no remote — push/pull will not work",
				Hint:     "git --git-dir ~/.sysfig/repo.git remote add origin <url>",
			})
		} else {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "remote configured",
				Severity: SeverityOK,
				Detail:   fmt.Sprintf("%s → %s", remoteName, remoteURL),
			})

			if opts.Network {
				if err := doctorLsRemote(repoDir, remoteURL); err != nil {
					add(DoctorFinding{
						Category: "git repo",
						Label:    "remote reachable",
						Severity: SeverityFail,
						Detail:   err.Error(),
						Hint:     "Check your network connection and SSH / HTTPS credentials",
					})
				} else {
					add(DoctorFinding{
						Category: "git repo",
						Label:    "remote reachable",
						Severity: SeverityOK,
						Detail:   remoteURL,
					})
				}
			}
		}

		// sysfig.yaml committed in manifest branch?
		_, manifestErr := gitShowBytesAt(repoDir, "manifest", "sysfig.yaml")
		if manifestErr != nil {
			// fallback: check HEAD for pre-branch-per-track repos
			_, manifestErr = gitShowBytes(repoDir, "sysfig.yaml")
		}
		if manifestErr != nil {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "sysfig.yaml manifest",
				Severity: SeverityWarn,
				Detail:   "manifest not committed yet",
				Hint:     "Run: sysfig sync --auto",
			})
		} else {
			add(DoctorFinding{
				Category: "git repo",
				Label:    "sysfig.yaml manifest",
				Severity: SeverityOK,
			})
		}
	}

	// ── 4. State file ───────────────────────────────────────────────────────
	sm := state.NewManager(stateFile)
	st, stErr := sm.Load()
	if stErr != nil {
		add(DoctorFinding{
			Category: "state",
			Label:    "state.json readable",
			Severity: SeverityFail,
			Detail:   stErr.Error(),
			Hint:     "Delete state.json and run: sysfig setup",
		})
	} else {
		add(DoctorFinding{
			Category: "state",
			Label:    "state.json readable",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("%d tracked file(s)", len(st.Files)),
		})

		if repoOK && len(st.Files) > 0 {
			// Cross-check state IDs vs manifest (manifest branch first, HEAD fallback).
			manifestData, err := gitShowBytesAt(repoDir, "manifest", "sysfig.yaml")
			if err != nil {
				manifestData, err = gitShowBytes(repoDir, "sysfig.yaml")
			}
			if err == nil {
				doctorCheckManifestSync(add, st, manifestData)
			}
			// Check each file's health (blob exists, system file exists).
			doctorCheckFileHealth(add, st, repoDir)
		}
	}

	// ── 5. Encryption key ───────────────────────────────────────────────────
	encryptedCount := 0
	if st != nil {
		for _, rec := range st.Files {
			if rec.Encrypt {
				encryptedCount++
			}
		}
	}

	keyPath := filepath.Join(keysDir, "master.key")
	keyFi, keyStatErr := os.Stat(keyPath)
	switch {
	case keyStatErr == nil && keyFi.Mode().Perm()&0o077 != 0:
		add(DoctorFinding{
			Category: "encryption",
			Label:    "master key permissions",
			Severity: SeverityWarn,
			Detail:   fmt.Sprintf("%04o (expected 0600)", keyFi.Mode().Perm()),
			Hint:     fmt.Sprintf("chmod 0600 %s", keyPath),
		})
	case keyStatErr == nil:
		detail := "present"
		if encryptedCount > 0 {
			detail = fmt.Sprintf("present — covers %d encrypted file(s)", encryptedCount)
		}
		add(DoctorFinding{
			Category: "encryption",
			Label:    "master key",
			Severity: SeverityOK,
			Detail:   detail,
		})
	case encryptedCount > 0:
		add(DoctorFinding{
			Category: "encryption",
			Label:    "master key",
			Severity: SeverityFail,
			Detail:   fmt.Sprintf("not found — %d encrypted file(s) cannot be applied", encryptedCount),
			Hint:     "Copy your master key to " + keyPath,
		})
	default:
		add(DoctorFinding{
			Category: "encryption",
			Label:    "master key",
			Severity: SeverityInfo,
			Detail:   "not present (no encrypted files tracked)",
		})
	}

	return r
}

// ── private helpers ──────────────────────────────────────────────────────────

func doctorCheckGit() (path, version string) {
	p, err := exec.LookPath("git")
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return p, "version unknown"
	}
	return p, string(bytes.TrimSpace(out))
}

func doctorHeadSHA(repoDir string) string {
	out, err := gitBareOutput(repoDir, 5*time.Second, nil, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	sha := string(bytes.TrimSpace(out))
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func doctorRemote(repoDir string) (name, url string) {
	out, err := gitBareOutput(repoDir, 5*time.Second, nil, "remote", "-v")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return "", ""
	}
	// First line: "origin\tgit@github.com:you/repo (fetch)"
	line := bytes.SplitN(out, []byte("\n"), 2)[0]
	fields := bytes.Fields(line)
	if len(fields) < 2 {
		return "", ""
	}
	return string(fields[0]), string(fields[1])
}

func doctorLsRemote(repoDir, url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", url, "HEAD")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", bytes.TrimSpace(buf.Bytes()))
	}
	return nil
}

// minManifest is the minimal shape of sysfig.yaml needed for the ID cross-check.
type minManifest struct {
	TrackedFiles []struct {
		ID string `yaml:"id"`
	} `yaml:"tracked_files"`
}

func doctorCheckManifestSync(add func(DoctorFinding), st *types.State, manifestData []byte) {
	var mf minManifest
	if err := yaml.Unmarshal(manifestData, &mf); err != nil {
		add(DoctorFinding{
			Category: "state",
			Label:    "manifest parse",
			Severity: SeverityWarn,
			Detail:   "could not parse sysfig.yaml: " + err.Error(),
		})
		return
	}

	manifestIDs := make(map[string]bool, len(mf.TrackedFiles))
	for _, e := range mf.TrackedFiles {
		if e.ID != "" {
			manifestIDs[e.ID] = true
		}
	}

	// IDs in state.json that are not in the committed manifest.
	var orphaned []string
	for id := range st.Files {
		if !manifestIDs[id] {
			orphaned = append(orphaned, id)
		}
	}
	if len(orphaned) > 0 {
		add(DoctorFinding{
			Category: "state",
			Label:    "state/manifest sync",
			Severity: SeverityWarn,
			Detail:   fmt.Sprintf("%d ID(s) in state.json not committed to sysfig.yaml: %v", len(orphaned), orphaned),
			Hint:     "Run: sysfig sync  to commit the manifest",
		})
	} else {
		add(DoctorFinding{
			Category: "state",
			Label:    "state/manifest sync",
			Severity: SeverityOK,
			Detail:   "state.json and sysfig.yaml are in sync",
		})
	}
}

func doctorCheckFileHealth(add func(DoctorFinding), st *types.State, repoDir string) {
	missingSystem := 0
	missingBlob := 0

	for _, rec := range st.Files {
		if _, err := os.Stat(rec.SystemPath); os.IsNotExist(err) {
			missingSystem++
		}
		if rec.RepoPath != "" {
			branch := rec.Branch
			if branch == "" {
				branch = "track/" + SanitizeBranchName(rec.RepoPath)
			}
			if _, err := gitShowBytesAt(repoDir, branch, rec.RepoPath); err != nil {
				missingBlob++
			}
		}
	}

	total := len(st.Files)

	if missingSystem > 0 {
		add(DoctorFinding{
			Category: "file health",
			Label:    "system files present",
			Severity: SeverityWarn,
			Detail:   fmt.Sprintf("%d of %d tracked file(s) not on disk", missingSystem, total),
			Hint:     "Run: sysfig apply",
		})
	} else {
		add(DoctorFinding{
			Category: "file health",
			Label:    "system files present",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("all %d file(s) exist on disk", total),
		})
	}

	if missingBlob > 0 {
		add(DoctorFinding{
			Category: "file health",
			Label:    "repo blobs in track branches",
			Severity: SeverityWarn,
			Detail:   fmt.Sprintf("%d of %d tracked file(s) have no blob in repo HEAD", missingBlob, total),
			Hint:     "Run: sysfig sync  to commit staged changes",
		})
	} else {
		add(DoctorFinding{
			Category: "file health",
			Label:    "repo blobs in track branches",
			Severity: SeverityOK,
			Detail:   fmt.Sprintf("all %d file(s) have blobs in HEAD", total),
		})
	}
}
