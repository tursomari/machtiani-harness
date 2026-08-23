package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

// Conversation captures the structured transcript for a session.
//
// OriginalGoal is expected to match the GOAL section rendered in the transcript
// (including any appended task description block when applicable).
//
// Timestamps are stored in RFC3339 format via the default json marshaler.
type Conversation struct {
	SessionID                string                   `json:"session_id"`
	Archived                 bool                     `json:"archived,omitempty"`
	ForkedFrom               string                   `json:"forked_from,omitempty"`
	ForkedHash               string                   `json:"forked_hash,omitempty"`
	OriginalGoal             string                   `json:"original_goal"`
	Messages                 []Message                `json:"messages"`
	CreatedAt                time.Time                `json:"created_at"`
	UpdatedAt                time.Time                `json:"updated_at"`
	ShellAgentResumable      bool                     `json:"shell_agent_resumable"`
	ShellAgentTrajectoryPath string                   `json:"shell_agent_trajectory_path,omitempty"`
	ShellAgentInterruptStep  int                      `json:"shell_agent_interrupt_step,omitempty"`
	AnswerTag                string                   `json:"answer_tag,omitempty"`
	CommandTag               string                   `json:"command_tag,omitempty"`
	ModelSelection           *ModelSelectionState     `json:"model_selection,omitempty"`
	TurnsCompleted           int                      `json:"turns_completed"`
	Goal                     string                   `json:"goal"`
	OriginalPrompt           string                   `json:"original_prompt,omitempty"`
	SuspendedUserInput       *SuspendedUserInputState `json:"suspended_user_input,omitempty"`
	PlannerProgress          *PlannerProgressState    `json:"planner_progress,omitempty"`
	Modes                    []string                 `json:"modes,omitempty"`
	ModeInstructionDir       string                   `json:"mode_instruction_dir,omitempty"`
	PlannerOverlay           string                   `json:"planner_overlay,omitempty"`
	TaskDescription          string                   `json:"task_description,omitempty"`
	Status                   string                   `json:"status,omitempty"`
	RuntimeStats             *RuntimeStatsState       `json:"runtime_stats,omitempty"`
	MagnificaHumanitas       *MagnificaHumanitas      `json:"magnifica_humanitas,omitempty"`
}

// MagnificaHumanitas records the quote selected for a session.
type MagnificaHumanitas struct {
	Paragraph int    `json:"paragraph"`
	Line      int    `json:"line"`
	Quote     string `json:"quote"`
}

func (m *MagnificaHumanitas) Clone() *MagnificaHumanitas {
	if m == nil {
		return nil
	}
	clone := *m
	return &clone
}

// ModelSelectionState records the effective model choices for a session.
// Aliases are persisted instead of credentials so a resumed session can
// resolve the same configured models without storing secrets.
type ModelSelectionState struct {
	OrchestratorAlias  string `json:"orchestrator_alias,omitempty"`
	AnswerAlias        string `json:"answer_alias,omitempty"`
	FileDiscoveryAlias string `json:"file_discovery_alias,omitempty"`
	ShellAgentAlias    string `json:"shell_agent_alias,omitempty"`
	DirectModel        string `json:"direct_model,omitempty"`
	DirectBaseURL      string `json:"direct_base_url,omitempty"`
}

