// Package fileops provides read-only, workspace-rooted file discovery operations.
package fileops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/git-pkgs/gitignore"
)

const (
	maxSymlinks    = 32
	maxLineBytes   = 1024 * 1024
	maxReadResults = 200
	maxDiagnostics = 128
)

var excludedDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "dist": {}, "build": {}, "vendor": {},
	".venv": {}, "__pycache__": {}, ".next": {}, "target": {},
}

var excludedExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".bmp": {}, ".ico": {},
	".pdf": {}, ".zip": {}, ".jar": {}, ".exe": {}, ".dll": {}, ".so": {}, ".dylib": {},
	".bin": {}, ".wasm": {}, ".ttf": {}, ".otf": {}, ".woff": {}, ".woff2": {},
	".mp4": {}, ".mov": {}, ".mp3": {}, ".wav": {},
	".gz": {}, ".tar": {}, ".tgz": {}, ".7z": {},
}

// SkipReason classifies a path omitted during enumeration.
type SkipReason string

const (
	SkipBroken       SkipReason = "broken_symlink"
	SkipOutside      SkipReason = "outside_workspace"
	SkipCycle        SkipReason = "symlink_cycle"
	SkipTooManyLinks SkipReason = "too_many_symlinks"
	SkipSpecial      SkipReason = "special_file"
	SkipUnsafeName   SkipReason = "unsafe_name"
	SkipIgnored      SkipReason = "ignored"
	SkipExcluded     SkipReason = "hard_excluded"
	SkipInvalidRule  SkipReason = "invalid_ignore_rule"
	SkipUnreadable   SkipReason = "unreadable"
)

// Diagnostic records a bounded, non-fatal traversal problem.
type Diagnostic struct {
	Path   string
	Reason SkipReason
	Detail string
}

// Stats describes one native operation.
type Stats struct {
	Duration    time.Duration
	TotalFiles  int
	Skipped     map[SkipReason]int
	Diagnostics []Diagnostic
}

func (s *Stats) add(reason SkipReason, name string, err error) {
	if s.Skipped == nil {
		s.Skipped = make(map[SkipReason]int)
	}
	s.Skipped[reason]++
	if len(s.Diagnostics) >= maxDiagnostics {
		return
	}
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	s.Diagnostics = append(s.Diagnostics, Diagnostic{Path: name, Reason: reason, Detail: detail})
}

// Selector chooses either an inclusive line range or regexp matches.
type Selector struct {
	StartLine int
	EndLine   int
	Pattern   *regexp.Regexp
}

// Entry is one deterministic list_dir record.
type Entry struct {
	Name       string
	Kind       string
	Mode       fs.FileMode
	Size       int64
	ModTime    time.Time
	LinkTarget string
}

// Line returns the stable tab-delimited list_dir representation.
func (e Entry) Line() string {
	size := "-"
	if !e.Mode.IsDir() {
		size = strconv.FormatInt(e.Size, 10)
	}
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", e.Kind, e.Mode.String(), size, e.ModTime.UTC().Format(time.RFC3339Nano), strconv.Quote(e.Name))
	if e.LinkTarget != "" {
		line += "\t->\t" + strconv.Quote(e.LinkTarget)
	}
	return line
}

// PathError classifies a rooted resolution failure.
type PathError struct {
	Path   string
	Reason SkipReason
	Err    error
}

func (e *PathError) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Path, e.Reason, e.Err)
}

func (e *PathError) Unwrap() error { return e.Err }

// Workspace owns a read-only os.Root for the lifetime of discovery.
type Workspace struct {
	root          *os.Root
	canonicalRoot string
}

// Open resolves root once and opens a read-only rooted filesystem handle.
func Open(rootPath string) (*Workspace, error) {
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("absolute workspace root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	st, err := os.Stat(canonical)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", canonical)
	}
	r, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	return &Workspace{root: r, canonicalRoot: canonical}, nil
}

// Close releases the rooted filesystem handle.
func (w *Workspace) Close() error { return w.root.Close() }

