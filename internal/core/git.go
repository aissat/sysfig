package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitBareRun runs a git command against a bare repo at repoDir.
//
// extraEnv is a list of "KEY=VALUE" strings merged on top of the current
// process environment. Callers pass GIT_WORK_TREE here when they need git to
// treat a directory as the working tree (e.g. during add/commit).
//
// There is intentionally no Repo wrapper object — the git CLI is the right
// interface; a Go struct around it adds indirection with zero benefit.
func gitBareRun(repoDir string, timeout time.Duration, extraEnv []string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)

	// Build the environment: inherit everything, then overlay caller extras,
	// then always set GIT_DIR so git knows this is a bare repo.
	env := os.Environ()
	env = append(env, extraEnv...)
	env = append(env, "GIT_DIR="+repoDir)
	cmd.Env = env

	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %v: %w\n%s", args, err, buf.String())
	}
	return nil
}

// gitBareOutput is like gitBareRun but returns the combined stdout output.
func gitBareOutput(repoDir string, timeout time.Duration, extraEnv []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)

	env := os.Environ()
	env = append(env, extraEnv...)
	env = append(env, "GIT_DIR="+repoDir)
	cmd.Env = env

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %v: %w\n%s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// gitShowBytes reads a file out of the bare repo at HEAD using
// `git --git-dir=<repoDir> show HEAD:<relPath>` and returns the raw content.
//
// relPath must NOT have a leading slash (e.g. "etc/nginx/nginx.conf").
// Returns an error when the file does not exist in HEAD (e.g. not yet committed).
func gitShowBytes(repoDir, relPath string) ([]byte, error) {
	ref := "HEAD:" + relPath
	data, err := gitBareOutput(repoDir, 15*time.Second, nil, "show", ref)
	if err != nil {
		return nil, fmt.Errorf("core: git show %q: %w", ref, err)
	}
	return data, nil
}

// gitStageFile stages a single file from the real filesystem into the bare
// repo index using GIT_WORK_TREE=/.
//
// relPath is the path relative to / (e.g. "etc/nginx/nginx.conf").
// The function sets GIT_WORK_TREE=/ so git reads the file from /etc/nginx/nginx.conf.
func gitStageFile(repoDir, relPath string, timeout time.Duration) error {
	return gitBareRun(repoDir, timeout, []string{"GIT_WORK_TREE=/"}, "add", relPath)
}

// gitStageFileFromDir stages relPath into the bare repo using an explicit
// work tree root (useful for temp dirs when staging encrypted content).
//
// workTree is the directory that acts as the filesystem root.
// relPath is relative to workTree (e.g. "etc/nginx/nginx.conf").
func gitStageFileFromDir(repoDir, workTree, relPath string, timeout time.Duration) error {
	return gitBareRun(repoDir, timeout, []string{"GIT_WORK_TREE=" + workTree}, "add", relPath)
}

// gitCommit creates a commit in the bare repo.
// GIT_WORK_TREE=/ is required even for commit when using a bare repo with an
// index — git checks it is set.
func gitCommit(repoDir, message string, timeout time.Duration) error {
	return gitBareRun(repoDir, timeout, []string{"GIT_WORK_TREE=/"}, "commit", "-m", message)
}

// isNothingToCommitBare returns true when the bare repo's index has no staged
// changes relative to HEAD (i.e. `git status --porcelain` is empty).
func isNothingToCommitBare(repoDir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+repoDir,
		"GIT_WORK_TREE=/",
	)
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return false
	}
	return len(bytes.TrimSpace(buf.Bytes())) == 0
}

// gitHashObject writes data to the bare repo's object store and returns the
// 40-character blob SHA. Used to stage encrypted content without touching the
// real filesystem at the canonical path.
func gitHashObject(repoDir string, data []byte, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "hash-object", "-w", "--stdin")
	cmd.Env = append(os.Environ(), "GIT_DIR="+repoDir)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("core: git hash-object: %w\n%s", err, stderr.String())
	}

	blobHash := string(bytes.TrimSpace(stdout.Bytes()))
	if len(blobHash) != 40 {
		return "", fmt.Errorf("core: git hash-object: unexpected output %q", blobHash)
	}
	return blobHash, nil
}

// gitUpdateIndex adds a blob to the bare repo's index at relPath with the
// given mode (e.g. "100644") and blob SHA returned by gitHashObject.
func gitUpdateIndex(repoDir, mode, blobHash, relPath string, timeout time.Duration) error {
	return gitBareRun(repoDir, timeout, nil,
		"update-index", "--add", "--cacheinfo",
		fmt.Sprintf("%s,%s,%s", mode, blobHash, relPath),
	)
}

// isNoUpstreamError returns true when a git push failed because the branch
// has no upstream tracking reference (first push to a new remote).
// git exits 128 with "has no upstream branch" in the error message.
func isNoUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "has no upstream branch") ||
		strings.Contains(msg, "set the remote as upstream")
}

// gitBareExists returns true if repoDir looks like an initialised bare git
// repository (it contains a HEAD file, which both bare repos and worktrees have).
func gitBareExists(repoDir string) bool {
	_, err := os.Stat(repoDir + "/HEAD")
	return err == nil
}