func (s *ModelSelectionState) Clone() *ModelSelectionState {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

type SuspendedUserInputState struct {
	Kind        string `json:"kind,omitempty"`
	Question    string `json:"question,omitempty"`
	Context     string `json:"context,omitempty"`
	Reason      string `json:"reason,omitempty"`
	OriginalAsk string `json:"original_ask,omitempty"`
}

func (s *SuspendedUserInputState) Clone() *SuspendedUserInputState {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

// PlannerProgressState captures planner-visible progress across turns so
// retries avoid re-targeting files that were already processed successfully.
type PlannerProgressState struct {
	SuccessFiles []string `json:"success_files,omitempty"`
}

func (p *PlannerProgressState) Clone() *PlannerProgressState {
	if p == nil {
		return nil
	}
	clone := &PlannerProgressState{}
	if len(p.SuccessFiles) > 0 {
		clone.SuccessFiles = append([]string(nil), p.SuccessFiles...)
	}
	return clone
}

// RuntimeStatsState captures user-visible session runtime and token usage
// totals. Numeric fields are kept for resumability; display fields keep the
// persisted JSON readable without affecting parsing.
type RuntimeStatsState struct {
	ActiveElapsedMS           int64  `json:"active_elapsed_ms,omitempty"`
	InputHitTokens            int    `json:"input_hit_tokens,omitempty"`
	InputHitTokensDisplay     string `json:"input_hit_tokens_display,omitempty"`
	InputMissTokens           int    `json:"input_miss_tokens,omitempty"`
	InputMissTokensDisplay    string `json:"input_miss_tokens_display,omitempty"`
	OutputTokens              int    `json:"output_tokens,omitempty"`
	OutputTokensDisplay       string `json:"output_tokens_display,omitempty"`
	PlannerActivePromptTokens int    `json:"planner_active_prompt_tokens,omitempty"`
}

func NewRuntimeStatsState(activeElapsedMS int64, inputHit, inputMiss, output int) *RuntimeStatsState {
	if activeElapsedMS < 0 {
		activeElapsedMS = 0
	}
	if inputHit < 0 {
		inputHit = 0
	}
	if inputMiss < 0 {
		inputMiss = 0
	}
	if output < 0 {
		output = 0
	}
	stats := &RuntimeStatsState{
		ActiveElapsedMS: activeElapsedMS,
		InputHitTokens:  inputHit,
		InputMissTokens: inputMiss,
		OutputTokens:    output,
	}
	stats.Normalize()
	return stats
}

func (s *RuntimeStatsState) Clone() *RuntimeStatsState {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Normalize()
	return &clone
}

func (s *RuntimeStatsState) Normalize() {
	if s == nil {
		return
	}
	if s.ActiveElapsedMS < 0 {
		s.ActiveElapsedMS = 0
	}
	if s.InputHitTokens < 0 {
		s.InputHitTokens = 0
	}
	if s.InputMissTokens < 0 {
		s.InputMissTokens = 0
	}
	if s.OutputTokens < 0 {
		s.OutputTokens = 0
	}
	if s.PlannerActivePromptTokens < 0 {
		s.PlannerActivePromptTokens = 0
	}
	s.InputHitTokensDisplay = formatTokenCount(s.InputHitTokens)
	s.InputMissTokensDisplay = formatTokenCount(s.InputMissTokens)
	s.OutputTokensDisplay = formatTokenCount(s.OutputTokens)
}

func formatTokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}
	b.WriteString(s[:prefix])
	for i := prefix; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

const (
	messageTypeAsk               = "ask"
	messageTypeAnswer            = "answer"
	messageTypeWorkRequest       = "work_request"
	messageTypeWorkResult        = "work_result"
	messageTypeUserInputRequest  = "user_input_request"
	messageTypeUserInputResponse = "user_input_response"
	messageTypeCacheAnchor       = "cache_anchor"
	messageTypeRaw               = "raw"
	messageTypeRawBlock          = "raw_block"
	messageTypeRecovery          = "recovery"
	messageTypeFinal             = "final"
	messageTypeFinalAnswer       = "final_answer"

	legacyTypeOriginalGoal = "original_goal"
	legacyTypeGoalUpdate   = "goal_update"
	legacyTypeUserFeedback = "user_feedback"
	legacyTypeUserInput    = "user_input"
)

// Message represents a single event in the conversation.
// The Role field typically mirrors chat roles ("assistant", "user").
// Metadata carries typed attributes used for rendering.
type Message struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Turn      *int           `json:"turn,omitempty"`
}

