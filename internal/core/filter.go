package core

import "github.com/aissat/sysfig/pkg/types"

// filterRecords returns the subset of files matching the ids, tags, and paths
// criteria. An empty slice means "no filter" (include all records). The
// returned map preserves the id → record mapping from the input.
//
// This is the canonical ID+Tag+Path filter shared by Apply, RemoteDeploy,
// and similar operations that accept --id / --tag / --path flags.
func filterRecords(files map[string]*types.FileRecord, ids, tags, paths []string) map[string]*types.FileRecord {
	if len(ids) == 0 && len(tags) == 0 && len(paths) == 0 {
		return files
	}
	wantIDs := make(map[string]bool, len(ids))
	for _, id := range ids {
		wantIDs[id] = true
	}
	wantPaths := make(map[string]bool, len(paths))
	for _, p := range paths {
		wantPaths[p] = true
	}
	wantTags := make(map[string]bool, len(tags))
	for _, t := range tags {
		wantTags[t] = true
	}
	result := make(map[string]*types.FileRecord)
	for id, rec := range files {
		if len(ids) > 0 && !(wantIDs[id] || hasIDPrefixInSet(id, wantIDs)) {
			continue
		}
		if len(paths) > 0 && !wantPaths[rec.SystemPath] {
			continue
		}
		if len(wantTags) > 0 {
			effectiveTags := rec.Tags
			if len(effectiveTags) == 0 {
				effectiveTags = DetectPlatformTags()
			}
			if !fileHasTag(effectiveTags, wantTags) {
				continue
			}
		}
		result[id] = rec
	}
	return result
}