func cleanRelative(name string) (string, error) {
	name = filepath.ToSlash(strings.TrimSpace(name))
	if name == "" {
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return "", errors.New("absolute path not allowed")
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(clean, 0) {
		return "", errors.New("path escapes workspace")
	}
	return clean, nil
}

func splitPath(name string) []string {
	if name == "." || name == "" {
		return nil
	}
	return strings.Split(name, "/")
}

func insideRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolve follows symlinks component-by-component without ever opening an
// out-of-root target. It returns a slash-separated physical path for os.Root.
func (w *Workspace) resolve(name string) (string, os.FileInfo, error) {
	clean, err := cleanRelative(name)
	if err != nil {
		return "", nil, &PathError{Path: name, Reason: SkipOutside, Err: err}
	}
	if clean == "." {
		st, statErr := w.root.Stat(".")
		return ".", st, statErr
	}

	pending := splitPath(clean)
	resolved := make([]string, 0, len(pending))
	seenLinks := make(map[string]struct{})
	links := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		candidateParts := append(append([]string(nil), resolved...), component)
		candidate := strings.Join(candidateParts, "/")
		info, statErr := w.root.Lstat(candidate)
		if statErr != nil {
			reason := SkipUnreadable
			if errors.Is(statErr, fs.ErrNotExist) {
				reason = SkipBroken
			}
			return "", nil, &PathError{Path: name, Reason: reason, Err: statErr}
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if len(pending) > 0 && !info.IsDir() {
				return "", nil, &PathError{Path: name, Reason: SkipBroken, Err: errors.New("non-directory path component")}
			}
			resolved = append(resolved, component)
			if len(pending) == 0 {
				return strings.Join(resolved, "/"), info, nil
			}
			continue
		}

		links++
		if links > maxSymlinks {
			return "", nil, &PathError{Path: name, Reason: SkipTooManyLinks, Err: errors.New("symlink depth exceeds 32")}
		}
		if _, ok := seenLinks[candidate]; ok {
			return "", nil, &PathError{Path: name, Reason: SkipCycle, Err: errors.New("symlink cycle")}
		}
		seenLinks[candidate] = struct{}{}
		target, readErr := w.root.Readlink(candidate)
		if readErr != nil {
			return "", nil, &PathError{Path: name, Reason: SkipUnreadable, Err: readErr}
		}

		var targetRel string
		if filepath.IsAbs(target) {
			absolute := filepath.Clean(target)
			if !insideRoot(w.canonicalRoot, absolute) {
				return "", nil, &PathError{Path: name, Reason: SkipOutside, Err: errors.New("absolute symlink target is outside workspace")}
			}
			targetRel, err = filepath.Rel(w.canonicalRoot, absolute)
			if err != nil {
				return "", nil, &PathError{Path: name, Reason: SkipOutside, Err: err}
			}
			targetRel = filepath.ToSlash(targetRel)
		} else {
			base := strings.Join(resolved, "/")
			targetRel = path.Clean(path.Join(base, filepath.ToSlash(target)))
			if targetRel == ".." || strings.HasPrefix(targetRel, "../") {
				return "", nil, &PathError{Path: name, Reason: SkipOutside, Err: errors.New("relative symlink target is outside workspace")}
			}
		}
		pending = append(splitPath(targetRel), pending...)
		resolved = resolved[:0]
		if len(pending) == 0 {
			st, statErr := w.root.Stat(".")
			return ".", st, statErr
		}
	}
	return "", nil, &PathError{Path: name, Reason: SkipBroken, Err: errors.New("unresolved path")}
}