// New seeds a Conversation with timestamps and the provided goal text.
func New(sessionID, originalGoal string) *Conversation {
	now := time.Now().UTC()
	conv := &Conversation{
		SessionID:    strings.TrimSpace(sessionID),
		OriginalGoal: strings.TrimSpace(originalGoal),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if conv.OriginalGoal != "" {
		conv.AddMessage("user", conv.OriginalGoal, nil)
	}
	return conv
}

// AddMessage appends a new message to the conversation, capturing metadata and
// automatically updating timestamps. The metadata map is shallow-copied.
func (c *Conversation) AddMessage(role, content string, metadata map[string]any) {
	if c == nil {
		return
	}
	ts := time.Now().UTC()
	msg := Message{
		Role:      strings.TrimSpace(role),
		Content:   content,
		Metadata:  cloneMetadata(metadata),
		Timestamp: ts,
	}
	if msg.Metadata != nil {
		if turn, ok := coerceInt(msg.Metadata["turn"]); ok {
			msg.Turn = &turn
		}
	}
	c.Messages = append(c.Messages, msg)
	c.UpdatedAt = ts
}

// InsertMessageAt inserts a message at the requested index, clamping to the
// valid slice bounds. Metadata is shallow-copied and timestamps updated.
func (c *Conversation) InsertMessageAt(index int, role, content string, metadata map[string]any) {
	if c == nil {
		return
	}
	if index < 0 {
		index = 0
	}
	if index > len(c.Messages) {
		index = len(c.Messages)
	}
	ts := time.Now().UTC()
	msg := Message{
		Role:      strings.TrimSpace(role),
		Content:   content,
		Metadata:  cloneMetadata(metadata),
		Timestamp: ts,
	}
	if msg.Metadata != nil {
		if turn, ok := coerceInt(msg.Metadata["turn"]); ok {
			msg.Turn = &turn
		}
	}
	if index == len(c.Messages) {
		c.Messages = append(c.Messages, msg)
	} else {
		c.Messages = append(c.Messages[:index], append([]Message{msg}, c.Messages[index:]...)...)
	}
	c.UpdatedAt = ts
}

// Marshal serializes the conversation to JSON with indentation.
func (c *Conversation) Marshal() ([]byte, error) {
	if c == nil {
		return nil, errors.New("conversation is nil")
	}
	if c.RuntimeStats != nil {
		c.RuntimeStats.Normalize()
	}
	return json.MarshalIndent(c, "", "  ")
}

// Unmarshal parses a conversation from JSON data.
func Unmarshal(data []byte) (*Conversation, error) {
	if len(data) == 0 {
		return nil, errors.New("conversation JSON is empty")
	}
	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("unmarshal conversation: %w", err)
	}
	if err := validateMessages(conv.Messages); err != nil {
		return nil, err
	}
	return &conv, nil
}

func validateMessages(messages []Message) error {
	for i, msg := range messages {
		switch getType(msg.Metadata) {
		case legacyTypeOriginalGoal, legacyTypeGoalUpdate, legacyTypeUserFeedback, legacyTypeUserInput:
			return fmt.Errorf("unsupported legacy conversation message type %q in message %d after simplified visible tag cutover", getType(msg.Metadata), i)
		}
	}
	return nil
}

// ToTranscript renders the full AsciiDoc transcript equivalent to the legacy
// transcript output for the stored messages.
func (c *Conversation) ToTranscript() (string, error) {
	if c == nil {
		return "", errors.New("conversation is nil")
	}

	header := renderHeader(c.OriginalGoal)

	type renderEvent struct {
		kind string // turn or raw
		turn int
		raw  string
	}

	type turnData struct {
		question  string
		answer    string
		decision  string
		savedPath string
		retrieved []string
	}

	events := []renderEvent{}
	turns := map[int]*turnData{}

	addTurnEvent := func(turn int) *turnData {
		if data, ok := turns[turn]; ok {
			return data
		}
		events = append(events, renderEvent{kind: "turn", turn: turn})
		td := &turnData{}
		turns[turn] = td
		return td
	}

	addRawEvent := func(raw string) {
		events = append(events, renderEvent{kind: "raw", raw: raw})
	}

	for _, msg := range c.Messages {
		if isHeaderGoalMessage(c, msg) {
			continue
		}
		msgType := getType(msg.Metadata)
		switch msgType {
		case "":
			switch normalizeRole(msg.Role) {
			case "user":
				addRawEvent(renderUserMessage(msg.Content))
			case "assistant":
				addRawEvent(renderAssistantMessage(msg.Content))
			}
		case messageTypeCacheAnchor:
			continue
		case messageTypeUserInputResponse:
			addRawEvent(renderUserInputResponse(msg.Content))
		case messageTypeUserInputRequest:
			addRawEvent(renderUserInputRequest(msg.Content))
		case messageTypeRaw, messageTypeRawBlock:
			if strings.TrimSpace(msg.Content) != "" {
				addRawEvent(msg.Content)
			}
		case messageTypeRecovery:
			switch normalizeRole(msg.Role) {
			case "system":
				addRawEvent(renderSystemMessage(msg.Content))
			case "user":
				addRawEvent(renderUserMessage(msg.Content))
			case "assistant":
				addRawEvent(renderAssistantMessage(msg.Content))
			}
		case messageTypeFinal, messageTypeFinalAnswer:
			turns, _ := coerceInt(msg.Metadata["turns"])
			capped, _ := coerceBool(msg.Metadata["capped"])
			block := renderConclusion(msg.Content, turns, capped)
			addRawEvent(block)
		case messageTypeAsk, messageTypeWorkRequest:
			turn := coalesceTurn(msg)
			if turn < 0 {
				continue
			}
			td := addTurnEvent(turn)
			td.question = msg.Content
			if decision, ok := coerceString(msg.Metadata["decision"]); ok {
				td.decision = decision
			}
		case messageTypeAnswer, messageTypeWorkResult:
			turn := coalesceTurn(msg)
			if turn < 0 {
				continue
			}
			td := addTurnEvent(turn)
			td.answer = msg.Content
			td.retrieved = coerceStringSlice(msg.Metadata["retrieved_files"])
			if saved, ok := coerceString(msg.Metadata["chat_path"]); ok {
				td.savedPath = saved
			}
		default:
			// Every known message type is handled explicitly above. Reaching
			// the default branch means an unrecognized type was added to the
			// conversation without a matching render rule — return an error
			// so we don't silently drop content from the transcript.
			return "", fmt.Errorf("conversation: unhandled message type %q in ToTranscript", msgType)
		}
	}

	var b strings.Builder
	b.WriteString(header)
	for _, ev := range events {
		switch ev.kind {
		case "raw":
			block := sanitize(ev.raw)
			if block == "" {
				continue
			}
			b.WriteString(block)
		case "turn":
			data := turns[ev.turn]
			if data == nil {
				continue
			}
			block := renderTurn(ev.turn, data.question, data.savedPath, data.retrieved, data.answer, data.decision)
			if block == "" {
				continue
			}
			b.WriteString(block)
		}
	}

	return sanitize(b.String()), nil
}

