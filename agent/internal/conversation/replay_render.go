package conversation

import (
	"errors"
	"fmt"
	"strings"
)

// RenderReplay renders a persisted conversation as deterministic plain text
// for read-only terminal replay. It deliberately uses framing that is distinct
// from the AsciiDoc transcript format.
func RenderReplay(conv *Conversation) (string, error) {
	if conv == nil {
		return "", errors.New("conversation is nil")
	}

	type renderEvent struct {
		kind string
		turn int
		text string
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
		data := &turnData{}
		turns[turn] = data
		return data
	}

	addTextEvent := func(label, text string) {
		events = append(events, renderEvent{kind: label, text: text})
	}

	for _, msg := range conv.Messages {
		if isHeaderGoalMessage(conv, msg) {
			continue
		}

		msgType := getType(msg.Metadata)
		switch msgType {
		case "":
			switch normalizeRole(msg.Role) {
			case "user":
				addTextEvent("USER MESSAGE", replayHelperBody(renderUserMessage(msg.Content), "=== USER MESSAGE"))
			case "assistant":
				addTextEvent("ASSISTANT MESSAGE", replayHelperBody(renderAssistantMessage(msg.Content), "=== ASSISTANT MESSAGE"))
			case "system":
				addTextEvent("SYSTEM MESSAGE", replayHelperBody(renderSystemMessage(msg.Content), "=== SYSTEM MESSAGE"))
			}
		case messageTypeCacheAnchor:
			continue
		case messageTypeUserInputRequest:
			addTextEvent("USER INPUT REQUEST", replayHelperBody(renderUserInputRequest(msg.Content), "=== USER INPUT REQUEST"))
		case messageTypeUserInputResponse:
			addTextEvent("USER INPUT RESPONSE", replayHelperBody(renderUserInputResponse(msg.Content), "=== USER INPUT RESPONSE"))
		case messageTypeRaw:
			if strings.TrimSpace(msg.Content) != "" {
				addTextEvent("RAW", sanitize(msg.Content))
			}
		case messageTypeRawBlock:
			if strings.TrimSpace(msg.Content) != "" {
				addTextEvent("RAW BLOCK", sanitize(msg.Content))
			}
		case messageTypeRecovery:
			switch normalizeRole(msg.Role) {
			case "system":
				addTextEvent("SYSTEM MESSAGE", replayHelperBody(renderSystemMessage(msg.Content), "=== SYSTEM MESSAGE"))
			case "user":
				addTextEvent("USER MESSAGE", replayHelperBody(renderUserMessage(msg.Content), "=== USER MESSAGE"))
			case "assistant":
				addTextEvent("ASSISTANT MESSAGE", replayHelperBody(renderAssistantMessage(msg.Content), "=== ASSISTANT MESSAGE"))
			}
		case messageTypeFinal, messageTypeFinalAnswer:
			turnCount, _ := coerceInt(msg.Metadata["turns"])
			capped, _ := coerceBool(msg.Metadata["capped"])
			addTextEvent("conclusion", renderConclusion(msg.Content, turnCount, capped))
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
			data.retrieved = coerceStringSlice(msg.Metadata["retrieved_files"])
			if savedPath, ok := coerceString(msg.Metadata["chat_path"]); ok {
				data.savedPath = savedPath
			}
		default:
			return "", fmt.Errorf("conversation: unhandled message type %q in RenderReplay", msgType)
		}
	}

	var out strings.Builder
	headerBody := replayHelperBody(renderHeader(conv.OriginalGoal), "= MCT-AGENT TRANSCRIPT", "== GOAL:")
	out.WriteString(replaySection("GOAL", headerBody, false))
	for _, event := range events {
		switch event.kind {
		case "turn":
			data := turns[event.turn]
			if data != nil {
				out.WriteString(renderReplayTurn(event.turn, data.question, data.savedPath, data.retrieved, data.answer, data.decision))
			}
		case "conclusion":
			out.WriteString(renderReplayConclusion(event.text))
		default:
			out.WriteString(replaySection(event.kind, event.text, false))
		}
	}

	return strings.Trim(stripANSI(sanitize(out.String())), "\n"), nil
}

func renderReplayTurn(turn int, question, savedPath string, retrieved []string, answer, decision string) string {
	var out strings.Builder
	out.WriteString(replaySection(fmt.Sprintf("TURN %d", turn), "", true))

	questionBody := replayHelperBody(renderTurn(-1, question, "", nil, "", ""))
	if strings.TrimSpace(questionBody) != "" {
		out.WriteString(replaySection("QUESTION", questionBody, false))
	}

	artifactsBody := replayHelperBody(renderTurn(-1, "", savedPath, retrieved, "", ""))
	if strings.TrimSpace(artifactsBody) != "" {
		out.WriteString(replaySection("ARTIFACTS", artifactsBody, false))
	}

	answerBody := replayHelperBody(renderTurn(-1, "", "", nil, answer, ""), "=== ANSWER")
	if strings.TrimSpace(answerBody) != "" {
		out.WriteString(replaySection("ANSWER", answerBody, false))
	}

	decisionBody := replayHelperBody(renderTurn(-1, "", "", nil, "", decision))
	if strings.TrimSpace(decisionBody) != "" {
		out.WriteString(replaySection("DECISION", decisionBody, false))
	}

	return out.String()
}

func renderReplayConclusion(block string) string {
	block = strings.Trim(sanitize(block), "\n")
	line, body, found := strings.Cut(block, "\n")
	if !found {
		return replaySection("CONCLUSION", block, true)
	}
	label := strings.TrimSpace(strings.TrimPrefix(line, "=="))
	if label == "" {
		label = "CONCLUSION"
	}
	return replaySection(label, strings.Trim(body, "\n"), true)
}

func replayHelperBody(block string, headings ...string) string {
	body := strings.Trim(sanitize(block), "\n")
	for _, heading := range headings {
		if strings.HasPrefix(body, heading) {
			body = strings.TrimPrefix(body, heading)
			body = strings.Trim(body, "\n")
		}
	}
	return body
}

func replaySection(label, body string, major bool) string {
	rule := "──"
	if major {
		rule = "────"
	}
	var out strings.Builder
	out.WriteString(rule)
	out.WriteByte(' ')
	out.WriteString(label)
	out.WriteByte(' ')
	out.WriteString(rule)
	out.WriteString("\n\n")
	if body = strings.Trim(body, "\n"); body != "" {
		out.WriteString(body)
		out.WriteString("\n\n")
	}
	return out.String()
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
