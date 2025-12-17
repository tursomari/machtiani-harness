package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// SyncSnapshotToHost copies whole files from snapshotRepoRoot into hostRepoRoot
// based on per-turn SHA256 baselines.
//
// Scope:
// - host tracked files
// - plus session-created files (new files created in the snapshot that are not ignored)
//
// Conflicts are detected using baseline hashes.
func SyncSnapshotToHost(snapshotRepoRoot, hostRepoRoot, patchDir string) error {
	if strings.TrimSpace(snapshotRepoRoot) == "" || strings.TrimSpace(hostRepoRoot) == "" {
		return nil
	}
	snapshotRepoRoot = filepath.Clean(snapshotRepoRoot)
	hostRepoRoot = filepath.Clean(hostRepoRoot)

	manifestPath := filepath.Join(filepath.Dir(snapshotRepoRoot), "manifests", "sync.json")
	m, err := loadOrInitManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("load sync manifest: %w", err)
	}
	ignoreCfg := m.IgnoreConfig
	if ignoreCfg == nil {
		ignoreCfg, _ = loadIgnoreConfigBestEffort()
	}

	// Build candidate set from host tracked + session created.
	candidates, err := syncCandidateSet(hostRepoRoot, m.CreatedPaths, ignoreCfg)
	if err != nil {
		return fmt.Errorf("resolve sync candidate set: %w", err)
	}

	// Detect new files created in snapshot this turn.
	snapshotFiles, err := listSnapshotFiles(snapshotRepoRoot)
	if err != nil {
		return fmt.Errorf("list snapshot files: %w", err)
	}
	for _, rel := range snapshotFiles {
		if hardExcluded(rel) || ignoredByConfig(rel, ignoreCfg) {
			continue
		}
		if _, ok := candidates[rel]; ok {
			continue
		}
		// Only adopt if it exists in snapshot and isn't in host tracked set.
		candidates[rel] = struct{}{}
		m.CreatedPaths[rel] = struct{}{}
	}

	var conflictErrs []error
	for _, rel := range relPathList(candidates) {
		if hardExcluded(rel) || ignoredByConfig(rel, ignoreCfg) {
			delete(m.CreatedPaths, rel)
			continue
		}

		base, hasBase := m.Baseline[rel]
		if !hasBase {
			base = manifestEntry{Type: "missing"}
		}
		snap, err := computeEntry(snapshotRepoRoot, rel)
		if err != nil {
			return fmt.Errorf("hash snapshot file %s: %w", rel, err)
		}
		hostNow, err := computeEntry(hostRepoRoot, rel)
		if err != nil {
			return fmt.Errorf("hash host file %s: %w", rel, err)
		}

		snapChanged := snap.SHA256 != base.SHA256 || snap.Type != base.Type
		hostChanged := hostNow.SHA256 != base.SHA256 || hostNow.Type != base.Type

		if !snapChanged {
			continue
		}
		if hostChanged && (hostNow.SHA256 != snap.SHA256 || hostNow.Type != snap.Type) {
			conflictErrs = append(conflictErrs, fmt.Errorf(
				"conflict on %s\n  baseline: %s (%s)\n  host:     %s (%s)\n  snapshot:  %s (%s)",
				rel,
				fmtEntry(base), base.Type,
				fmtEntry(hostNow), hostNow.Type,
				fmtEntry(snap), snap.Type,
			))
			continue
		}

		// Apply snapshot state to host.
		switch snap.Type {
		case "missing":
			// Never delete host paths during snapshot sync.
			//
			// The snapshot can legitimately omit files (e.g., ignored files, sandbox
			// initialization differences, or partial patch application). Treating a
			// missing snapshot entry as an instruction to delete the host copy is too
			// dangerous (it can remove critical directories like `.git` if a bad/old
			// manifest entry slips in).
			delete(m.CreatedPaths, rel)
		case "file", "symlink":
			if err := copyPath(snapshotRepoRoot, hostRepoRoot, rel); err != nil {
				return fmt.Errorf("copy snapshot->host %s: %w", rel, err)
			}
		default:
			// ignore unknown
		}
	}

	// Persist created paths for the next turn.
	m.Baseline = map[string]manifestEntry{}
	if err := saveManifest(manifestPath, m); err != nil {
		return fmt.Errorf("persist sync manifest: %w", err)
	}

	if len(conflictErrs) > 0 {
		return errors.Join(conflictErrs...)
	}
	return nil
}

func fmtEntry(e manifestEntry) string {
	if e.Type == "missing" {
		return "<missing>"
	}
	if strings.TrimSpace(e.SHA256) == "" {
		return "<unknown>"
	}
	return e.SHA256
}
