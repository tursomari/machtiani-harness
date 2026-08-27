package conversation

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

// ShellActionRecord is an announced shell command available to replay
// renderers. It aliases the journal protocol without adding filesystem access
// to this package.
type ShellActionRecord = shellaction.Record

// ReplayOptions controls optional replay output.
type ReplayOptions struct {
	NoShellSteps       bool
	ShellActions       []ShellActionRecord
	SuppressConclusion bool
	StyledShellSteps   bool
	Theme              presentation.Theme
}

// RenderReplay renders a persisted conversation as deterministic plain text
// for read-only terminal replay. Its scrollback follows the ordinary run view:
// conversational messages and shell action blocks, without replay-only boxes.
func RenderReplay(conv *Conversation) (string, error) {
	return RenderReplayWithOptions(conv, ReplayOptions{})
}

// RenderReplayWithOptions renders a full replay with optional shell actions.
func RenderReplayWithOptions(conv *Conversation, opts ReplayOptions) (string, error) {
	if conv == nil {
		return "", errors.New("conversation is nil")
	}
	return renderReplay(conv, conv.Messages, nil, opts)
}

// RenderReplayDelta renders only message units appended after previous. Turn
// headings already present in previous are omitted so successive atomic
// publishes cannot repeat a partially published turn's framing.
func RenderReplayDelta(conv *Conversation, previous []Message) (string, error) {
	return RenderReplayDeltaWithOptions(conv, previous, ReplayOptions{})
}

// RenderReplayDeltaWithOptions renders appended messages and newly observed
// shell actions without repeating prior turn framing.
func RenderReplayDeltaWithOptions(conv *Conversation, previous []Message, opts ReplayOptions) (string, error) {
	if conv == nil {
		return "", errors.New("conversation is nil")
	}

	messages := IdentifyNewMessages(previous, conv.Messages)
	if len(messages) == 0 && (opts.NoShellSteps || len(opts.ShellActions) == 0) {
		return "", nil
	}

	knownTurns := make(map[int]struct{})
	for _, msg := range previous {
		switch getType(msg.Metadata) {
		case messageTypeAsk, messageTypeWorkRequest, messageTypeAnswer, messageTypeWorkResult:
			if turn := coalesceTurn(msg); turn >= 0 {
				knownTurns[turn] = struct{}{}
			}
		}
	}
	return renderReplay(conv, messages, knownTurns, opts)
}

func renderReplay(conv *Conversation, messages []Message, knownTurns map[int]struct{}, opts ReplayOptions) (string, error) {

	type renderEvent struct {
		kind string
		turn int
		text string
	}

	type turnData struct {
		question string
		answer   string
		decision string
		actions  []ShellActionRecord
	}

	events := []renderEvent{}
	turns := map[int]*turnData{}

	addTurnEvent := func(turn int) *turnData {
		if data, ok := turns[turn]; ok {
			return data
		}
		events = append(events, renderEvent{kind: "turn", turn: turn})
		data := &turnData{}
		turns[turn] = data
		return data
	}

	addTextEvent := func(label, text string) {
		events = append(events, renderEvent{kind: label, text: text})
	}

	for _, msg := range messages {
		if isHeaderGoalMessage(conv, msg) {
			continue
		}

		msgType := getType(msg.Metadata)
		switch msgType {
		case "":
			switch normalizeRole(msg.Role) {
			case "user":
				addTextEvent("message", renderReplayMessage(msg.Content))
			case "assistant":
				addTextEvent("message", renderReplayMessage(msg.Content))
			case "system":
				addTextEvent("message", renderReplayMessage(msg.Content))
			}
		case messageTypeCacheAnchor:
			continue
		case messageTypeUserInputRequest:
			addTextEvent("message", renderReplayMessage("[USER INPUT REQUEST] "+msg.Content))
		case messageTypeUserInputResponse:
			addTextEvent("message", renderReplayMessage("[USER INPUT RESPONSE] "+msg.Content))
		case messageTypeRaw:
			if strings.TrimSpace(msg.Content) != "" {
				addTextEvent("text", sanitize(msg.Content))
			}
		case messageTypeRawBlock:
			if strings.TrimSpace(msg.Content) != "" {
				addTextEvent("text", sanitize(msg.Content))
			}
		case messageTypeRecovery:
			switch normalizeRole(msg.Role) {
			case "system":
				addTextEvent("message", renderReplayMessage(msg.Content))
			case "user":
				addTextEvent("message", renderReplayMessage(msg.Content))
			case "assistant":
				addTextEvent("message", renderReplayMessage(msg.Content))
			}
		case messageTypeFinal, messageTypeFinalAnswer:
			if opts.SuppressConclusion {
				continue
			}
			addTextEvent("text", sanitize(msg.Content))
		case messageTypeAsk, messageTypeWorkRequest:
			turn := coalesceTurn(msg)
			if turn < 0 {
				continue
			}
			data := addTurnEvent(turn)
			data.question = msg.Content
			if decision, ok := coerceString(msg.Metadata["decision"]); ok {
				data.decision = decision
			}
		case messageTypeAnswer, messageTypeWorkResult:
			turn := coalesceTurn(msg)
			if turn < 0 {
				continue
			}
			data := addTurnEvent(turn)
			data.answer = msg.Content
		default:
			return "", fmt.Errorf("conversation: unhandled message type %q in RenderReplay", msgType)
		}
	}

	actionOnly := map[int][]ShellActionRecord{}
	if !opts.NoShellSteps {
		for _, action := range normalizeShellActions(opts.ShellActions) {
			if data, ok := turns[action.Turn]; ok {
				data.actions = append(data.actions, action)
				continue
			}
			if _, known := knownTurns[action.Turn]; known {
				if len(actionOnly[action.Turn]) == 0 {
					events = append(events, renderEvent{kind: "shell-actions", turn: action.Turn})
				}
				actionOnly[action.Turn] = append(actionOnly[action.Turn], action)
				continue
			}
			data := addTurnEvent(action.Turn)
			data.actions = append(data.actions, action)
		}
	}

	var out strings.Builder
	for _, event := range events {
		switch event.kind {
		case "turn":
			data := turns[event.turn]
			if data != nil {
				out.WriteString(renderReplayTurn(data.question, data.answer, data.decision, data.actions, opts))
			}
		case "shell-actions":
			out.WriteString(renderReplayShellActions(actionOnly[event.turn], opts))
		default:
			out.WriteString(replayBlock(event.text))
		}
	}

	return strings.Trim(sanitize(out.String()), "\n"), nil
}

