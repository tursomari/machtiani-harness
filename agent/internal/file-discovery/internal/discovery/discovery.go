package discovery

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"encoding/json"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// Chat API types
type chatMessage struct {
	Role    string
	Content string
}

// RG protocol parsing
type rgCommand struct {
	kind    string // only "files_pattern"
	pattern string // only for files_pattern
}

func (rg *rgCommand) Execute(ctx context.Context) ([]byte, error) {
	if rg == nil {
		return nil, errors.New("rg command is nil")
	}
	if rg.kind != "files_pattern" {
		return nil, fmt.Errorf("unsupported rg kind: %s", rg.kind)
	}
	allPaths, _, err := runRGFilesFn(ctx)
	if err != nil {
		return nil, err
	}
	filtered := applyExcludes(allPaths)
	matched, err := applyPattern(filtered, rg.pattern)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(matched, "\n")), nil
}

// lastRGState tracks the most recent RG command and its RG_OUT count.
// outCount is -1 when unknown (e.g., due to timeout/error), 0 when empty,
// and >0 when non-empty.
type lastRGState struct {
	kind     string
	pattern  string
	outCount int
}

// failedRG keeps track of patterns that produced zero results previously.
// The map stores counts, and the slice preserves first-seen order for messages.
type failedRG struct {
	byPattern map[string]int
	order     []string
}

func (f *failedRG) add(pattern string) {
	if f.byPattern == nil {
		f.byPattern = map[string]int{}
	}
	if _, ok := f.byPattern[pattern]; !ok {
		f.order = append(f.order, pattern)
	}
	f.byPattern[pattern]++
}

func (f *failedRG) has(pattern string) bool {
	if f == nil || f.byPattern == nil {
		return false
	}
	_, ok := f.byPattern[pattern]
	return ok
}

// buildDuplicateNudgeMessage creates a concise nudge including the current
// repeated pattern and a short list of previously empty patterns.
func buildDuplicateNudgeMessage(current string, failedList []string) string {
	// Ensure current appears first in the display list once
	shown := []string{}
	seen := map[string]struct{}{}
	if strings.TrimSpace(current) != "" {
		shown = append(shown, current)
		seen[current] = struct{}{}
	}
	for _, p := range failedList {
		if _, ok := seen[p]; ok {
			continue
		}
		shown = append(shown, p)
		seen[p] = struct{}{}
		if len(shown) >= 5 {
			break
		} // keep message tight
	}
	// Quote items for clarity
	for i, p := range shown {
		shown[i] = fmt.Sprintf("%q", p)
	}
	list := strings.Join(shown, ", ")
	return "No results for that pattern previously. Avoid repeating empty patterns: " + list + ". Try a different rg pattern (broader keyword, different file type term, or substring)."
}

// shouldNudgeDuplicate returns true if the incoming command is identical to the
// last RG command and the last RG_OUT had zero results. Only exact equality is
// considered; non-consecutive or different commands return false.
func shouldNudgeDuplicate(last lastRGState, cur rgCommand) bool {
	// Only guard pattern searches
	if cur.kind != "files_pattern" || last.kind != "files_pattern" {
		return false
	}
	if last.outCount != 0 { // only nudge when last RG_OUT was empty
		return false
	}
	if last.pattern != cur.pattern { // exact-string equality only
		return false
	}
	return true
}

// SED protocol parsing
type sedCommand struct {
	program string // validated sed -n program (range or /pattern/)
	path    string // validated, relative path
}