func protocolSafe(name string) bool {
	if !utf8.ValidString(name) || strings.Contains(name, "END_RG_OUT") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func hardExcluded(name string, isDir bool) bool {
	for _, segment := range splitPath(filepath.ToSlash(name)) {
		if _, ok := excludedDirs[segment]; ok {
			return true
		}
	}
	if isDir {
		return false
	}
	_, ok := excludedExts[strings.ToLower(path.Ext(filepath.ToSlash(name)))]
	return ok
}

type ignoreSet struct {
	git *gitignore.Matcher
	all *gitignore.Matcher
	rg  *gitignore.Matcher
}

func newIgnoreSet() *ignoreSet {
	// Deliberately use zero-value matchers. Constructors in this dependency may
	// inspect Git/global files; workspace discovery must only load rooted files.
	return &ignoreSet{git: new(gitignore.Matcher), all: new(gitignore.Matcher), rg: new(gitignore.Matcher)}
}

func (i *ignoreSet) ignored(name string, isDir bool) bool {
	matchName := name
	if isDir {
		matchName += "/"
	}
	for _, matcher := range []*gitignore.Matcher{i.rg, i.all, i.git} {
		result := matcher.MatchDetail(matchName)
		if result.Matched {
			return result.Ignored
		}
	}
	return false
}

func (w *Workspace) readPhysical(name string) ([]byte, error) {
	f, err := w.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (w *Workspace) loadIgnores(logicalDir string, ignores *ignoreSet, stats *Stats) {
	for _, spec := range []struct {
		name    string
		matcher *gitignore.Matcher
	}{
		{name: ".gitignore", matcher: ignores.git},
		{name: ".ignore", matcher: ignores.all},
		{name: ".rgignore", matcher: ignores.rg},
	} {
		logical := path.Join(logicalDir, spec.name)
		if logicalDir == "" {
			logical = spec.name
		}
		physical, info, err := w.resolve(logical)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := w.readPhysical(physical)
		if err != nil {
			stats.add(SkipUnreadable, logical, err)
			continue
		}
		before := len(spec.matcher.Errors())
		spec.matcher.AddPatterns(data, logicalDir)
		for _, patternErr := range spec.matcher.Errors()[before:] {
			stats.add(SkipInvalidRule, logical, patternErr)
		}
	}
}

func reasonFrom(err error) SkipReason {
	var pathErr *PathError
	if errors.As(err, &pathErr) {
		return pathErr.Reason
	}
	return SkipUnreadable
}

// ListFiles returns deterministic logical paths for source-like regular files.
func (w *Workspace) ListFiles(ctx context.Context) ([]string, Stats, error) {
	start := time.Now()
	stats := Stats{Skipped: make(map[SkipReason]int)}
	ignores := newIgnoreSet()
	rootInfo, err := w.root.Stat(".")
	if err != nil {
		return nil, stats, err
	}
	var files []string
	var walk func(string, string, []os.FileInfo) error
	walk = func(logicalDir, physicalDir string, ancestors []os.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.loadIgnores(logicalDir, ignores, &stats)
		dir, err := w.root.Open(physicalDir)
		if err != nil {
			stats.add(SkipUnreadable, logicalDir, err)
			return nil
		}
		entries, readErr := dir.ReadDir(-1)
		_ = dir.Close()
		if readErr != nil {
			stats.add(SkipUnreadable, logicalDir, readErr)
			return nil
		}
		sort.Slice(entries, func(a, b int) bool { return entries[a].Name() < entries[b].Name() })
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			logical := entry.Name()
			if logicalDir != "" {
				logical = logicalDir + "/" + entry.Name()
			}
			if !protocolSafe(logical) {
				stats.add(SkipUnsafeName, logical, nil)
				continue
			}
			physical, info, resolveErr := w.resolve(logical)
			if resolveErr != nil {
				stats.add(reasonFrom(resolveErr), logical, resolveErr)
				continue
			}
			isDir := info.IsDir()
			if hardExcluded(logical, isDir) || hardExcluded(physical, isDir) {
				stats.add(SkipExcluded, logical, nil)
				continue
			}
			if ignores.ignored(logical, isDir) {
				stats.add(SkipIgnored, logical, nil)
				continue
			}
			if isDir {
				cycle := false
				for _, ancestor := range ancestors {
					if os.SameFile(ancestor, info) {
						cycle = true
						break
					}
				}
				if cycle {
					stats.add(SkipCycle, logical, nil)
					continue
				}
				if err := walk(logical, physical, append(ancestors, info)); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				stats.add(SkipSpecial, logical, nil)
				continue
			}
			files = append(files, logical)
		}
		return nil
	}
	if err := walk("", ".", []os.FileInfo{rootInfo}); err != nil {
		stats.Duration = time.Since(start)
		return nil, stats, err
	}
	sort.Strings(files)
	stats.TotalFiles = len(files)
	stats.Duration = time.Since(start)
	return files, stats, nil
}

// ReadLines reads a regular file selected by range or regexp.
func (w *Workspace) ReadLines(ctx context.Context, name string, selector Selector) ([]string, Stats, error) {
	start := time.Now()
	stats := Stats{Skipped: make(map[SkipReason]int)}
	physical, info, err := w.resolve(name)
	if err != nil {
		return nil, stats, err
	}
	if !info.Mode().IsRegular() {
		return nil, stats, fmt.Errorf("read_file target is not a regular file: %s", name)
	}
	if selector.Pattern == nil && (selector.StartLine < 1 || selector.EndLine < selector.StartLine || selector.EndLine-selector.StartLine+1 > maxReadResults) {
		return nil, stats, errors.New("invalid line range")
	}
	f, err := w.root.Open(physical)
	if err != nil {
		return nil, stats, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	lines := make([]string, 0, maxReadResults)
	lineNumber := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		lineNumber++
		line := scanner.Text()
		if selector.Pattern != nil {
			if len(lines) < maxReadResults && selector.Pattern.MatchString(line) {
				lines = append(lines, line)
			}
			continue
		}
		if lineNumber >= selector.StartLine && lineNumber <= selector.EndLine {
			lines = append(lines, line)
		}
		if lineNumber >= selector.EndLine {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, stats, err
	}
	stats.Duration = time.Since(start)
	return lines, stats, nil
}

func kindFor(info os.FileInfo) string {
	switch {
	case info.IsDir():
		return "dir"
	case info.Mode().IsRegular():
		return "file"
	default:
		return "special"
	}
}

func (w *Workspace) entry(logical, physical, displayName string) Entry {
	info, err := w.root.Lstat(physical)
	if err != nil {
		return Entry{Name: displayName, Kind: "unreadable"}
	}
	entry := Entry{Name: displayName, Kind: kindFor(info), Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime()}
	if info.Mode()&os.ModeSymlink == 0 {
		return entry
	}
	entry.LinkTarget, _ = w.root.Readlink(physical)
	_, resolved, resolveErr := w.resolve(logical)
	if resolveErr != nil {
		entry.Kind = "symlink:" + string(reasonFrom(resolveErr))
		return entry
	}
	entry.Kind = "symlink:" + kindFor(resolved)
	return entry
}

// ListDir returns sorted immediate children, following an in-root directory symlink.
func (w *Workspace) ListDir(ctx context.Context, name string) ([]Entry, Stats, error) {
	start := time.Now()
	stats := Stats{Skipped: make(map[SkipReason]int)}
	logical, err := cleanRelative(name)
	if err != nil {
		return nil, stats, err
	}
	physical, info, err := w.resolve(logical)
	if err != nil {
		return nil, stats, err
	}
	if !info.IsDir() {
		entry := w.entry(logical, physical, path.Base(logical))
		stats.Duration = time.Since(start)
		return []Entry{entry}, stats, nil
	}
	dir, err := w.root.Open(physical)
	if err != nil {
		return nil, stats, err
	}
	children, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return nil, stats, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		childLogical := path.Join(logical, child.Name())
		if logical == "." {
			childLogical = child.Name()
		}
		childPhysical := path.Join(physical, child.Name())
		entries = append(entries, w.entry(childLogical, childPhysical, child.Name()))
	}
	stats.Duration = time.Since(start)
	return entries, stats, nil
}
