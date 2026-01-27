package conversation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Conversation captures the structured transcript for a session.
//
// OriginalGoal is expected to match the GOAL section rendered in the transcript
// (including any appended task description block when applicable).
//
// Timestamps are stored in RFC3339 format via the default json marshaler.
type Conversation struct {
	SessionID    string    `json:"session_id"`
	OriginalGoal string    `json:"original_goal"`
	Messages     []Message `json:"messages"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

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
		conv.AddMessage("user", conv.OriginalGoal, map[string]any{"type": "original_goal"})
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

// CurrentGoal returns the latest goal text from goal_update messages, falling
// back to the original goal when none are present.
func (c *Conversation) CurrentGoal() string {
	if c == nil {
		return ""
	}
	goal := strings.TrimSpace(c.OriginalGoal)
	for i := len(c.Messages) - 1; i >= 0; i-- {
		msg := c.Messages[i]
		if !strings.EqualFold(getType(msg.Metadata), "goal_update") {
			continue
		}
		trimmed := strings.TrimSpace(msg.Content)
		if trimmed != "" {
			return trimmed
		}
	}
	return goal
}

// Marshal serializes the conversation to JSON with indentation.
func (c *Conversation) Marshal() ([]byte, error) {
	if c == nil {
		return nil, errors.New("conversation is nil")
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
	return &conv, nil
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
		msgType := getType(msg.Metadata)
		switch msgType {
		case "goal_update", "user_feedback":
			block := renderGoalUpdate(msg.Content)
			addRawEvent(block)
		case "raw", "raw_block":
			if strings.TrimSpace(msg.Content) != "" {
				addRawEvent(msg.Content)
			}
		case "ask":
			turn := coalesceTurn(msg)
			td := addTurnEvent(turn)
			td.question = msg.Content
			if decision, ok := coerceString(msg.Metadata["decision"]); ok {
				td.decision = decision
			}
		case "answer":
			turn := coalesceTurn(msg)
			td := addTurnEvent(turn)
			td.answer = msg.Content
			td.retrieved = coerceStringSlice(msg.Metadata["retrieved_files"])
			if saved, ok := coerceString(msg.Metadata["chat_path"]); ok {
				td.savedPath = saved
			}
		default:
			continue
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
	b.WriteString("Planner decision: ")
	b.WriteString(decision)
	b.WriteString("\n")
	return sanitize(b.String())
}

func renderGoalUpdate(content string) string {
	body := strings.TrimRight(content, "\n")
	return sanitize("\n=== GOAL UPDATE\n\n" + body + "\n")
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
