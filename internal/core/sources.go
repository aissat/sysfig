package core

// sources.go — Config Sources feature: manage shared config template catalogs.
//
// Key concepts:
//   - A *source* is a remote bundle (bundle+local:// or bundle+ssh://) that
//     contains profile directories under profiles/<name>/.
//   - A *profile* is a template bundle with a profile.yaml, templates, and
//     optional post-apply hooks.
//   - A *profile activation* pairs a profile with per-machine variable values
//     and is stored in ~/.sysfig/sources.yaml.
//   - sysfig source render reads each activation, fetches the source bundle
//     into a local cache repo, renders each template, and commits the results
//     to the machine's track/* branches exactly like manually tracked files.
//
// This follows the "Render-to-Git" architecture from docs/rfcs/config-sources.md.

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aissat/sysfig/internal/hash"
	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// ── Configuration types ───────────────────────────────────────────────────────

// SourceDecl declares a named source bundle that supplies profile templates.
type SourceDecl struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

// ProfileActivation pairs a profile with per-machine variable values.
// Source is "<sourceName>/<profileName>", e.g. "corporate/system-proxy".
type ProfileActivation struct {
	Source    string            `yaml:"source"`
	Variables map[string]string `yaml:"variables,omitempty"`
}

// SourcesConfig is the on-disk representation of ~/.sysfig/sources.yaml.
type SourcesConfig struct {
	Sources  []SourceDecl        `yaml:"sources,omitempty"`
	Profiles []ProfileActivation `yaml:"profiles,omitempty"`
}

// ── profile.yaml types ────────────────────────────────────────────────────────

