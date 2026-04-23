package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	sysfigfs "github.com/aissat/sysfig/internal/fs"
)

// resolveTrackBranch returns a branch name that won't conflict with existing
// refs in the bare repo. Git refs use "/" as a hierarchy separator, so
// "track/a/b" and "track/a/b/c" cannot coexist — one would be a "directory"
// blocking the other. If a conflict is detected the function appends "--file"
// (or "--dir") to make the name unique.
func resolveTrackBranch(repoDir, branch string) string {
	out, err := gitBareOutput(repoDir, 5*time.Second, nil,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return branch // can't check — use as-is
	}
	existing := strings.Split(strings.TrimSpace(string(out)), "\n")

	for _, ref := range existing {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		// Case 1: existing ref is a prefix of our branch (e.g. "track/a/b"
		// exists and we want "track/a/b/c").
		if strings.HasPrefix(branch, ref+"/") {
			branch = strings.ReplaceAll(branch, ref+"/", ref+"--/")
		}
		// Case 2: our branch is a prefix of an existing ref (e.g. "track/a/b/c"
		// exists and we want "track/a/b").
		if strings.HasPrefix(ref, branch+"/") {
			branch = branch + "--dir"
		}
	}
	return branch
}

// SanitizeBranchName converts a repo-relative path into a valid git ref
// component. Git refuses any ref path component that starts with "." (e.g.
// ".zshrc"), so we replace a leading dot with "dot-".
//
// Example: "home/aye7/.zshrc" → "home/aye7/dot-zshrc"
func SanitizeBranchName(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ".") {
			parts[i] = "dot-" + p[1:]
		}
	}
	return strings.Join(parts, "/")
}

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
	return gitShowBytesAt(repoDir, "HEAD", relPath)
}