func (sed *sedCommand) Execute(ctx context.Context) ([]byte, error) {
	if sed == nil {
		return nil, errors.New("sed command is nil")
	}
	lines, _, err := runSed(ctx, sed.program, sed.path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// LS protocol parsing
type lsCommand struct {
	path string // validated, relative path
}

func (ls *lsCommand) Execute(ctx context.Context) ([]byte, error) {
	if ls == nil {
		return nil, errors.New("ls command is nil")
	}
	lines, _, err := runLS(ctx, ls.path)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// Note: Only JSON tool calls are accepted; CLI-style RG>/SED>/LS> lines are no longer parsed.
var (
	reFinalBlock    = regexp.MustCompile(`(?s)BEGIN_RELEVANT_FILES\[file-discovery\](.*?)END_RELEVANT_FILES\[file-discovery\]`)
	reInvalidPathEl = regexp.MustCompile(`[\\]`)
	// sed -n program validation
	reSEDRange   = regexp.MustCompile(`^([1-9][0-9]{0,5}),([1-9][0-9]{0,5})p$`)
	reSEDPattern = regexp.MustCompile(`^/(?:[^/\\]|\\.){1,256}/p$`)
)

const maxFinalBlockRetries = 2

// Excludes
var excludeDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"dist":         {},
	"build":        {},
	"vendor":       {},
	".venv":        {},
	"__pycache__":  {},
	".next":        {},
	"target":       {},
}

var excludeExts = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".bmp": {}, ".ico": {},
	".pdf": {}, ".zip": {}, ".jar": {}, ".exe": {}, ".dll": {}, ".so": {}, ".dylib": {},
	".bin": {}, ".wasm": {}, ".ttf": {}, ".otf": {}, ".woff": {}, ".woff2": {},
	".mp4": {}, ".mov": {}, ".mp3": {}, ".wav": {},
	".gz": {}, ".tar": {}, ".tgz": {}, ".7z": {},
}

type LLMSettings struct {
	Model            llm.ResolvedModel
	Extras           map[string]any
	FallbackAliases  []string
	FallbackResolved []llm.ResolvedModel
	APIKeyOverrides  map[string]string
}

func readAllStdin(limit int) (string, bool, error) {
	var buf bytes.Buffer
	r := bufio.NewReader(os.Stdin)
	truncated := false
	for {
		chunk := make([]byte, 32*1024)
		n, err := r.Read(chunk)
		if n > 0 {
			if buf.Len()+n > limit {
				remain := limit - buf.Len()
				if remain > 0 {
					buf.Write(chunk[:remain])
				}
				truncated = true
				for err == nil {
					_, err = r.Read(chunk)
				}
				if errors.Is(err, io.EOF) {
					return buf.String(), truncated, nil
				}
				return buf.String(), truncated, nil
			}
			buf.Write(chunk[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return buf.String(), truncated, err
		}
	}
	return buf.String(), truncated, nil
}

// parse RG> lines
// JSON tool-calling parsing
// Expected shape: {"tool": "file_search"|"read_file"|"list_dir", "args": { ... }}
type toolEnvelope struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type fileSearchArgs struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
}

type readFileArgs struct {
	Program string `json:"program"`
	Path    string `json:"path"`
}

type listDirArgs struct {
	Path string `json:"path"`
}

func normalizeToolCallMode(mode cfgpkg.ToolCallMode) cfgpkg.ToolCallMode {
	switch mode {
	case cfgpkg.ToolCallModeSimple:
		return cfgpkg.ToolCallModeSimple
	case cfgpkg.ToolCallModeJSON:
		return cfgpkg.ToolCallModeJSON
	default:
		return cfgpkg.ToolCallModeJSON
	}
}

func toolCallRejectMessage(mode cfgpkg.ToolCallMode) string {
	if normalizeToolCallMode(mode) == cfgpkg.ToolCallModeSimple {
		return "Command rejected: Emit exactly one tool call using the bracket format (preferred) or JSON with valid arguments."
	}
	return "Command rejected: Use a single JSON function call: {\"tool\": \"file_search|read_file|list_dir\", \"args\": {...}} with valid arguments."
}

func toolCallMissingMessage(mode cfgpkg.ToolCallMode) string {
	if normalizeToolCallMode(mode) == cfgpkg.ToolCallModeSimple {
		return "Please emit exactly one tool call (file_search, read_file, or list_dir) using the bracket format or JSON."
	}
	return "Please emit exactly one JSON function call: file_search, read_file, or list_dir."
}

// parseSimpleToolCall parses the simplified bracket syntax and routes to the appropriate handler.
func parseSimpleToolCall(text string) (tool string, rg *rgCommand, sed *sedCommand, ls *lsCommand, reject string) {
	lines := strings.Split(text, "\n")
	start := -1
	end := -1
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		start = i
		break
	}
	if start == -1 {
		return "", nil, nil, nil, "invalid simplified tool call format"
	}
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		end = i
		break
	}
	if end <= start {
		return "", nil, nil, nil, "invalid simplified tool call format"
	}
	firstLine := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(firstLine, "[") || !strings.HasSuffix(firstLine, "]") {
		return "", nil, nil, nil, "invalid tool call start tag"
	}
	lastLine := strings.TrimSpace(lines[end])
	if lastLine != firstLine {
		return "", nil, nil, nil, "mismatched tool call tags"
	}
	toolTag := strings.TrimSpace(firstLine[1 : len(firstLine)-1])
	if toolTag == "" {
		return "", nil, nil, nil, "empty tool name in brackets"
	}
	toolName := strings.ReplaceAll(strings.ToLower(toolTag), "-", "_")
	args := make(map[string]string)
	for i := start + 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx <= 0 {
			return toolName, nil, nil, nil, "invalid argument format"
		}
		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])
		if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
			value = value[1 : len(value)-1]
		}
		args[strings.ToLower(key)] = value
	}
	switch toolName {
	case "file_search":
		kind, hasKind := args["kind"]
		pattern, hasPattern := args["pattern"]
		if !hasKind || strings.TrimSpace(kind) == "" {
			return "file_search", nil, nil, nil, "file_search.kind is required"
		}
		if strings.TrimSpace(kind) != "files_pattern" {
			return "file_search", nil, nil, nil, "file_search.kind must be 'files_pattern'"
		}
		if !hasPattern || strings.TrimSpace(pattern) == "" {
			return "file_search", nil, nil, nil, "file_search.pattern is required"
		}
		return "file_search", &rgCommand{kind: "files_pattern", pattern: pattern}, nil, nil, ""
	case "read_file":
		program, hasProgram := args["program"]
		path, hasPath := args["path"]
		program = strings.TrimSpace(program)
		path = strings.TrimSpace(path)
		if !hasProgram || program == "" {
			return "read_file", nil, nil, nil, "read_file.program is required"
		}
		if !reSEDRange.MatchString(program) && !reSEDPattern.MatchString(program) {
			return "read_file", nil, nil, nil, "read_file.program must be a range like '1,120p' or a single regex like '/pattern/p'"
		}
		if mm := reSEDRange.FindStringSubmatch(program); mm != nil {
			var s, e int
			fmt.Sscanf(mm[1], "%d", &s)
			fmt.Sscanf(mm[2], "%d", &e)
			if e < s || (e-s+1) > 200 {
				return "read_file", nil, nil, nil, "read_file.program range too large (max 200 lines)"
			}
		}
		if !hasPath || path == "" {
			return "read_file", nil, nil, nil, "read_file.path is required"
		}
		if err := validateRelPath(path); err != nil {
			return "read_file", nil, nil, nil, fmt.Sprintf("invalid path: %v", err)
		}
		return "read_file", nil, &sedCommand{program: program, path: filepath.ToSlash(path)}, nil, ""
	case "list_dir":
		path, hasPath := args["path"]
		path = strings.TrimSpace(path)
		if !hasPath || path == "" {
			return "list_dir", nil, nil, nil, "list_dir.path is required"
		}
		if err := validateRelPath(path); err != nil {
			return "list_dir", nil, nil, nil, fmt.Sprintf("invalid path: %v", err)
		}
		return "list_dir", nil, nil, &lsCommand{path: filepath.ToSlash(path)}, ""
	default:
		return toolName, nil, nil, nil, fmt.Sprintf("unknown tool: %q", toolName)
	}
}