// ToChatMessages converts the structured conversation into chat messages.
func (c *Conversation) ToChatMessages(systemPrompt string) []llm.Message {
	trimmedSystem := strings.TrimSpace(systemPrompt)
	count := 0
	if c != nil {
		count = len(c.Messages)
	}
	if trimmedSystem != "" {
		count++
	}
	output := make([]llm.Message, 0, count)
	if trimmedSystem != "" {
		output = append(output, llm.Message{
			Role:    "system",
			Content: trimmedSystem,
			Metadata: map[string]any{
				"estimated_tokens": llm.EstimateMessageTokens(llm.Message{Content: trimmedSystem}),
			},
		})
	}
	if c == nil {
		return output
	}
	for _, msg := range c.Messages {
		msgType := getType(msg.Metadata)
		role, content, ok := serializeChatMessage(msg.Role, msgType, msg.Content)
		if !ok {
			continue
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		meta := cloneMetadata(msg.Metadata)
		if meta == nil {
			meta = map[string]any{}
		}
		if msg.Turn != nil {
			meta["turn"] = *msg.Turn
		}
		meta["estimated_tokens"] = llm.EstimateMessageTokens(llm.Message{Content: content})
		output = append(output, llm.Message{
			Role:     role,
			Content:  content,
			Metadata: meta,
		})
	}
	return output
}

func isHeaderGoalMessage(c *Conversation, msg Message) bool {
	if c == nil || getType(msg.Metadata) != "" {
		return false
	}
	if normalizeRole(msg.Role) != "user" {
		return false
	}
	goal := strings.TrimSpace(c.OriginalGoal)
	return goal != "" && strings.TrimSpace(msg.Content) == goal
}

func serializeChatMessage(role, msgType, content string) (string, string, bool) {
	if strings.TrimSpace(content) == "" {
		return "", "", false
	}
	switch msgType {
	case "":
		role = normalizeRole(role)
		if role != "user" && role != "assistant" {
			return "", "", false
		}
		return role, content, true
	case messageTypeCacheAnchor:
		return "user", content, true
	case messageTypeUserInputRequest:
		return "assistant", prefixVisibleTag(messageTypeUserInputRequest, content), true
	case messageTypeUserInputResponse:
		return "user", prefixVisibleTag(messageTypeUserInputResponse, content), true
	case messageTypeAsk, messageTypeWorkRequest:
		return "assistant", prefixVisibleTag(messageTypeWorkRequest, content), true
	case messageTypeAnswer, messageTypeWorkResult:
		return "assistant", prefixVisibleTag(messageTypeWorkResult, content), true
	case messageTypeRecovery:
		role = normalizeRole(role)
		if role != "system" && role != "user" && role != "assistant" {
			return "", "", false
		}
		return role, content, true
	case messageTypeRaw, messageTypeRawBlock, messageTypeFinal, messageTypeFinalAnswer:
		return "assistant", content, true
	default:
		return "", "", false
	}
}

func normalizeRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func prefixVisibleTag(tag, content string) string {
	return fmt.Sprintf("[%s] %s", tag, content)
}

func renderHeader(goal string) string {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("= MCT-AGENT TRANSCRIPT\n\n")
	b.WriteString("== GOAL:\n\n")
	b.WriteString(goal)
	b.WriteString("\n\n")
	return sanitize(b.String())
}

func renderTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) string {
	var b strings.Builder
	if step >= 0 {
		b.WriteString(fmt.Sprintf("\n== TURN %d\n\n", step))
	}
	b.WriteString(question)
	b.WriteString("\n\n")
	if strings.TrimSpace(savedPath) != "" {
		b.WriteString("mct chat: ")
		b.WriteString(savedPath)
		b.WriteString("\n\n")
	}
	if len(retrieved) > 0 {
		b.WriteString("Retrieved File Paths:\n")
		for _, p := range retrieved {
			b.WriteString("* ")
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if strings.TrimSpace(summary) != "" {
		b.WriteString("=== ANSWER\n\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(decision) != "" {
		b.WriteString("Planner decision: ")
		b.WriteString(decision)
		b.WriteString("\n")
	}
	return sanitize(b.String())
}

func renderUserMessage(content string) string {
	body := strings.TrimRight(content, "\n")
	return sanitize("\n=== USER MESSAGE\n\n" + body + "\n")
}

func renderAssistantMessage(content string) string {
	body := strings.TrimRight(content, "\n")
	return sanitize("\n=== ASSISTANT MESSAGE\n\n" + body + "\n")
}

func renderSystemMessage(content string) string {
	body := strings.TrimRight(content, "\n")
	return sanitize("\n=== SYSTEM MESSAGE\n\n" + body + "\n")
}

func renderUserInputResponse(content string) string {
	body := strings.TrimRight(content, "\n")
	return sanitize("\n=== USER INPUT RESPONSE\n\n" + body + "\n")
}

func renderUserInputRequest(content string) string {
	body := strings.TrimRight(content, "\n")
	return sanitize("\n=== USER INPUT REQUEST\n\n" + body + "\n")
}

func renderConclusion(answer string, step int, capped bool) string {
	var note string
	if capped {
		note = " (reached max-steps cap)"
	}
	return sanitize(fmt.Sprintf("\n== CONCLUSION%s (after %d turn(s))\n\n%s\n", note, step, answer))
}

func sanitize(text string) string {
	if text == "" {
		return text
	}
	if !strings.ContainsRune(text, '\x00') {
		return text
	}
	return strings.ReplaceAll(text, "\x00", "")
}

func cloneMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func getType(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	if val, ok := coerceString(meta["type"]); ok {
		return strings.ToLower(strings.TrimSpace(val))
	}
	return ""
}

func coalesceTurn(msg Message) int {
	if msg.Turn != nil {
		return *msg.Turn
	}
	if turn, ok := coerceInt(msg.Metadata["turn"]); ok {
		return turn
	}
	return -1
}

// MessageTurn returns the turn associated with a persisted message.
func MessageTurn(msg Message) (int, bool) {
	turn := coalesceTurn(msg)
	return turn, turn >= 0
}

func coerceInt(val any) (int, bool) {
	switch v := val.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), true
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		if i, err := strconv.Atoi(trimmed); err == nil {
			return i, true
		}
	}
	return 0, false
}

func coerceString(val any) (string, bool) {
	switch v := val.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	case fmt.Stringer:
		trimmed := strings.TrimSpace(v.String())
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	}
	return "", false
}