// gitShowBytesAt reads a file from a specific ref (branch, commit, or HEAD).
func gitShowBytesAt(repoDir, ref, relPath string) ([]byte, error) {
	r := ref + ":" + relPath
	data, err := gitBareOutput(repoDir, 15*time.Second, nil, "show", r)
	if err != nil {
		return nil, fmt.Errorf("core: git show %q: %w", r, err)
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

// BlobEntry describes one file to include in a gitCommitToBranch call.
type BlobEntry struct {
	BlobHash string // 40-char SHA from git hash-object
	RelPath  string // repo-relative path, e.g. "etc/nginx/nginx.conf"
	Mode     string // git file mode, typically "100644"
}

// gitCommitToBranch creates an isolated commit on the given branch containing
// only the provided blobs — no other files, no shared index state.
//
// It uses a temporary GIT_INDEX_FILE so the operation is fully isolated from
// the default index and from concurrent group commits.
//
// Steps:
//  1. Temp index file: GIT_INDEX_FILE=<tmp> git read-tree --empty
//  2. For each blob: git update-index --add --cacheinfo <mode>,<sha>,<path>
//     2b. For each deletePath: git update-index --remove <path>
//  3. git write-tree  → tree SHA (contains ONLY these blobs)
//  4. git commit-tree → commit SHA (parent = current branch tip, if any)
//  5. git update-ref  → advance refs/heads/<branch>
func gitCommitToBranch(repoDir, branch, message string, blobs []BlobEntry, deletePaths []string, timeout time.Duration) error {
	// Temporary index file — isolated per branch, safe for concurrent groups.
	tmpIdx, err := os.CreateTemp(sysfigfs.SecureTempDir(), "sysfig-index-*")
	if err != nil {
		return fmt.Errorf("gitCommitToBranch: create temp index: %w", err)
	}
	tmpIdx.Close()
	defer os.Remove(tmpIdx.Name())

	idxEnv := []string{"GIT_INDEX_FILE=" + tmpIdx.Name()}

	// 1. Seed the index from the existing branch HEAD (if it exists) so that
	//    files not included in this commit are preserved in the tree.
	//    Falls back to an empty index for the very first commit on this branch.
	ref := "refs/heads/" + branch
	parentOut, _ := gitBareOutput(repoDir, 5*time.Second, nil, "rev-parse", "--verify", ref)
	existingParent := strings.TrimSpace(string(parentOut))
	if existingParent != "" {
		if err := gitBareRun(repoDir, timeout, idxEnv, "read-tree", existingParent); err != nil {
			return fmt.Errorf("git read-tree %s: %w", existingParent, err)
		}
	} else {
		if err := gitBareRun(repoDir, timeout, idxEnv, "read-tree", "--empty"); err != nil {
			return fmt.Errorf("git read-tree --empty: %w", err)
		}
	}

	// 2. Add each blob to the isolated index (overwrites existing entries for the same path).
	for _, b := range blobs {
		mode := b.Mode
		if mode == "" {
			mode = "100644"
		}
		cacheinfo := fmt.Sprintf("%s,%s,%s", mode, b.BlobHash, b.RelPath)
		if err := gitBareRun(repoDir, timeout, idxEnv,
			"update-index", "--add", "--cacheinfo", cacheinfo); err != nil {
			return fmt.Errorf("git update-index %s: %w", b.RelPath, err)
		}
	}

	// 2b. Remove deleted files from the index.
	// Use git ls-files --stage to read current index entries, filter out the
	// deleted paths, rebuild the index from scratch using --cacheinfo (which
	// is path-resolution-free and works reliably in bare repos).
	if len(deletePaths) > 0 {
		deleteSet := make(map[string]bool, len(deletePaths))
		for _, dp := range deletePaths {
			deleteSet[dp] = true
		}

		// Dump current index entries: "<mode> <sha> <stage>\t<path>"
		lsOut, err := gitBareOutput(repoDir, timeout, idxEnv, "ls-files", "--stage")
		if err != nil {
			return fmt.Errorf("git ls-files --stage: %w", err)
		}

		// Clear the index.
		if err := gitBareRun(repoDir, timeout, idxEnv, "read-tree", "--empty"); err != nil {
			return fmt.Errorf("git read-tree --empty (rebuild): %w", err)
		}

		// Re-add every entry except deleted ones.
		for _, line := range strings.Split(strings.TrimSpace(string(lsOut)), "\n") {
			if line == "" {
				continue
			}
			// Format: "<mode> <sha> <stage>\t<path>"
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				continue
			}
			entryPath := line[tab+1:]
			if deleteSet[entryPath] {
				continue // omit deleted file
			}
			fields := strings.Fields(line[:tab])
			if len(fields) < 2 {
				continue
			}
			mode, sha := fields[0], fields[1]
			cacheinfo := mode + "," + sha + "," + entryPath
			if err := gitBareRun(repoDir, timeout, idxEnv,
				"update-index", "--add", "--cacheinfo", cacheinfo); err != nil {
				return fmt.Errorf("git update-index (rebuild) %s: %w", entryPath, err)
			}
		}

		// Re-add the new blobs on top (they were added in step 2 but the
		// index was just cleared; add them again).
		for _, b := range blobs {
			mode := b.Mode
			if mode == "" {
				mode = "100644"
			}
			cacheinfo := fmt.Sprintf("%s,%s,%s", mode, b.BlobHash, b.RelPath)
			if err := gitBareRun(repoDir, timeout, idxEnv,
				"update-index", "--add", "--cacheinfo", cacheinfo); err != nil {
				return fmt.Errorf("git update-index (re-add blob) %s: %w", b.RelPath, err)
			}
		}
	}

	// 3. Write the tree — contains ONLY the blobs above.
	treeOut, err := gitBareOutput(repoDir, timeout, idxEnv, "write-tree")
	if err != nil {
		return fmt.Errorf("git write-tree: %w", err)
	}
	treeHash := strings.TrimSpace(string(treeOut))

	// 4. Use the parent already resolved in step 1.
	parent := existingParent

	commitArgs := []string{"commit-tree", treeHash, "-m", message}
	if parent != "" {
		commitArgs = append(commitArgs, "-p", parent)
	}
	commitOut, err := gitBareOutput(repoDir, timeout, resolveGitIdentity(repoDir), commitArgs...)
	if err != nil {
		return fmt.Errorf("git commit-tree: %w", err)
	}
	commitHash := strings.TrimSpace(string(commitOut))

	// 5. Advance the branch ref.
	if err := gitBareRun(repoDir, timeout, nil, "update-ref", ref, commitHash); err != nil {
		return fmt.Errorf("git update-ref %s: %w", branch, err)
	}
	return nil
}

// resolveGitIdentity returns GIT_AUTHOR_* and GIT_COMMITTER_* env vars for
// git commit-tree. Reads from (in priority order):
//  1. The repo's local git config (user.name / user.email)
//  2. The real user's ~/.gitconfig (via SUDO_USER when running under sudo)
//  3. Hard fallback: "sysfig" / "sysfig@localhost"
func resolveGitIdentity(repoDir string) []string {
	name, email := repoLocalGitIdentity(repoDir)
	if name == "" || email == "" {
		n2, e2 := realUserGitIdentity()
		if name == "" {
			name = n2
		}
		if email == "" {
			email = e2
		}
	}
	if name == "" {
		name = "sysfig"
	}
	if email == "" {
		email = "sysfig@localhost"
	}
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
}

// repoLocalGitIdentity reads user.name and user.email from the bare repo's
// own config file (GIT_DIR/config).
func repoLocalGitIdentity(repoDir string) (name, email string) {
	if out, err := gitBareOutput(repoDir, 3*time.Second, nil, "config", "--local", "user.name"); err == nil {
		name = strings.TrimSpace(string(out))
	}
	if out, err := gitBareOutput(repoDir, 3*time.Second, nil, "config", "--local", "user.email"); err == nil {
		email = strings.TrimSpace(string(out))
	}
	return
}

// realUserGitIdentity reads user.name and user.email from the real user's
// ~/.gitconfig. When running under sudo, SUDO_USER identifies the real user.
func realUserGitIdentity() (name, email string) {
	homeDir := ""
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			homeDir = u.HomeDir
		}
	}
	if homeDir == "" {
		homeDir = os.Getenv("HOME")
	}
	if homeDir == "" {
		return
	}
	gitconfigPath := filepath.Join(homeDir, ".gitconfig")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "git", "config", "--file", gitconfigPath, "user.name").Output(); err == nil {
		name = strings.TrimSpace(string(out))
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if out, err := exec.CommandContext(ctx2, "git", "config", "--file", gitconfigPath, "user.email").Output(); err == nil {
		email = strings.TrimSpace(string(out))
	}
	return
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

// hasBranch returns true if the named branch exists in the bare repo.
func hasBranch(repoDir, branch string) bool {
	_, err := gitBareOutput(repoDir, 5*time.Second, nil,
		"rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

// listBranchRefs returns all ref names (e.g. "refs/heads/track/etc_nginx")
// that start with the given prefix, using `git for-each-ref`.
func listBranchRefs(repoDir, prefix string) ([]string, error) {
	out, err := gitBareOutput(repoDir, 10*time.Second, nil,
		"for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			refs = append(refs, line)
		}
	}
	return refs, nil
}