func parseJSONToolCall(text string) (tool string, rg *rgCommand, sed *sedCommand, ls *lsCommand, reject string) {
	if extracted, ok := extractJSONObject(text); ok {
		text = extracted
	}
	var env toolEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		if cleaned, ok := tryUnescapeJSONLike(text); ok {
			if extracted, ok := extractJSONObject(cleaned); ok {
				text = extracted
			} else {
				text = cleaned
			}
			if err := json.Unmarshal([]byte(text), &env); err != nil {
				return "", nil, nil, nil, "invalid JSON for tool call"
			}
		} else {
			return "", nil, nil, nil, "invalid JSON for tool call"
		}
	}
	switch env.Tool {
	case "file_search":
		var args fileSearchArgs
		if err := json.Unmarshal(env.Args, &args); err != nil {
			return "file_search", nil, nil, nil, "invalid args for file_search"
		}
		if strings.TrimSpace(args.Kind) != "files_pattern" {
			return "file_search", nil, nil, nil, "file_search.kind must be 'files_pattern'"
		}
		if strings.TrimSpace(args.Pattern) == "" {
			return "file_search", nil, nil, nil, "file_search.pattern is required"
		}
		return "file_search", &rgCommand{kind: "files_pattern", pattern: args.Pattern}, nil, nil, ""
	case "read_file":
		var args readFileArgs
		if err := json.Unmarshal(env.Args, &args); err != nil {
			return "read_file", nil, nil, nil, "invalid args for read_file"
		}
		program := strings.TrimSpace(args.Program)
		path := strings.TrimSpace(args.Path)
		if !reSEDRange.MatchString(program) && !reSEDPattern.MatchString(program) {
			return "read_file", nil, nil, nil, "read_file.program must be a range like '1,120p' or a single regex like '/pattern/p'"
		}
		if mm := reSEDRange.FindStringSubmatch(program); mm != nil {
			var s, e int
			fmt.Sscanf(mm[1], "%d", &s)
			fmt.Sscanf(mm[2], "%d", &e)
			if e < s || (e-s+1) > 200 {
				return "read_file", nil, nil, nil, "read_file.program range too large (max 200 lines)"
			}
		}
		if err := validateRelPath(path); err != nil {
			return "read_file", nil, nil, nil, fmt.Sprintf("invalid path: %v", err)
		}
		return "read_file", nil, &sedCommand{program: program, path: filepath.ToSlash(path)}, nil, ""
	case "list_dir":
		var args listDirArgs
		if err := json.Unmarshal(env.Args, &args); err != nil {
			return "list_dir", nil, nil, nil, "invalid args for list_dir"
		}
		path := strings.TrimSpace(args.Path)
		if err := validateRelPath(path); err != nil {
			return "list_dir", nil, nil, nil, fmt.Sprintf("invalid path: %v", err)
		}
		return "list_dir", nil, nil, &lsCommand{path: filepath.ToSlash(path)}, ""
	default:
		if env.Tool == "" {
			return "", nil, nil, nil, "missing 'tool' field"
		}
		return env.Tool, nil, nil, nil, "unknown tool"
	}
}