func renderReplayTurn(question, answer, decision string, actions []ShellActionRecord, opts ReplayOptions) string {
	var out strings.Builder
	if strings.TrimSpace(question) != "" {
		out.WriteString(renderReplayMessage(question))
	}
	if len(actions) > 0 {
		out.WriteString(renderReplayShellActions(actions, opts))
	}
	if strings.TrimSpace(answer) != "" {
		out.WriteString(renderReplayMessage(answer))
	}
	if strings.TrimSpace(decision) != "" {
		out.WriteString(replayBlock("Planner decision: " + decision))
	}

	return out.String()
}

func renderReplayShellActions(actions []ShellActionRecord, opts ReplayOptions) string {
	body := renderShellActionBody(actions, opts)
	if strings.TrimSpace(stripANSI(body)) == "" {
		return ""
	}
	return strings.TrimRight(body, "\n") + "\n\n"
}

func renderShellActionBody(actions []ShellActionRecord, opts ReplayOptions) string {
	var out strings.Builder
	for _, action := range normalizeShellActions(actions) {
		if action.Step > 0 && action.StepLimit > 0 {
			out.WriteString(renderReplayStyledLine(opts, presentation.StyledLine{
				presentation.Bold(presentation.RoleTruth, fmt.Sprintf("Step %d of %d", action.Step, action.StepLimit)),
			}))
			out.WriteString("\n\n")
		} else if action.Step > 0 {
			out.WriteString(renderReplayStyledLine(opts, presentation.StyledLine{
				presentation.Bold(presentation.RoleTruth, fmt.Sprintf("Step %d", action.Step)),
			}))
			out.WriteString("\n\n")
		}
		description := stripANSI(ui.CleanActionDescription(action.Description))
		command := stripANSI(strings.TrimSpace(action.Command))
		if strings.EqualFold(strings.TrimSpace(description), command) {
			description = ""
		}
		if description != "" {
			out.WriteString(description)
			out.WriteString("\n\n")
		}
		if command != "" {
			out.WriteString(renderReplayStyledLine(opts, presentation.StyledLine{
				presentation.Bold(presentation.RoleGoodness, "$ "),
				presentation.Text(command),
			}))
			out.WriteString("\n\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func renderReplayStyledLine(opts ReplayOptions, line presentation.StyledLine) string {
	if opts.StyledShellSteps {
		return opts.Theme.RenderLine(line)
	}
	var out strings.Builder
	for _, span := range line {
		out.WriteString(span.Text)
	}
	return out.String()
}

func normalizeShellActions(actions []ShellActionRecord) []ShellActionRecord {
	ordered := append([]ShellActionRecord(nil), actions...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Turn != ordered[j].Turn {
			return ordered[i].Turn < ordered[j].Turn
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})

	seen := make(map[[2]int64]struct{}, len(ordered))
	result := ordered[:0]
	for _, action := range ordered {
		if action.Sequence > 0 {
			key := [2]int64{int64(action.Turn), action.Sequence}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		result = append(result, action)
	}
	return result
}

func renderReplayMessage(body string) string {
	body = strings.Trim(stripANSI(sanitize(body)), "\n")
	if body == "" {
		return ""
	}
	return body + "\n\n"
}

func replayBlock(body string) string {
	body = strings.Trim(stripANSI(sanitize(body)), "\n")
	if body == "" {
		return ""
	}
	return body + "\n\n"
}

func stripANSI(text string) string {
	if !strings.ContainsRune(text, '\x1b') {
		return text
	}

	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] != '\x1b' {
			out.WriteByte(text[i])
			i++
			continue
		}
		if i+1 >= len(text) {
			break
		}

		switch text[i+1] {
		case '[':
			i += 2
			for i < len(text) {
				final := text[i] >= 0x40 && text[i] <= 0x7e
				i++
				if final {
					break
				}
			}
		case ']':
			i += 2
			for i < len(text) {
				if text[i] == '\a' {
					i++
					break
				}
				if text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i += 2
		}
	}
	return out.String()
}

// LastFinalMessage returns the final conclusion content when the final answer
// message is the last message in the conversation. Read-only attach uses it to
// render exactly one run-style conclusion instead of one per snapshot.
func LastFinalMessage(conv *Conversation) (string, bool) {
	if conv == nil || len(conv.Messages) == 0 {
		return "", false
	}
	last := conv.Messages[len(conv.Messages)-1]
	switch getType(last.Metadata) {
	case messageTypeFinal, messageTypeFinalAnswer:
		return last.Content, true
	}
	return "", false
}