// ProfileVarDecl describes one variable declared in a profile.yaml.
type ProfileVarDecl struct {
	Required    bool   `yaml:"required,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Description string `yaml:"description,omitempty"`
	Example     string `yaml:"example,omitempty"`
}

// ProfileFileDecl describes one output file declared in a profile.yaml.
type ProfileFileDecl struct {
	Dest     string `yaml:"dest"`     // absolute destination path, e.g. /etc/environment
	Template string `yaml:"template"` // path relative to profile dir, e.g. templates/environment.tmpl
	Mode     string `yaml:"mode,omitempty"`
	Owner    string `yaml:"owner,omitempty"`
	Group    string `yaml:"group,omitempty"`
	Encrypt  bool   `yaml:"encrypt,omitempty"`
}

// ProfileHookDecl describes one hook entry in a profile.yaml.
type ProfileHookDecl struct {
	Exec          string `yaml:"exec,omitempty"`
	SystemdReload string `yaml:"systemd_reload,omitempty"`
}

// ProfileHooks groups all hook lists for a profile.
type ProfileHooks struct {
	PostApply []ProfileHookDecl `yaml:"post_apply,omitempty"`
}

// ProfileYAML is the parsed representation of a profile.yaml file.
type ProfileYAML struct {
	Name        string                    `yaml:"name"`
	Version     string                    `yaml:"version,omitempty"`
	Description string                    `yaml:"description,omitempty"`
	Variables   map[string]ProfileVarDecl `yaml:"variables,omitempty"`
	Files       []ProfileFileDecl         `yaml:"files"`
	Hooks       ProfileHooks              `yaml:"hooks,omitempty"`
}

// ── Config I/O ────────────────────────────────────────────────────────────────

// LoadSourcesConfig reads ~/.sysfig/sources.yaml.
// Returns an empty config (no error) if the file doesn't exist yet.
func LoadSourcesConfig(baseDir string) (*SourcesConfig, error) {
	path := filepath.Join(baseDir, "sources.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &SourcesConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sources: read %q: %w", path, err)
	}
	var cfg SourcesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("sources: parse %q: %w", path, err)
	}
	return &cfg, nil
}

// SaveSourcesConfig writes sources.yaml back to ~/.sysfig/sources.yaml.
func SaveSourcesConfig(baseDir string, cfg *SourcesConfig) error {
	path := filepath.Join(baseDir, "sources.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("sources: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("sources: write %q: %w", path, err)
	}
	return nil
}

// ── Cache paths ───────────────────────────────────────────────────────────────

// SourceCacheDir returns the local cache directory for a named source.
// e.g. ~/.sysfig/sources/corporate/
func SourceCacheDir(baseDir, name string) string {
	return filepath.Join(baseDir, "sources", name)
}

// SourceRepoDir returns the bare git repo path for a cached source bundle.
// e.g. ~/.sysfig/sources/corporate/repo.git
func SourceRepoDir(baseDir, name string) string {
	return filepath.Join(SourceCacheDir(baseDir, name), "repo.git")
}

// ── Source management commands ────────────────────────────────────────────────

// SourceAdd registers a new source declaration in sources.yaml.
// Returns an error if a source with the same name already exists.
func SourceAdd(baseDir, name, url string) error {
	cfg, err := LoadSourcesConfig(baseDir)
	if err != nil {
		return err
	}
	for _, s := range cfg.Sources {
		if s.Name == name {
			return fmt.Errorf("source %q is already registered (url: %s)", name, s.URL)
		}
	}
	cfg.Sources = append(cfg.Sources, SourceDecl{Name: name, URL: url})
	return SaveSourcesConfig(baseDir, cfg)
}

// SourcePull fetches (or updates) a source bundle into the local cache.
// If the cache repo does not exist yet it is initialised from scratch.
func SourcePull(baseDir, name string) error {
	cfg, err := LoadSourcesConfig(baseDir)
	if err != nil {
		return err
	}
	var srcURL string
	for _, s := range cfg.Sources {
		if s.Name == name {
			srcURL = s.URL
			break
		}
	}
	if srcURL == "" {
		return fmt.Errorf("source %q is not registered — run: sysfig source add %s <url>", name, name)
	}
	return ensureSourceRepo(baseDir, name, srcURL)
}

// ensureSourceRepo initialises (if needed) and pulls the source bundle into
// the local cache repo at SourceRepoDir(baseDir, name).
//
// Supports all three transport types:
//   - bundle+local:// and bundle+ssh:// → BundlePull
//   - standard git remote (git@, https://, etc.) → git clone/fetch
func ensureSourceRepo(baseDir, name, url string) error {
	cacheDir := SourceCacheDir(baseDir, name)
	repoDir := SourceRepoDir(baseDir, name)

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("sources: create cache dir: %w", err)
	}

	repoExists := func() bool {
		_, err := os.Stat(filepath.Join(repoDir, "HEAD"))
		return err == nil
	}

	if ParseRemoteKind(url) != RemoteGit {
		// Bundle transport — init bare repo if needed, then BundlePull.
		if !repoExists() {
			if err := bundleGitInitBare(repoDir); err != nil {
				return fmt.Errorf("sources: init source repo: %w", err)
			}
		}
		_, err := BundlePull(BundlePullOptions{
			RepoDir:   repoDir,
			RemoteURL: url,
		})
		return err
	}

	// Standard git remote — clone bare on first use, fetch on subsequent calls.
	if !repoExists() {
		if err := gitClone(url, repoDir); err != nil {
			return fmt.Errorf("sources: clone %q: %w", url, err)
		}
		return nil
	}
	// Repo already exists — fetch latest from origin.
	return gitBareRun(repoDir, 60*time.Second, nil, "fetch", "origin")
}

// sourceBestRef returns the best available branch name in a source bare repo.
// Prefers "main", then "master", then the first branch alphabetically.
// Falls back to "HEAD" when no branches are found.
func sourceBestRef(repoDir string) string {
	out, err := gitBareOutput(repoDir, 5*time.Second, nil,
		"for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return "HEAD"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var branches []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		branches = append(branches, l)
	}
	sort.Strings(branches)
	for _, b := range branches {
		if b == "main" || b == "master" || b == "trunk" {
			return b
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return "HEAD"
}

// ── source list ───────────────────────────────────────────────────────────────

// SourceListEntry describes one profile available in a source.
type SourceListEntry struct {
	Name        string
	Description string
	Files       int // number of output files declared
}

// SourceList returns all profiles available in a registered source.
// Calls SourcePull first so the local cache is up to date.
func SourceList(baseDir, sourceName string) ([]SourceListEntry, error) {
	if err := SourcePull(baseDir, sourceName); err != nil {
		return nil, fmt.Errorf("source list: pull: %w", err)
	}

	repoDir := SourceRepoDir(baseDir, sourceName)
	ref := sourceBestRef(repoDir)

	// List all directory entries under "profiles/" in the source repo.
	out, err := gitBareOutput(repoDir, 10*time.Second, nil,
		"ls-tree", "--name-only", ref, "profiles/")
	if err != nil {
		return nil, fmt.Errorf("source list: ls-tree profiles/: %w", err)
	}

	var entries []SourceListEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// git ls-tree outputs full paths (e.g. "profiles/system-proxy").
		// We need just the last component as the profile name.
		name := filepath.Base(line)
		profileYAMLPath := "profiles/" + name + "/profile.yaml"
		data, err := gitShowBytesAt(repoDir, ref, profileYAMLPath)
		if err != nil {
			// Profile directory has no profile.yaml — skip silently.
			continue
		}
		var p ProfileYAML
		if err := yaml.Unmarshal(data, &p); err != nil {
			continue
		}
		entries = append(entries, SourceListEntry{
			Name:        name,
			Description: p.Description,
			Files:       len(p.Files),
		})
	}
	return entries, nil
}

// ── source use ────────────────────────────────────────────────────────────────

// SourceUseOptions configures a sysfig source use operation.
type SourceUseOptions struct {
	BaseDir       string
	SourceProfile string            // "<sourceName>/<profileName>"
	Variables     map[string]string // variable name → value
}

// SourceUse adds or updates a profile activation in sources.yaml.
// If the profile is already activated, its variable values are updated.
func SourceUse(opts SourceUseOptions) error {
	if opts.BaseDir == "" {
		return fmt.Errorf("source use: BaseDir must not be empty")
	}
	if opts.SourceProfile == "" {
		return fmt.Errorf("source use: SourceProfile must not be empty")
	}

	cfg, err := LoadSourcesConfig(opts.BaseDir)
	if err != nil {
		return err
	}

	incoming := ProfileActivation{
		Source:    opts.SourceProfile,
		Variables: opts.Variables,
	}

	// Dedup by source + variables: same profile with same vars = update in place.
	// Same profile with different vars = new activation (e.g. git-identity for proda vs charik).
	for i, p := range cfg.Profiles {
		if p.Source == incoming.Source && maps.Equal(p.Variables, incoming.Variables) {
			cfg.Profiles[i] = incoming
			return SaveSourcesConfig(opts.BaseDir, cfg)
		}
	}

	cfg.Profiles = append(cfg.Profiles, incoming)
	return SaveSourcesConfig(opts.BaseDir, cfg)
}

// ReadProfileYAML reads and parses a profile.yaml from a source cache repo.
func ReadProfileYAML(baseDir, sourceName, profileName string) (*ProfileYAML, error) {
	repoDir := SourceRepoDir(baseDir, sourceName)
	ref := sourceBestRef(repoDir)
	path := "profiles/" + profileName + "/profile.yaml"
	data, err := gitShowBytesAt(repoDir, ref, path)
	if err != nil {
		return nil, fmt.Errorf("source: read profile.yaml for %s/%s: %w", sourceName, profileName, err)
	}
	var p ProfileYAML
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("source: parse profile.yaml for %s/%s: %w", sourceName, profileName, err)
	}
	return &p, nil
}

// ── source render ─────────────────────────────────────────────────────────────

// RenderOptions configures a sysfig source render operation.
type RenderOptions struct {
	// BaseDir is ~/.sysfig.
	BaseDir string
	// Profile, when non-empty, limits rendering to one activated profile.
	// Format: "<sourceName>/<profileName>".
	Profile string
	// Force transfers ownership of files currently owned by a different
	// profile or manually tracked.
	Force bool
	// DryRun prints what would be committed without writing anything.
	DryRun bool
}

// RenderConflict describes a file ownership conflict detected during render.
type RenderConflict struct {
	SystemPath   string
	CurrentOwner string // "manual" or "<other-sourceProfile>"
	RequestedBy  string // the source profile requesting ownership
}

// RenderResult reports the outcome of a render operation.
type RenderResult struct {
	Rendered   []string         // system paths with new commits
	Skipped    []string         // system paths with no change (hash matched)
	Conflicts  []RenderConflict // conflicts that blocked rendering (empty when Force used)
	HookErrors []error          // non-fatal post_apply hook failures
}

// SourceRender renders all (or one specific) activated profile(s) into the
// local bare repo.
//
// For each activated profile:
//  1. Fetch the source bundle into the local cache (if needed)
//  2. Read profile.yaml and validate required variables
//  3. For each output file: render the template, check ownership, commit
//  4. Update state.json with source_profile ownership
//
// Returns a RenderResult and an error. When conflicts are detected and
// opts.Force is false, an error is returned listing all conflicts. With
// opts.DryRun no git writes occur.
func SourceRender(opts RenderOptions) (*RenderResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("source render: BaseDir must not be empty")
	}

	cfg, err := LoadSourcesConfig(opts.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("source render: load sources config: %w", err)
	}

	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)
	currentState, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("source render: load state: %w", err)
	}

	repoDir := filepath.Join(opts.BaseDir, "repo.git")
	result := &RenderResult{}

	// Build a map from source name → URL for fast lookup.
	sourceURLs := make(map[string]string, len(cfg.Sources))
	for _, s := range cfg.Sources {
		sourceURLs[s.Name] = s.URL
	}

	// Build a map of systemPath → existing FileRecord for conflict detection.
	pathToRecord := make(map[string]*types.FileRecord, len(currentState.Files))
	for _, rec := range currentState.Files {
		pathToRecord[rec.SystemPath] = rec
	}

	for _, activation := range cfg.Profiles {
		if opts.Profile != "" && activation.Source != opts.Profile {
			continue
		}
		renderedCount := 0 // tracks new commits for this activation

		// Parse "sourceName/profileName".
		parts := strings.SplitN(activation.Source, "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("source render: invalid source reference %q (expected sourceName/profileName)", activation.Source)
		}
		sourceName, profileName := parts[0], parts[1]

		// Ensure source is cached locally.
		srcURL, ok := sourceURLs[sourceName]
		if !ok {
			return nil, fmt.Errorf("source render: source %q is not registered in sources.yaml", sourceName)
		}
		if err := ensureSourceRepo(opts.BaseDir, sourceName, srcURL); err != nil {
			return nil, fmt.Errorf("source render: ensure source %q: %w", sourceName, err)
		}

		sourceRepoDir := SourceRepoDir(opts.BaseDir, sourceName)
		ref := sourceBestRef(sourceRepoDir)

		// Read profile.yaml from source repo.
		profileYAMLPath := "profiles/" + profileName + "/profile.yaml"
		profileData, err := gitShowBytesAt(sourceRepoDir, ref, profileYAMLPath)
		if err != nil {
			return nil, fmt.Errorf("source render: read %s: %w", profileYAMLPath, err)
		}
		var profile ProfileYAML
		if err := yaml.Unmarshal(profileData, &profile); err != nil {
			return nil, fmt.Errorf("source render: parse %s: %w", profileYAMLPath, err)
		}

		// Validate required variables and resolve defaults.
		resolved := make(map[string]string, len(profile.Variables))
		for varName, decl := range profile.Variables {
			val, provided := activation.Variables[varName]
			if provided && val != "" {
				resolved[varName] = val
			} else if decl.Default != "" {
				resolved[varName] = decl.Default
			} else if decl.Required {
				return nil, fmt.Errorf("source render: profile %s: required variable %q is not set", activation.Source, varName)
			} else {
				// Optional variable with empty default — still register it so
				// {{varName}} in templates resolves to an empty string rather
				// than an "unknown variable" error.
				resolved[varName] = ""
			}
		}
		// Include any extra variables the user set that aren't in the declaration.
		for k, v := range activation.Variables {
			if _, exists := resolved[k]; !exists {
				resolved[k] = v
			}
		}

		// Build TemplateVars: put resolved profile variables into Extra so
		// {{varName}} placeholders are substituted by the existing engine.
		tmplVars := DefaultTemplateVars()
		for k, v := range resolved {
			tmplVars.Extra[k] = v
		}

		// Render each output file declared in the profile.
		for _, fileDecl := range profile.Files {
			// Substitute variables in the dest path too (e.g. /home/{{user}}/…).
			renderedDest, err := RenderTemplate([]byte(fileDecl.Dest), tmplVars)
			if err != nil {
				return nil, fmt.Errorf("source render: render dest path %q: %w", fileDecl.Dest, err)
			}
			dest := strings.TrimRight(string(renderedDest), "\n")
			relPath := strings.TrimPrefix(dest, "/")

			// Read template from source repo.
			tmplPath := "profiles/" + profileName + "/" + fileDecl.Template
			tmplBytes, err := gitShowBytesAt(sourceRepoDir, ref, tmplPath)
			if err != nil {
				return nil, fmt.Errorf("source render: read template %s: %w", tmplPath, err)
			}

			// Render using the existing {{variable}} engine.
			rendered, err := RenderTemplate(tmplBytes, tmplVars)
			if err != nil {
				return nil, fmt.Errorf("source render: render %s → %s: %w", tmplPath, dest, err)
			}

			// Conflict check: is this path already owned by someone else?
			if existing, owned := pathToRecord[dest]; owned {
				if existing.SourceProfile != activation.Source {
					owner := existing.SourceProfile
					if owner == "" {
						owner = "manual"
					}
					if !opts.Force {
						result.Conflicts = append(result.Conflicts, RenderConflict{
							SystemPath:   dest,
							CurrentOwner: owner,
							RequestedBy:  activation.Source,
						})
						continue
					}
					// --force: transfer ownership (fall through to commit).
				}
			}

			if opts.DryRun {
				result.Rendered = append(result.Rendered, dest)
				continue
			}

			// Check if the rendered content matches what's already committed.
			trackBranch := "track/" + SanitizeBranchName(relPath)
			if existing, err := gitShowBytesAt(repoDir, trackBranch, relPath); err == nil {
				if hash.Bytes(existing) == hash.Bytes(rendered) {
					result.Skipped = append(result.Skipped, dest)
					// Still refresh the state record to ensure source_profile is set.
					updateSourceRecord(sm, dest, relPath, trackBranch, activation.Source, rendered)
					continue
				}
			}

			// Hash and commit the rendered blob.
			blobHash, err := syncHashBlob(repoDir, rendered)
			if err != nil {
				return nil, fmt.Errorf("source render: hash blob %s: %w", dest, err)
			}
			commitMsg := "sysfig: generate " + activation.Source
			if err := gitCommitToBranch(repoDir, trackBranch, commitMsg, []BlobEntry{
				{BlobHash: blobHash, RelPath: relPath},
			}, 30*time.Second); err != nil {
				return nil, fmt.Errorf("source render: commit %s: %w", dest, err)
			}
			result.Rendered = append(result.Rendered, dest)
			renderedCount++

			// Update state.json.
			updateSourceRecord(sm, dest, relPath, trackBranch, activation.Source, rendered)
			// Refresh pathToRecord so subsequent profiles see the updated ownership.
			_ = sm.Load // force reload is done implicitly by WithLock; refresh local map
		}

		// Run post_apply hooks if any files were newly committed.
		if !opts.DryRun && renderedCount > 0 {
			for _, hookErr := range runProfileHooks(profile.Hooks.PostApply) {
				// Non-fatal: collect but don't abort the render.
				result.HookErrors = append(result.HookErrors, hookErr)
			}
		}
	}

	// If there are unresolved conflicts, return an error.
	if len(result.Conflicts) > 0 {
		return result, fmt.Errorf("source render: %d conflict(s) — re-run with --force to transfer ownership", len(result.Conflicts))
	}

	return result, nil
}

// updateSourceRecord creates or updates the FileRecord for a source-managed
// file in state.json.
func updateSourceRecord(sm *state.Manager, systemPath, relPath, branch, sourceProfile string, rendered []byte) {
	id := deriveID(systemPath)
	h := hash.Bytes(rendered)
	now := time.Now()

	_ = sm.WithLock(func(s *types.State) error {
		rec, exists := s.Files[id]
		if !exists {
			rec = &types.FileRecord{
				ID:         id,
				SystemPath: systemPath,
				RepoPath:   relPath,
				Branch:     branch,
				Status:     types.StatusTracked,
			}
		}
		rec.CurrentHash = h
		rec.LastSync = &now
		rec.SourceProfile = sourceProfile
		rec.Branch = branch
		rec.RepoPath = relPath
		s.Files[id] = rec
		return nil
	})
}

// runProfileHooks executes post_apply hooks declared in a profile.yaml.
// Each entry has either an exec string ("cmd arg1 arg2") or a systemd_reload
// service name — never both. Errors are returned but do not abort the render.
func runProfileHooks(hooks []ProfileHookDecl) []error {
	var errs []error
	for _, h := range hooks {
		switch {
		case h.Exec != "":
			parts := strings.Fields(h.Exec)
			if len(parts) == 0 {
				continue
			}
			if _, err := runCmd(30*time.Second, parts[0], parts[1:]...); err != nil {
				errs = append(errs, fmt.Errorf("source hook exec %q: %w", h.Exec, err))
			}
		case h.SystemdReload != "":
			if err := validateService(h.SystemdReload); err != nil {
				errs = append(errs, err)
				continue
			}
			if _, err := runCmd(30*time.Second, "systemctl", "reload", h.SystemdReload); err != nil {
				errs = append(errs, fmt.Errorf("source hook systemd_reload %q: %w", h.SystemdReload, err))
			}
		}
	}
	return errs
}

// ── Direct remote profile rendering ──────────────────────────────────────────

// RenderedFile is a single rendered template output ready to be written.
type RenderedFile struct {
	Dest    string      // absolute destination path, e.g. /etc/environment
	Content []byte      // fully rendered file content
	Mode    os.FileMode // file permission (defaults to 0o644)
}

// FetchProfileRepo clones any git/bundle URL into a bare repo at repoDir.
// If repoDir already contains a valid bare repo it is updated (fetch/pull).
// This is the lightweight version of ensureSourceRepo for one-shot operations.
func FetchProfileRepo(url, repoDir string) error {
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return fmt.Errorf("source: create repo dir: %w", err)
	}
	repoExists := func() bool {
		_, err := os.Stat(filepath.Join(repoDir, "HEAD"))
		return err == nil
	}
	if ParseRemoteKind(url) != RemoteGit {
		if !repoExists() {
			if err := bundleGitInitBare(repoDir); err != nil {
				return fmt.Errorf("source: init repo: %w", err)
			}
		}
		_, err := BundlePull(BundlePullOptions{RepoDir: repoDir, RemoteURL: url})
		return err
	}
	if !repoExists() {
		return gitClone(url, repoDir)
	}
	return gitBareRun(repoDir, 60*time.Second, nil, "fetch", "origin")
}

// RenderProfileFromRepo renders a single profile from an already-fetched bare
// repo dir and returns the rendered files ready for deployment.
//
//   - repoDir: path to the bare git repo containing profiles/
//   - profileName: name of the profile directory under profiles/
//   - vars: user-supplied variable values (override defaults from profile.yaml)
func RenderProfileFromRepo(repoDir, profileName string, vars map[string]string) ([]RenderedFile, error) {
	ref := sourceBestRef(repoDir)
	profilePath := "profiles/" + profileName + "/profile.yaml"
	data, err := gitShowBytesAt(repoDir, ref, profilePath)
	if err != nil {
		return nil, fmt.Errorf("source: read profile %q: %w", profileName, err)
	}
	var profile ProfileYAML
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("source: parse profile.yaml: %w", err)
	}

	// Build template vars: start with built-in OS vars, overlay declared
	// profile defaults, then overlay caller-supplied vars.
	tvars := DefaultTemplateVars()
	if tvars.Extra == nil {
		tvars.Extra = make(map[string]string)
	}
	for name, decl := range profile.Variables {
		if decl.Default != "" {
			tvars.Extra[name] = decl.Default
		}
	}
	for k, v := range vars {
		tvars.Extra[k] = v
	}

	// Validate required variables.
	for name, decl := range profile.Variables {
		if decl.Required {
			if _, ok := tvars.Extra[name]; !ok {
				return nil, fmt.Errorf("source: profile %q: required variable %q not provided", profileName, name)
			}
		}
	}

	var results []RenderedFile
	for _, f := range profile.Files {
		tmplPath := "profiles/" + profileName + "/" + f.Template
		tmplData, err := gitShowBytesAt(repoDir, ref, tmplPath)
		if err != nil {
			return nil, fmt.Errorf("source: read template %q: %w", f.Template, err)
		}
		rendered, err := RenderTemplate(tmplData, tvars)
		if err != nil {
			return nil, fmt.Errorf("source: render %q: %w", f.Dest, err)
		}
		// Substitute variables in dest path (e.g. /home/{{user}}/Workspace/{{workspace}}/).
		renderedDestBytes, err := RenderTemplate([]byte(f.Dest), tvars)
		if err != nil {
			return nil, fmt.Errorf("source: render dest path %q: %w", f.Dest, err)
		}
		f.Dest = strings.TrimRight(string(renderedDestBytes), "\n")
		mode := os.FileMode(0o644)
		if f.Mode != "" {
			var m uint32
			if _, err := fmt.Sscanf(f.Mode, "%o", &m); err == nil {
				mode = os.FileMode(m)
			}
		}
		results = append(results, RenderedFile{Dest: f.Dest, Content: rendered, Mode: mode})
	}
	return results, nil
}