func tryUnescapeJSONLike(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.Contains(trimmed, `\`) {
		return "", false
	}
	unescaped, err := strconv.Unquote(`"` + trimmed + `"`)
	if err != nil {
		return "", false
	}
	return unescaped, true
}

func extractJSONObject(text string) (string, bool) {
	start := strings.IndexByte(text, '{')
	if start == -1 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return "", false
			}
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return "", false
}

// parseToolCall parses a single tool-call based on the configured mode. Returns the parsed command or a reject reason.
func parseToolCall(text string, mode cfgpkg.ToolCallMode) (tool string, rg *rgCommand, sed *sedCommand, ls *lsCommand, reject string) {
	mode = normalizeToolCallMode(mode)
	s := strings.TrimSpace(text)
	if s == "" {
		return "", nil, nil, nil, "empty content"
	}
	if reFinalBlock.MatchString(s) {
		return "", nil, nil, nil, ""
	}
	trimmed := strings.TrimSpace(text)
	if mode == cfgpkg.ToolCallModeSimple && strings.HasPrefix(trimmed, "[") {
		return parseSimpleToolCall(text)
	}
	return parseJSONToolCall(s)
}

// run rg --files --hidden with timeout, collect lines
type rgStats struct {
	duration   time.Duration
	totalLines int
}

// runRGFilesFn allows tests and special builds to override the RG runner.
var runRGFilesFn = runRGFiles

func runRGFiles(ctx context.Context) ([]string, rgStats, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "rg", "--files", "--hidden")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, rgStats{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, rgStats{}, err
	}
	var lines []string
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, rgStats{}, context.DeadlineExceeded
		}
		return nil, rgStats{}, err
	}
	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, rgStats{}, context.DeadlineExceeded
		}
		return nil, rgStats{}, err
	}
	st := rgStats{duration: time.Since(start), totalLines: len(lines)}
	return lines, st, nil
}

func pathExcluded(p string) bool {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return true
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if _, ok := excludeDirs[s]; ok {
			return true
		}
	}
	ext := strings.ToLower(filepath.Ext(p))
	if _, ok := excludeExts[ext]; ok {
		return true
	}
	return false
}

func applyExcludes(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if pathExcluded(p) {
			continue
		}
		out = append(out, filepath.ToSlash(p))
	}
	return out
}

func applyPattern(paths []string, pattern string) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if re.MatchString(p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func formatRGOut(paths []string, capBytes int) (string, int, bool, int) {
	res := formatOutputBlock(paths, outputBlockOptions{
		header:             "RG_OUT:\n",
		footer:             "END_RG_OUT\n",
		truncationMessage:  "[TRUNCATED: 20 KB limit]\n",
		maxBytes:           capBytes,
		countLinesFromBody: true,
	})
	return res.formatted, res.bodyBytes, res.truncated, res.linesIncluded
}

// formatSEDOut formats SED_OUT with up to capLines lines.
func formatSEDOut(path string, lines []string, capLines int) (string, int, bool) {
	res := formatOutputBlock(lines, outputBlockOptions{
		header:            "SED_OUT[" + path + "]:\n",
		footer:            "END_SED_OUT\n",
		truncationMessage: "[TRUNCATED: 200 lines limit]\n",
		maxLines:          capLines,
	})
	return res.formatted, res.linesIncluded, res.truncated
}

// formatLSOut formats LS_OUT with up to capLines lines.
func formatLSOut(path string, lines []string, capLines int) (string, int, bool) {
	res := formatOutputBlock(lines, outputBlockOptions{
		header:            "LS_OUT[" + path + "]:\n",
		footer:            "END_LS_OUT\n",
		truncationMessage: "[TRUNCATED: 200 lines limit]\n",
		maxLines:          capLines,
	})
	return res.formatted, res.linesIncluded, res.truncated
}

// validateRelPath enforces relative path constraints used across RG/SED outputs.
func validateRelPath(p string) error {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return errors.New("empty path")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("absolute path not allowed: %q", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("contains '..': %q", p)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("trailing slash: %q", p)
	}
	if reInvalidPathEl.MatchString(p) {
		return fmt.Errorf("invalid path separators: %q", p)
	}
	return nil
}

type sedStats struct {
	duration time.Duration
}

func runSed(ctx context.Context, program, path string) ([]string, sedStats, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sed", "-n", program, "--", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, sedStats{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, sedStats{}, err
	}
	var lines []string
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= 200 { // hard cap
			// Drain but stop capturing further lines
			for scanner.Scan() {
				// ignore
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, sedStats{}, context.DeadlineExceeded
		}
		return nil, sedStats{}, err
	}
	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, sedStats{}, context.DeadlineExceeded
		}
		return nil, sedStats{}, err
	}
	st := sedStats{duration: time.Since(start)}
	return lines, st, nil
}

type lsStats struct {
	duration time.Duration
}

func runLS(ctx context.Context, path string) ([]string, lsStats, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "ls", "-la", "--", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, lsStats{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, lsStats{}, err
	}
	var lines []string
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= 200 { // hard cap
			for scanner.Scan() { /* drain */
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, lsStats{}, context.DeadlineExceeded
		}
		return nil, lsStats{}, err
	}
	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, lsStats{}, context.DeadlineExceeded
		}
		return nil, lsStats{}, err
	}
	st := lsStats{duration: time.Since(start)}
	return lines, st, nil
}

// collectPathsFromRGBlock parses an RG_OUT block and returns included paths.
func collectPathsFromRGBlock(block string) []string {
	start := strings.Index(block, "RG_OUT:\n")
	if start == -1 {
		return nil
	}
	body := block[start+len("RG_OUT:\n"):]
	end := strings.Index(body, "END_RG_OUT")
	if end != -1 {
		body = body[:end]
	}
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var out []string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "[") { // skip truncation footer
			continue
		}
		out = append(out, filepath.ToSlash(ln))
	}
	return out
}

// validateAndNormalizeFinalBlockWithLabel parses a final block emitted by the assistant
// using the legacy [file-discovery] markers, validates paths, and rewrites it to use
// the provided label for the BEGIN/END markers.
func validateAndNormalizeFinalBlockWithLabel(raw, label string) (string, error) {
	m := reFinalBlock.FindStringSubmatch(raw)
	if m == nil {
		return "", errors.New("no final block found")
	}
	inner := m[1]
	lines := strings.Split(inner, "\n")
	out := make([]string, 0, len(lines))
	seen := map[string]struct{}{}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "/") {
			return "", fmt.Errorf("Invalid output block: absolute path not allowed: %q", ln)
		}
		if strings.Contains(ln, "..") {
			return "", fmt.Errorf("Invalid output block: contains '..': %q", ln)
		}
		if strings.HasSuffix(ln, "/") {
			return "", fmt.Errorf("Invalid output block: trailing slash: %q", ln)
		}
		if reInvalidPathEl.MatchString(ln) {
			return "", fmt.Errorf("Invalid output block: invalid path separators: %q", ln)
		}
		if _, ok := seen[ln]; ok {
			continue
		}
		seen[ln] = struct{}{}
		out = append(out, ln)
	}
	if len(out) == 0 {
		return "", errors.New("Invalid output block: empty list")
	}
	var b bytes.Buffer
	b.WriteString("BEGIN_RELEVANT_FILES[")
	b.WriteString(label)
	b.WriteString("]\n")
	for _, ln := range out {
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	b.WriteString("END_RELEVANT_FILES[")
	b.WriteString(label)
	b.WriteString("]\n")
	return b.String(), nil
}

// Backward-compatible helper used by tests and internal callers that expect
// the legacy file-discovery markers.
func validateAndNormalizeFinalBlock(raw string) (string, error) {
	return validateAndNormalizeFinalBlockWithLabel(raw, "file-discovery")
}

func buildFinalBlockInvalidNudge(err error) string {
	if err == nil {
		return "The `BEGIN_RELEVANT_FILES` block was malformed or missing expected markers. Please re-emit the block using the exact `BEGIN_RELEVANT_FILES[file-discovery]\n<one relative path per line>\nEND_RELEVANT_FILES[file-discovery]` format, with only relative paths from prior `_OUT` blocks."
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "empty list"):
		return "The `BEGIN_RELEVANT_FILES` block was empty. Please ensure you include at least one valid relative file path from prior `_OUT` blocks."
	case strings.Contains(msg, "absolute path not allowed"),
		strings.Contains(msg, "contains '..'"),
		strings.Contains(msg, "trailing slash"),
		strings.Contains(msg, "invalid path separators"):
		return "The `BEGIN_RELEVANT_FILES` block contained invalid paths (e.g., absolute paths, `..`, trailing slashes, or invalid separators). Please re-emit the block with only valid relative paths from prior `_OUT` blocks, adhering to current working directory constraints."
	default:
		return "The `BEGIN_RELEVANT_FILES` block was malformed or missing expected markers. Please re-emit the block using the exact `BEGIN_RELEVANT_FILES[file-discovery]\n<one relative path per line>\nEND_RELEVANT_FILES[file-discovery]` format, with only relative paths from prior `_OUT` blocks."
	}
}

func buildFinalBlockMissingNudge() string {
	return "The `BEGIN_RELEVANT_FILES` block was missing. Please emit exactly one block using `BEGIN_RELEVANT_FILES[file-discovery]\n<one relative path per line>\nEND_RELEVANT_FILES[file-discovery]`, limited to relative paths previously shown in `_OUT` blocks."
}

// computeMarkerLabel returns the session marker label to use.
// If sessionID is empty, returns "file-discovery".
// Otherwise trims whitespace and returns the first up to 5 characters.
func computeMarkerLabel(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return "file-discovery"
	}
	// Use runes to avoid slicing in the middle of multi-byte sequences
	r := []rune(sid)
	if len(r) > 5 {
		r = r[:5]
	}
	return string(r)
}

func callChat(ctx context.Context, llmCfg LLMSettings, msgs []chatMessage) (string, error) {
	if strings.TrimSpace(llmCfg.Model.APIKey) == "" || strings.TrimSpace(llmCfg.Model.BaseURL) == "" || strings.TrimSpace(llmCfg.Model.Model) == "" {
		return "", errors.New("llm runtime not configured")
	}
	llmMsgs := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		llmMsgs[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	extras := cloneExtras(llmCfg.Extras)
	if _, ok := extras["temperature"]; !ok {
		extras["temperature"] = 1.0
	}
	if _, ok := extras["max_completion_tokens"]; !ok {
		extras["max_completion_tokens"] = 2048
	}
	chatCtx := llm.WithAPIKeyOverrides(ctx, llmCfg.APIKeyOverrides)
	return llm.ChatWithResolvedFallback(chatCtx, llmCfg.Model, llmCfg.FallbackAliases, llmCfg.FallbackResolved, extras, llmMsgs)
}

var chatInvoker = callChat

func llmCallContext(parent context.Context, timeoutSec int) (context.Context, context.CancelFunc) {
	if timeoutSec == 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, time.Duration(timeoutSec)*time.Second)
}

const exitCodeNoRelevantFiles = 2

func cloneExtras(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// withNudgeRule augments the system prompt with the duplicate-RG guidance rule.
func withNudgeRule(s string) string {
	marker := "\n\nEnd of system prompt."
	rule := "\n- Avoid repeating any file_search pattern that previously yielded no results. If a pattern comes back empty, try a broader or alternative pattern instead of repeating it."
	if strings.Contains(s, marker) {
		return strings.Replace(s, marker, rule+marker, 1)
	}
	return s + rule
}

// failNoRelevantBlockProduced centralizes logging and trajectory recording when the
// assistant exhausts final-block retries without producing a valid block.
func failNoRelevantBlockProduced(lg cfgpkg.Logger, tr *cfgpkg.TrajectoryRecorder, round int, details map[string]any) int {
	const noRelevantFilesMessage = "Could not find anything"
	lg.Warn("No relevant file block produced")
	log.Println("No relevant file block produced")
	if _, err := fmt.Fprintln(os.Stdout, noRelevantFilesMessage); err != nil {
		lg.Error("failed to write 'Could not find anything'")
	}
	if tr != nil && tr.Enabled {
		payload := map[string]any{"round": round, "exit_message": noRelevantFilesMessage}
		for k, v := range details {
			payload[k] = v
		}
		tr.Event("final_block_retry_exhausted", round, payload)
		tr.Event("run_end", round, map[string]any{"exit_code": exitCodeNoRelevantFiles, "reason": "no_relevant_files"})
		tr.Close()
	}
	return exitCodeNoRelevantFiles
}

// FixedInputTokens estimates the non-negotiable system/protocol content added
// around an embedded initial prompt.
func FixedInputTokens(mode cfgpkg.ToolCallMode) int {
	sys := withNudgeRule(buildSystemPrompt(normalizeToolCallMode(mode)))
	reminder := "Protocol reminder:\n\n" + sys
	return llm.EstimateMessagesTokens([]llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: reminder},
	})
}

// Run executes the standalone discovery loop, retaining stdin compatibility.
func Run(ctx context.Context, cfg cfgpkg.Config, llmCfg LLMSettings) int {
	return run(ctx, cfg, llmCfg, "", false)
}

// RunEmbedded executes discovery with an explicit, already-budgeted initial
// prompt and never reads process stdin.
func RunEmbedded(ctx context.Context, cfg cfgpkg.Config, llmCfg LLMSettings, initialPrompt string) int {
	return run(ctx, cfg, llmCfg, initialPrompt, true)
}

func run(ctx context.Context, cfg cfgpkg.Config, llmCfg LLMSettings, initialPrompt string, explicitPrompt bool) int {
	if ctx == nil {
		ctx = context.Background()
	}
	baseCtx := ctx
	lg := cfgpkg.Logger{JSON: cfg.LogJSON, V: cfg.Verbose}
	mode := normalizeToolCallMode(cfg.ToolCallMode)

	// Setup trajectory recorder
	var tr cfgpkg.TrajectoryRecorder
	if !cfg.NoTrajectory {
		if cfg.TrajectoryPath == "" {
			// Use only the new env var; no legacy fallback
			envPath := os.Getenv("FILE_DISCOVERY_TRAJECTORY")
			if envPath != "" {
				cfg.TrajectoryPath = envPath
			} else {
				cfg.TrajectoryPath = cfgpkg.AutoTrajectoryPath(os.Getpid())
			}
		}
		_ = tr.Start(cfg.TrajectoryPath)
	}

	// Startup checks
	if _, err := exec.LookPath("rg"); err != nil {
		lg.Error("ripgrep (rg) is required; install from https://github.com/BurntSushi/ripgrep")
		log.Println("ripgrep (rg) is required; install from https://github.com/BurntSushi/ripgrep")
		if tr.Enabled {
			tr.Event("run_end", 0, map[string]any{"exit_code": 1, "reason": "rg_not_found"})
			tr.Close()
		}
		return 1
	}

	// Dry-run mode
	if cfg.DryRunRG {
		cwd, _ := os.Getwd()
		lg.Log("dry_run_start", map[string]any{"cwd": cwd, "pattern": cfg.DryPattern})
		if !cfg.NoTrajectory && tr.Enabled {
			tr.Event("run_start", 0, map[string]any{
				"cfg":      cfgpkg.RedactConfig(cfg),
				"model":    cfg.Model,
				"base_url": cfg.BaseURL,
				"cwd":      cwd,
			})
			tr.Event("round_start", 0, map[string]any{"round": 0, "transcript_bytes_before": 0, "messages_count_before": 0})
		}
		cctx, ccancel := context.WithTimeout(baseCtx, time.Duration(cfg.CmdTimeoutSec)*time.Second)
		allPaths, stats, err := runRGFilesFn(cctx)
		ccancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("Command timed out after %ds", cfg.CmdTimeoutSec)
				if tr.Enabled {
					tr.Event("rg_exec", 0, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "total_lines": 0, "excludes_applied": true, "pattern": cfg.DryPattern})
				}
				if tr.Enabled {
					tr.Event("run_end", 0, map[string]any{"exit_code": 1, "reason": "timeout"})
					tr.Close()
				}
				return 1
			}
			log.Println("rg error:", err)
			if tr.Enabled {
				tr.Event("run_end", 0, map[string]any{"exit_code": 1, "reason": "rg_error"})
				tr.Close()
			}
			return 1
		}
		filtered := applyExcludes(allPaths)
		if cfg.DryPattern != "" {
			f2, err := applyPattern(filtered, cfg.DryPattern)
			if err != nil {
				log.Println("Invalid pattern:", err)
				return 1
			}
			filtered = f2
		}
		if tr.Enabled {
			tr.Event("rg_exec", 0, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "total_lines": stats.totalLines, "excludes_applied": true, "pattern": cfg.DryPattern})
		}
		block, bytesWritten, truncated, linesIncluded := formatRGOut(filtered, cfg.MaxStdoutBytes)
		_, _ = os.Stderr.WriteString(block)
		if tr.Enabled {
			tr.Event("rg_out_emitted", 0, map[string]any{"bytes": bytesWritten, "truncated": truncated, "lines_included": linesIncluded})
		}
		if tr.Enabled {
			tr.Event("run_end", 0, map[string]any{"exit_code": 0, "reason": "dry_run"})
			tr.Close()
		}
		return 0
	}

	// LLM-driven loop
	var messages []chatMessage
	prompt := buildSystemPrompt(mode)
	sys := withNudgeRule(prompt)
	messages = append(messages, chatMessage{Role: "system", Content: sys})
	seenPaths := map[string]struct{}{}
	// Seed with the explicit embedded prompt or standalone stdin, but always
	// prepend the system protocol reminder.
	s := initialPrompt
	truncated := false
	inputErr := error(nil)
	if !explicitPrompt {
		s, truncated, inputErr = readAllStdin(cfg.MaxTranscript)
	}
	if inputErr == nil {
		header := "Protocol reminder:"
		userContent := header + "\n\n" + sys
		if strings.TrimSpace(s) != "" {
			if truncated {
				s += "\n[TRUNCATED]"
			}
			userContent = userContent + "\n\n" + s

			// Extract seenPaths from any RG_OUT blocks in initial stdin
			// (e.g., post-patch seeding from orchestrator)
			paths := collectPathsFromRGBlock(s)
			for _, p := range paths {
				seenPaths[p] = struct{}{}
			}
			if len(paths) > 0 && tr.Enabled {
				tr.Event("seenPaths_prepopulated_from_stdin", 0, map[string]any{"count": len(paths)})
			}
		}
		messages = append(messages, chatMessage{Role: "user", Content: userContent})
	}

	transcriptBytes := 0
	// Track last RG command and its RG_OUT count
	lastRG := lastRGState{outCount: -1}
	// Track all previously empty RG patterns (not just the last one)
	var failedPatterns failedRG
	finalBlockRetries := 0
	waitingFinalBlock := false
	round := 0
	for round = 1; round <= cfg.MaxRounds; round++ {
		if tr.Enabled {
			tr.Event("round_start", round, map[string]any{"round": round, "transcript_bytes_before": transcriptBytes, "messages_count_before": len(messages)})
			tr.Event("llm_request", round, map[string]any{"messages": messages})
		}

		llmCtx, cancel := llmCallContext(baseCtx, cfg.LLMTimeoutSec)
		assistantContent, err := chatInvoker(llmCtx, llmCfg, messages)
		cancel()
		if err != nil {
			lg.Error("chat API error")
			log.Println("chat API error:", err)
			if tr.Enabled {
				tr.Event("run_end", round, map[string]any{"exit_code": 1, "reason": "chat_api_error"})
				tr.Close()
			}
			return 1
		}
		transcriptBytes += len(assistantContent)
		lg.Log("round", map[string]any{"round": round, "sent_bytes": transcriptBytes, "assistant_bytes": len(assistantContent)})
		if tr.Enabled {
			tr.Event("llm_response", round, map[string]any{"content": assistantContent})
		}

		hasFinalBlock := reFinalBlock.MatchString(assistantContent)
		if hasFinalBlock {
			waitingFinalBlock = false
			label := computeMarkerLabel(cfg.SessionID)
			normalized, err := validateAndNormalizeFinalBlockWithLabel(assistantContent, label)
			if err != nil {
				if finalBlockRetries >= maxFinalBlockRetries {
					return failNoRelevantBlockProduced(lg, &tr, round, map[string]any{"reason": "invalid_final_block", "error": err.Error(), "retries": finalBlockRetries})
				}
				msg := buildFinalBlockInvalidNudge(err)
				messages = append(messages, chatMessage{Role: "user", Content: msg})
				finalBlockRetries++
				waitingFinalBlock = true
				transcriptBytes += len(msg)
				lg.Warn("final block invalid; nudging assistant")
				if tr.Enabled {
					tr.Event("final_block_invalid", round, map[string]any{"error": err.Error(), "retry_count": finalBlockRetries})
				}
				continue
			}
			_, _ = os.Stdout.WriteString(normalized)
			if tr.Enabled {
				// Count lines between BEGIN/END for the selected label
				begin := "BEGIN_RELEVANT_FILES[" + label + "]"
				end := "END_RELEVANT_FILES[" + label + "]"
				lineCount := 0
				in := false
				for _, ln := range strings.Split(normalized, "\n") {
					if ln == begin {
						in = true
						continue
					}
					if ln == end {
						in = false
						break
					}
					if in && strings.TrimSpace(ln) != "" {
						lineCount++
					}
				}
				tr.Event("final_block_valid", round, map[string]any{"normalized_block": normalized, "normalized_block_len": len(normalized), "line_count": lineCount})
				tr.Event("run_end", round, map[string]any{"exit_code": 0, "reason": "final_block_emitted"})
				tr.Close()
			}
			return 0
		}

		if waitingFinalBlock {
			if finalBlockRetries >= maxFinalBlockRetries {
				return failNoRelevantBlockProduced(lg, &tr, round, map[string]any{"reason": "missing_final_block", "retries": finalBlockRetries})
			}
			msg := buildFinalBlockMissingNudge()
			messages = append(messages, chatMessage{Role: "user", Content: msg})
			finalBlockRetries++
			waitingFinalBlock = true
			transcriptBytes += len(msg)
			lg.Warn("final block missing; nudging assistant")
			if tr.Enabled {
				tr.Event("final_block_missing", round, map[string]any{"retry_count": finalBlockRetries})
			}
			continue
		}

		tool, rgCmd, sedCmd, lsCmd, reject := parseToolCall(assistantContent, mode)
		if tr.Enabled {
			payload := map[string]any{
				"has_final_block": hasFinalBlock,
				"tool":            tool,
				"rg_command": func() any {
					if rgCmd != nil {
						return map[string]any{"kind": rgCmd.kind, "pattern": rgCmd.pattern}
					}
					return nil
				}(),
				"sed_command": func() any {
					if sedCmd != nil {
						return map[string]any{"program": sedCmd.program, "path": sedCmd.path}
					}
					return nil
				}(),
				"ls_command": func() any {
					if lsCmd != nil {
						return map[string]any{"path": lsCmd.path}
					}
					return nil
				}(),
				"reject":         reject,
				"tool_call_mode": string(mode),
			}
			tr.Event("response_parsed", round, payload)
		}
		if reject != "" {
			messages = append(messages, chatMessage{Role: "user", Content: toolCallRejectMessage(mode)})
			transcriptBytes += len(messages[len(messages)-1].Content)
			lg.Warn("rejected invalid tool call")
			continue
		}
		if tool == "" {
			messages = append(messages, chatMessage{Role: "user", Content: toolCallMissingMessage(mode)})
			transcriptBytes += len(messages[len(messages)-1].Content)
			lg.Warn("assistant returned no tool call; prompting again")
			continue
		}

		var responseBuf bytes.Buffer
		if sedCmd != nil {
			// Enforce PATH appeared in prior RG_OUT
			if _, ok := seenPaths[sedCmd.path]; !ok {
				msg := fmt.Sprintf("read_file path not allowed: %s not found in any prior RG_OUT", sedCmd.path)
				messages = append(messages, chatMessage{Role: "user", Content: msg})
				transcriptBytes += len(msg)
				lg.Warn("sed path not in seenPaths")
			} else {
				cctx, ccancel := context.WithTimeout(baseCtx, time.Duration(cfg.CmdTimeoutSec)*time.Second)
				lines, stats, err := runSed(cctx, sedCmd.program, sedCmd.path)
				ccancel()
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Command timed out after %ds", cfg.CmdTimeoutSec)})
						transcriptBytes += len(messages[len(messages)-1].Content)
						lg.Error("sed command timeout")
						if tr.Enabled {
							tr.Event("sed_exec", round, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "program": sedCmd.program, "path": sedCmd.path})
						}
					} else {
						messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Command execution error: %v", err)})
						transcriptBytes += len(messages[len(messages)-1].Content)
						lg.Error("sed command error")
					}
				} else {
					if tr.Enabled {
						tr.Event("sed_exec", round, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "program": sedCmd.program, "path": sedCmd.path})
					}
					block, linesEmitted, truncated := formatSEDOut(sedCmd.path, lines, 200)
					responseBuf.WriteString(block)
					lg.Log("sed", map[string]any{"duration_ms": stats.duration.Milliseconds(), "lines": linesEmitted, "truncated": truncated})
					if tr.Enabled {
						tr.Event("sed_out_emitted", round, map[string]any{"lines_emitted": linesEmitted, "truncated": truncated, "path": sedCmd.path})
					}
				}
			}
		} else if lsCmd != nil {
			// LS is allowed for any validated relative PATH; no prior RG_OUT requirement
			cctx, ccancel := context.WithTimeout(baseCtx, time.Duration(cfg.CmdTimeoutSec)*time.Second)
			lines, stats, err := runLS(cctx, lsCmd.path)
			ccancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Command timed out after %ds", cfg.CmdTimeoutSec)})
					transcriptBytes += len(messages[len(messages)-1].Content)
					lg.Error("ls command timeout")
					if tr.Enabled {
						tr.Event("ls_exec", round, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "path": lsCmd.path})
					}
				} else {
					messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Command execution error: %v", err)})
					transcriptBytes += len(messages[len(messages)-1].Content)
					lg.Error("ls command error")
				}
			} else {
				if tr.Enabled {
					tr.Event("ls_exec", round, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "path": lsCmd.path})
				}
				block, linesEmitted, truncated := formatLSOut(lsCmd.path, lines, 200)
				responseBuf.WriteString(block)
				lg.Log("ls", map[string]any{"duration_ms": stats.duration.Milliseconds(), "lines": linesEmitted, "truncated": truncated})
				if tr.Enabled {
					tr.Event("ls_out_emitted", round, map[string]any{"lines_emitted": linesEmitted, "truncated": truncated, "path": lsCmd.path})
				}
			}
		} else if rgCmd != nil {
			c := *rgCmd
			// Duplicate RG guardrail: if this pattern previously yielded no results, nudge instead of re-running
			if c.kind == "files_pattern" && (failedPatterns.has(c.pattern) || shouldNudgeDuplicate(lastRG, c)) {
				msg := buildDuplicateNudgeMessage(c.pattern, failedPatterns.order)
				messages = append(messages, chatMessage{Role: "user", Content: msg})
				transcriptBytes += len(msg)
				lg.Log("duplicate_rg_nudged", map[string]any{"kind": c.kind, "pattern": c.pattern, "last_out_count": lastRG.outCount, "failed_patterns": failedPatterns.order})
				if tr.Enabled {
					tr.Event("duplicate_rg_nudged", round, map[string]any{"kind": c.kind, "pattern": c.pattern, "last_out_count": lastRG.outCount, "failed_patterns": failedPatterns.order})
				}
				responseBuf.Reset()
			} else {
				cctx, ccancel := context.WithTimeout(baseCtx, time.Duration(cfg.CmdTimeoutSec)*time.Second)
				allPaths, stats, err := runRGFiles(cctx)
				ccancel()
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Command timed out after %ds", cfg.CmdTimeoutSec)})
						transcriptBytes += len(messages[len(messages)-1].Content)
						lg.Error("rg command timeout")
						responseBuf.Reset()
						if tr.Enabled {
							tr.Event("rg_exec", round, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "total_lines": 0, "excludes_applied": true, "pattern": c.pattern})
						}
					} else {
						messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Command execution error: %v", err)})
						transcriptBytes += len(messages[len(messages)-1].Content)
						lg.Error("rg command error")
						responseBuf.Reset()
						// Do not update lastRG on error; preserve prior state
					}
				} else {
					filtered := applyExcludes(allPaths)
					if c.kind == "files_pattern" {
						filtered2, err := applyPattern(filtered, c.pattern)
						if err != nil {
							messages = append(messages, chatMessage{Role: "user", Content: fmt.Sprintf("Invalid pattern: %v", err)})
							transcriptBytes += len(messages[len(messages)-1].Content)
							lg.Warn("invalid pattern; informing assistant")
							responseBuf.Reset()
						} else {
							filtered = filtered2
							if tr.Enabled {
								tr.Event("rg_exec", round, map[string]any{"timeout_sec": cfg.CmdTimeoutSec, "duration_ms": stats.duration.Milliseconds(), "total_lines": len(allPaths), "excludes_applied": true, "pattern": c.pattern})
							}
							block, bodyBytes, truncated, linesCount := formatRGOut(filtered, cfg.MaxStdoutBytes)
							responseBuf.WriteString(block)
							lg.Log("rg", map[string]any{"duration_ms": stats.duration.Milliseconds(), "lines": len(filtered), "bytes": bodyBytes, "truncated": truncated})
							if tr.Enabled {
								tr.Event("rg_out_emitted", round, map[string]any{"body": block, "bytes": bodyBytes, "truncated": truncated, "lines_count": linesCount})
							}
							// Update seenPaths with the lines we actually emitted
							for _, p := range collectPathsFromRGBlock(block) {
								seenPaths[p] = struct{}{}
							}
							// Update last RG state
							lastRG = lastRGState{kind: c.kind, pattern: c.pattern, outCount: linesCount}
							// Track failed pattern if no results
							if c.kind == "files_pattern" && linesCount == 0 {
								failedPatterns.add(c.pattern)
							}
						}
					}
				}
			}
		}

		if responseBuf.Len() > 0 {
			reply := responseBuf.String()
			messages = append(messages, chatMessage{Role: "user", Content: reply})
			transcriptBytes += len(reply)
			if tr.Enabled {
				tr.Event("user_reply_appended", round, map[string]any{"body": reply, "bytes": len(reply)})
			}
		}
	}

	// Forced finalization attempt: keep nudging for a valid final block up to the retry limit.
	finalMsg := "Max rounds reached. Do NOT call any function. Immediately output exactly one block using these markers and nothing else, based on prior RG_OUT/SED_OUT/LS_OUT:\n" + relevantFilesBlockMarkers + "\nUse only relative paths previously shown in *_OUT; avoid duplicates, directories, and excluded junk. No other text."
	messages = append(messages, chatMessage{Role: "user", Content: finalMsg})
	transcriptBytes += len(finalMsg)
	waitingFinalBlock = true

	for {
		if finalBlockRetries > maxFinalBlockRetries {
			return failNoRelevantBlockProduced(lg, &tr, cfg.MaxRounds+finalBlockRetries, map[string]any{"reason": "retry_limit_exceeded", "forced_finalization": true, "retries": finalBlockRetries})
		}
		forcedRound := cfg.MaxRounds + finalBlockRetries + 1
		if tr.Enabled {
			tr.Event("round_start", forcedRound, map[string]any{"round": forcedRound, "transcript_bytes_before": transcriptBytes, "messages_count_before": len(messages), "forced_finalization": true, "retry_count": finalBlockRetries})
			tr.Event("llm_request", forcedRound, map[string]any{"messages": messages, "forced_finalization": true, "retry_count": finalBlockRetries})
		}

		llmCtx, cancel := llmCallContext(baseCtx, cfg.LLMTimeoutSec)
		assistantContent, err := chatInvoker(llmCtx, llmCfg, messages)
		cancel()
		if err != nil {
			lg.Error("chat API error on forced finalization")
			log.Println("chat API error:", err)
			if tr.Enabled {
				tr.Event("run_end", forcedRound, map[string]any{"exit_code": 1, "reason": "chat_api_error_forced", "retry_count": finalBlockRetries})
				tr.Close()
			}
			return 1
		}
		transcriptBytes += len(assistantContent)
		lg.Log("round", map[string]any{"round": forcedRound, "sent_bytes": transcriptBytes, "assistant_bytes": len(assistantContent), "forced_finalization": true, "retry_count": finalBlockRetries})
		if tr.Enabled {
			tr.Event("llm_response", forcedRound, map[string]any{"content": assistantContent, "forced_finalization": true, "retry_count": finalBlockRetries})
		}

		// Enforce finalization-only: ignore any function calls in this extra round.
		if tool, rgCmd, sedCmd, lsCmd, reject := parseToolCall(assistantContent, mode); tool != "" || rgCmd != nil || sedCmd != nil || lsCmd != nil || reject != "" {
			lg.Warn("forced finalization: function call present; ignoring")
			if tr.Enabled {
				tr.Event("forced_finalization_rejected_commands", forcedRound, map[string]any{"tool": tool, "reject": reject, "has_rg": rgCmd != nil, "has_sed": sedCmd != nil, "has_ls": lsCmd != nil, "retry_count": finalBlockRetries})
			}
		}

		hasFinalBlock := reFinalBlock.MatchString(assistantContent)
		if hasFinalBlock {
			waitingFinalBlock = false
			label := computeMarkerLabel(cfg.SessionID)
			normalized, err := validateAndNormalizeFinalBlockWithLabel(assistantContent, label)
			if err != nil {
				if finalBlockRetries >= maxFinalBlockRetries {
					return failNoRelevantBlockProduced(lg, &tr, forcedRound, map[string]any{"reason": "invalid_final_block", "error": err.Error(), "retries": finalBlockRetries, "forced_finalization": true})
				}
				msg := buildFinalBlockInvalidNudge(err)
				messages = append(messages, chatMessage{Role: "user", Content: msg})
				finalBlockRetries++
				waitingFinalBlock = true
				transcriptBytes += len(msg)
				lg.Warn("final block invalid during forced finalization; nudging assistant")
				if tr.Enabled {
					tr.Event("final_block_invalid", forcedRound, map[string]any{"error": err.Error(), "retry_count": finalBlockRetries, "forced_finalization": true})
				}
				continue
			}
			_, _ = os.Stdout.WriteString(normalized)
			if tr.Enabled {
				// Count lines between BEGIN/END for the selected label
				begin := "BEGIN_RELEVANT_FILES[" + label + "]"
				end := "END_RELEVANT_FILES[" + label + "]"
				lineCount := 0
				in := false
				for _, ln := range strings.Split(normalized, "\n") {
					if ln == begin {
						in = true
						continue
					}
					if ln == end {
						in = false
						break
					}
					if in && strings.TrimSpace(ln) != "" {
						lineCount++
					}
				}
				tr.Event("final_block_valid", forcedRound, map[string]any{"normalized_block": normalized, "normalized_block_len": len(normalized), "line_count": lineCount, "forced_finalization": true})
				tr.Event("run_end", forcedRound, map[string]any{"exit_code": 0, "reason": "final_block_emitted_forced", "retry_count": finalBlockRetries})
				tr.Close()
			}
			return 0
		}

		if waitingFinalBlock {
			if finalBlockRetries >= maxFinalBlockRetries {
				return failNoRelevantBlockProduced(lg, &tr, forcedRound, map[string]any{"reason": "missing_final_block", "retries": finalBlockRetries, "forced_finalization": true})
			}
			msg := buildFinalBlockMissingNudge()
			messages = append(messages, chatMessage{Role: "user", Content: msg})
			finalBlockRetries++
			waitingFinalBlock = true
			transcriptBytes += len(msg)
			lg.Warn("final block missing during forced finalization; nudging assistant")
			if tr.Enabled {
				tr.Event("final_block_missing", forcedRound, map[string]any{"retry_count": finalBlockRetries, "forced_finalization": true})
			}
			continue
		}
	}
}