func coerceStringSlice(val any) []string {
	switch v := val.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out
	case []any:
		out := []string{}
		for _, item := range v {
			if s, ok := coerceString(item); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func coerceBool(val any) (bool, bool) {
	switch v := val.(type) {
	case bool:
		return v, true
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(v))
		switch trimmed {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// ToLLMMessages converts the conversation's messages into a slice of
// llm.Message suitable for the shell-agent's pre-built message prefix.
//
// The conversion uses serializeChatMessage to apply role mapping and
// [work_request]/[work_result] prefixes. Internal-only markers such as
// cache_anchor sentinels are filtered out. The resulting messages carry
// a "source":"planner" metadata entry and an estimated_tokens count.
func (c *Conversation) ToLLMMessages() []llm.Message {
	if c == nil {
		return nil
	}
	out := make([]llm.Message, 0, len(c.Messages))
	for _, msg := range c.Messages {
		msgType := getType(msg.Metadata)
		if msgType == messageTypeCacheAnchor {
			continue
		}
		role, content, ok := serializeChatMessage(msg.Role, msgType, msg.Content)
		if !ok {
			continue
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		meta := map[string]any{
			"source": "planner",
		}
		if msg.Turn != nil {
			meta["turn"] = *msg.Turn
		}
		meta["estimated_tokens"] = llm.EstimateMessageTokens(llm.Message{Content: content})
		out = append(out, llm.Message{
			Role:     role,
			Content:  content,
			Metadata: meta,
		})
	}
	return out
}
