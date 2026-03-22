package core

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/aissat/sysfig/internal/state"
	"github.com/aissat/sysfig/pkg/types"
)

// ── TagList ───────────────────────────────────────────────────────────────────

// TagListOptions configures a tag list operation.
type TagListOptions struct {
	BaseDir string
}

// TagEntry is one row in the tag list result.
type TagEntry struct {
	Tag   string
	Count int
}

// TagListResult is the outcome of TagList.
type TagListResult struct {
	Entries  []TagEntry // sorted by tag name
	Untagged int        // files with no explicit tags
}

// TagList reads state.json and returns all explicit tags with per-tag file counts.
func TagList(opts TagListOptions) (*TagListResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: tag list: BaseDir must not be empty")
	}
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)
	s, err := sm.Load()
	if err != nil {
		return nil, fmt.Errorf("core: tag list: %w", err)
	}

	counts := map[string]int{}
	untagged := 0
	for _, rec := range s.Files {
		if len(rec.Tags) == 0 {
			untagged++
			continue
		}
		for _, t := range rec.Tags {
			counts[t]++
		}
	}

	result := &TagListResult{Untagged: untagged}
	for tag, count := range counts {
		result.Entries = append(result.Entries, TagEntry{tag, count})
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		return result.Entries[i].Tag < result.Entries[j].Tag
	})
	return result, nil
}

// ── TagAuto ───────────────────────────────────────────────────────────────────

// TagAutoOptions configures an auto-tag operation.
type TagAutoOptions struct {
	BaseDir   string
	Overwrite bool // if true, rewrite tags on already-tagged files too
}

// TagAutoResult is the outcome of TagAuto.
type TagAutoResult struct {
	Updated int
	Skipped int
}

// TagAuto writes DetectPlatformTags() to files in state.json that have no
// explicit tags. With Overwrite=true, rewrites tags on all files.
func TagAuto(opts TagAutoOptions) (*TagAutoResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: tag auto: BaseDir must not be empty")
	}
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	platformTags := DetectPlatformTags()
	result := &TagAutoResult{}

	return result, sm.WithLock(func(s *types.State) error {
		for _, rec := range s.Files {
			if !opts.Overwrite && len(rec.Tags) > 0 {
				result.Skipped++
				continue
			}
			rec.Tags = platformTags
			result.Updated++
		}
		return nil
	})
}

// ── TagSet ────────────────────────────────────────────────────────────────────

// TagSetOptions configures setting tags on a single tracked file.
type TagSetOptions struct {
	BaseDir  string
	PathOrID string   // absolute system path or 8-char ID prefix
	Tags     []string // replaces existing tags; nil/empty clears all tags
}

// TagSetResult is the outcome of TagSet.
type TagSetResult struct {
	ID         string
	SystemPath string
	OldTags    []string
	NewTags    []string
}

// TagSet replaces the tags on a single tracked file identified by path or ID.
func TagSet(opts TagSetOptions) (*TagSetResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: tag set: BaseDir must not be empty")
	}
	if opts.PathOrID == "" {
		return nil, fmt.Errorf("core: tag set: path or ID is required")
	}
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	var result *TagSetResult
	return result, sm.WithLock(func(s *types.State) error {
		for id, rec := range s.Files {
			if rec.SystemPath != opts.PathOrID && id != opts.PathOrID && !hasIDPrefix(id, opts.PathOrID) {
				continue
			}
			old := make([]string, len(rec.Tags))
			copy(old, rec.Tags)
			rec.Tags = opts.Tags
			result = &TagSetResult{
				ID:         id,
				SystemPath: rec.SystemPath,
				OldTags:    old,
				NewTags:    opts.Tags,
			}
			return nil
		}
		return fmt.Errorf("core: tag set: no tracked file found for %q", opts.PathOrID)
	})
}

// ── TagRename ─────────────────────────────────────────────────────────────────

// TagRenameOptions configures a tag rename operation.
type TagRenameOptions struct {
	BaseDir string
	OldTag  string
	NewTag  string
}

// TagRenameResult is the outcome of TagRename.
type TagRenameResult struct {
	Updated int // number of files whose tag list was changed
}

// TagRename renames a tag across all tracked files in state.json.
func TagRename(opts TagRenameOptions) (*TagRenameResult, error) {
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("core: tag rename: BaseDir must not be empty")
	}
	if opts.OldTag == "" || opts.NewTag == "" {
		return nil, fmt.Errorf("core: tag rename: old and new tag names are required")
	}
	statePath := filepath.Join(opts.BaseDir, "state.json")
	sm := state.NewManager(statePath)

	result := &TagRenameResult{}
	return result, sm.WithLock(func(s *types.State) error {
		for _, rec := range s.Files {
			changed := false
			seen := map[string]bool{}
			var updated []string
			for _, t := range rec.Tags {
				effective := t
				if t == opts.OldTag {
					effective = opts.NewTag
					changed = true
				}
				if !seen[effective] {
					seen[effective] = true
					updated = append(updated, effective)
				}
			}
			if changed {
				rec.Tags = updated
				result.Updated++
			}
		}
		return nil
	})
}

// hasIDPrefix reports whether the full 32-char hex id starts with prefix.
// Only matches when prefix is at least 4 characters.
func hasIDPrefix(id, prefix string) bool {
	return len(prefix) >= 4 && len(id) >= len(prefix) && id[:len(prefix)] == prefix
}
