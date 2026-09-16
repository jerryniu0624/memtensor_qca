package qca

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// RenderTranscript serializes OpenAI messages into the single user.message text
// a QCA turn accepts.
//
// QCA has no per-request system prompt, so system messages become <|system|>
// blocks and the Agent's own server-side system prompt must be written to treat
// those blocks as authoritative instructions. Assistant tool calls and tool
// results are rendered into the transcript as well, which lets a client replay a
// conversation that contained function calls even though every turn runs in a
// fresh QCA session.
func RenderTranscript(messages []dto.Message) (string, error) {
	if len(messages) == 0 {
		return "", errors.New("'messages' must be a non-empty array")
	}

	roles := make([]string, 0, len(messages))
	blocks := make([]string, 0, len(messages))
	for i := range messages {
		message := &messages[i]
		role := message.Role
		switch role {
		case "system", "developer":
			role = "system"
		case "user", "assistant", "tool":
		default:
			return "", fmt.Errorf("messages[%d].role %q is not supported by the QCA channel", i, message.Role)
		}
		text, err := textContent(message, i)
		if err != nil {
			return "", err
		}
		if role == "assistant" {
			if rendered := renderToolCalls(message.ParseToolCalls()); rendered != "" {
				if text != "" {
					text += "\n"
				}
				text += rendered
			}
		}
		roles = append(roles, role)
		blocks = append(blocks, text)
	}

	if last := roles[len(roles)-1]; last != "user" && last != "tool" {
		return "", errors.New("the last message must have role 'user', or role 'tool' when returning tool results")
	}
	if len(blocks) == 1 {
		return blocks[0], nil
	}

	var transcript strings.Builder
	transcript.WriteString(transcriptPreamble)
	for i := range blocks {
		transcript.WriteString("\n\n<|")
		transcript.WriteString(roles[i])
		transcript.WriteString("|>\n")
		transcript.WriteString(blocks[i])
	}
	return transcript.String(), nil
}

// textContent flattens message content into plain text. QCA turns carry text
// only, so any other content part is rejected instead of being silently dropped.
func textContent(message *dto.Message, index int) (string, error) {
	if message.Content == nil {
		return "", nil
	}
	if text, ok := message.Content.(string); ok {
		return text, nil
	}
	var texts []string
	for _, part := range message.ParseContent() {
		if part.Type != dto.ContentTypeText {
			return "", fmt.Errorf("messages[%d].content contains a %q part; the QCA channel relays text only", index, part.Type)
		}
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n"), nil
}

// renderToolCalls renders an assistant message's tool calls for the transcript.
func renderToolCalls(toolCalls []dto.ToolCallRequest) string {
	if len(toolCalls) == 0 {
		return ""
	}
	var lines []string
	for _, toolCall := range toolCalls {
		name := toolCall.Function.Name
		if name == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s(%s)", name, toolCall.Function.Arguments))
	}
	if len(lines) == 0 {
		return ""
	}
	return "<tool_calls>\n" + strings.Join(lines, "\n") + "\n</tool_calls>"
}
