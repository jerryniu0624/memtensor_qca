package qca

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// upstreamError carries a non-2xx QCA response back to DoRequest, which returns
// it as the relay response so the upstream status code and error body reach the
// client unchanged. Keeping the real status matters for gateway behavior: a 401
// from an expired PAT still drives channel auto-disable, and a 429 still counts
// as an upstream rate limit.
type upstreamError struct {
	response *http.Response
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("QCA upstream returned status %d", e.response.StatusCode)
}

// startTurn runs one QCA turn and presents it to the relay as an OpenAI-shaped
// HTTP response: buffered chat.completion JSON, or a text/event-stream of
// chat.completion.chunk frames.
//
// The call order is load-bearing. QCA only accepts the user event once the SSE
// subscription is open, so the session is created, the stream is opened, and
// only then is the message posted.
//
// Every turn runs in a fresh QCA session. Conversation context therefore comes
// from the rendered transcript rather than from server-side session state, which
// keeps the channel stateless across gateway replicas at the cost of one QCA
// session per request.
func startTurn(c *gin.Context, info *relaycommon.RelayInfo, chatRequest *ChatRequest) (*http.Response, error) {
	client, err := service.GetHttpClientWithProxySettings(info.ChannelSetting.Proxy, info.ChannelSetting)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed)
	}
	upstream := baseURL(info)
	ctx := c.Request.Context()

	sessionID, err := createSession(ctx, client, upstream, info.ApiKey, chatRequest)
	if err != nil {
		return nil, err
	}
	streamResponse, err := openEventStream(ctx, client, upstream, info.ApiKey, sessionID)
	if err != nil {
		return nil, err
	}
	if err := sendUserMessage(ctx, client, upstream, info.ApiKey, sessionID, chatRequest.Prompt); err != nil {
		_ = streamResponse.Body.Close()
		return nil, err
	}

	if !chatRequest.Stream {
		return bufferedTurnResponse(c, chatRequest, streamResponse)
	}
	return streamingTurnResponse(c, chatRequest, streamResponse), nil
}

func createSession(ctx context.Context, client *http.Client, baseURL, accessToken string, chatRequest *ChatRequest) (string, error) {
	body, err := common.Marshal(sessionRequest{
		Agent:         chatRequest.AgentID,
		EnvironmentID: chatRequest.EnvironmentID,
		Title:         chatRequest.SessionTitle,
	})
	if err != nil {
		return "", err
	}
	payload, failure, err := postJSON(ctx, client, baseURL+"/sessions", accessToken, body)
	if failure != nil {
		return "", &upstreamError{response: failure}
	}
	if err != nil {
		return "", err
	}
	var envelope resourceEnvelope
	if err := common.Unmarshal(payload, &envelope); err != nil {
		return "", fmt.Errorf("QCA returned an invalid JSON response: %w", err)
	}
	sessionID := envelope.ID
	if envelope.Data != nil {
		sessionID = envelope.Data.ID
	}
	if sessionID == "" {
		return "", errors.New("QCA session response did not include an id")
	}
	return sessionID, nil
}

func openEventStream(ctx context.Context, client *http.Client, baseURL, accessToken, sessionID string) (*http.Response, error) {
	query := neturl.Values{}
	query.Add("event_deltas[]", eventTypeAgentMessage)
	streamURL := fmt.Sprintf("%s/sessions/%s/events/stream?%s", baseURL, neturl.PathEscape(sessionID), query.Encode())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &upstreamError{response: response}
	}
	return response, nil
}

func sendUserMessage(ctx context.Context, client *http.Client, baseURL, accessToken, sessionID, prompt string) error {
	body, err := common.Marshal(sessionEventRequest{Events: []sessionEvent{{
		Type:    "user.message",
		Content: []contentBlock{{Type: contentTypeText, Text: prompt}},
	}}})
	if err != nil {
		return err
	}
	_, failure, err := postJSON(ctx, client, fmt.Sprintf("%s/sessions/%s/events", baseURL, neturl.PathEscape(sessionID)), accessToken, body)
	if failure != nil {
		return &upstreamError{response: failure}
	}
	return err
}

// postJSON sends a QCA JSON request. A non-2xx response is returned as failure
// with its body unread so the relay can surface the upstream status and error.
func postJSON(ctx context.Context, client *http.Client, url, accessToken string, body []byte) ([]byte, *http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, response, nil
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	return payload, nil, nil
}

func bufferedTurnResponse(c *gin.Context, chatRequest *ChatRequest, streamResponse *http.Response) (*http.Response, error) {
	defer streamResponse.Body.Close()
	text, err := consumeTurn(streamResponse, func(string) error { return nil })
	if err != nil {
		return nil, err
	}
	body, err := common.Marshal(chatCompletion(c, chatRequest, text))
	if err != nil {
		return nil, err
	}
	return jsonResponse(body), nil
}

// chatCompletion builds the OpenAI chat.completion the relay returns to the
// client, or converts to the client's own protocol.
func chatCompletion(c *gin.Context, chatRequest *ChatRequest, text string) *dto.OpenAITextResponse {
	return &dto.OpenAITextResponse{
		Id:      helper.GetResponseID(c),
		Model:   chatRequest.Model,
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
		Choices: []dto.OpenAITextResponseChoice{{
			Index:        0,
			Message:      dto.Message{Role: "assistant", Content: text},
			FinishReason: "stop",
		}},
		Usage: *estimateUsage(chatRequest.Prompt, text),
	}
}

func streamingTurnResponse(c *gin.Context, chatRequest *ChatRequest, streamResponse *http.Response) *http.Response {
	responseID := helper.GetResponseID(c)
	created := common.GetTimestamp()
	model := chatRequest.Model
	prompt := chatRequest.Prompt

	reader, writer := io.Pipe()
	gopool.Go(func() {
		defer streamResponse.Body.Close()

		var text strings.Builder
		roleChunk := baseChunk(responseID, created, model)
		roleChunk.Choices = []dto.ChatCompletionsStreamResponseChoice{{
			Delta: dto.ChatCompletionsStreamResponseChoiceDelta{Role: "assistant"},
		}}
		err := writeChunk(writer, roleChunk)
		if err == nil {
			_, err = consumeTurn(streamResponse, func(delta string) error {
				text.WriteString(delta)
				textChunk := baseChunk(responseID, created, model)
				textChunk.Choices = []dto.ChatCompletionsStreamResponseChoice{{}}
				textChunk.Choices[0].Delta.SetContentString(delta)
				return writeChunk(writer, textChunk)
			})
		}
		if err == nil {
			finishReason := "stop"
			finishChunk := baseChunk(responseID, created, model)
			finishChunk.Choices = []dto.ChatCompletionsStreamResponseChoice{{FinishReason: &finishReason}}
			err = writeChunk(writer, finishChunk)
		}
		if err == nil {
			// Usage follows the OpenAI convention: an empty-choices frame after
			// finish_reason and before [DONE], so gateways can bill the turn.
			usageChunk := baseChunk(responseID, created, model)
			usageChunk.Choices = []dto.ChatCompletionsStreamResponseChoice{}
			usageChunk.Usage = estimateUsage(prompt, text.String())
			err = writeChunk(writer, usageChunk)
		}
		if err != nil {
			// A turn that failed upstream ends the client stream without [DONE]
			// instead of pretending it completed.
			_ = writer.CloseWithError(err)
			return
		}
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		_ = writer.Close()
	})

	header := http.Header{}
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: reader}
}

func baseChunk(id string, created int64, model string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
	}
}

func writeChunk(writer *io.PipeWriter, chunk *dto.ChatCompletionsStreamResponse) error {
	body, err := common.Marshal(chunk)
	if err != nil {
		return err
	}
	var frame bytes.Buffer
	frame.WriteString("data: ")
	frame.Write(body)
	frame.WriteString("\n\n")
	_, err = writer.Write(frame.Bytes())
	return err
}

func jsonResponse(body []byte) *http.Response {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// charsPerToken is the estimation ratio for billing. QCA reports no token
// counts: the only usage signal in its event stream is
// span.model_request_end.model_usage.credits. Without an estimate every QCA
// request would be logged and billed as zero tokens.
const charsPerToken = 4

func estimateUsage(prompt, completion string) *dto.Usage {
	promptTokens := estimateTokens(prompt)
	completionTokens := estimateTokens(completion)
	return &dto.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
}

func estimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return max(1, (runes+charsPerToken-1)/charsPerToken)
}

// consumeTurn reads the QCA event stream until the turn completes and returns
// the assistant text. onDelta is called for every newly emitted piece of text.
//
// Only session.status_idle with stop_reason.type end_turn completes a turn;
// every other terminal state, and a stream that ends early, is an upstream
// failure.
func consumeTurn(streamResponse *http.Response, onDelta func(text string) error) (string, error) {
	reader := &turnReader{accumulator: newTurnAccumulator(), eventName: sseDefaultEventName}
	scanner := helper.NewStreamScanner(streamResponse.Body)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !reader.hasData() {
				reader.resetFrame()
				continue
			}
			completed, err := reader.handleFrame(onDelta)
			if err != nil {
				return "", err
			}
			if completed {
				return reader.text.String(), nil
			}
			continue
		}
		reader.parseLine(line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read QCA event stream: %w", err)
	}
	// A stream may end on the final frame without a trailing blank line.
	if reader.hasData() {
		completed, err := reader.handleFrame(onDelta)
		if err != nil {
			return "", err
		}
		if completed {
			return reader.text.String(), nil
		}
	}
	return "", errors.New("QCA event stream ended before the turn completed")
}

const sseDefaultEventName = "message"

// turnReader parses QCA server-sent events and accumulates the assistant text.
type turnReader struct {
	accumulator *turnAccumulator
	text        strings.Builder
	eventName   string
	eventID     string
	dataLines   []string
}

func (r *turnReader) hasData() bool {
	return len(r.dataLines) > 0
}

func (r *turnReader) resetFrame() {
	r.eventName = sseDefaultEventName
	r.eventID = ""
	r.dataLines = nil
}

func (r *turnReader) parseLine(line string) {
	if strings.HasPrefix(line, ":") {
		return
	}
	field, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		r.eventName = value
	case "id":
		r.eventID = value
	case "data":
		r.dataLines = append(r.dataLines, value)
	}
}

// handleFrame decodes the buffered frame and reports whether the turn completed.
func (r *turnReader) handleFrame(onDelta func(text string) error) (bool, error) {
	frame, err := decodeSSEFrame(r.eventName, r.eventID, strings.Join(r.dataLines, "\n"))
	r.resetFrame()
	if err != nil {
		return false, err
	}

	switch frame.eventType() {
	case eventTypeDelta:
		if delta, ok := r.accumulator.contentDelta(frame); ok {
			return false, r.emit(delta, onDelta)
		}
		return false, nil
	case eventTypeAgentMessage:
		suffix, ok := r.accumulator.messageSuffix(frame)
		if !ok {
			return false, nil
		}
		return false, r.emit(suffix, onDelta)
	case eventTypeStatusIdle:
		return r.idleStopReason(frame)
	case eventTypeTerminated, eventTypeDeleted:
		return false, fmt.Errorf("QCA session reached terminal state %q before the turn completed", frame.eventType())
	}
	return false, nil
}

// idleStopReason turns a session.status_idle frame into either a completed turn
// or an upstream failure.
func (r *turnReader) idleStopReason(frame sseFrame) (bool, error) {
	stopType := ""
	if frame.Data.StopReason != nil {
		stopType = frame.Data.StopReason.Type
	}
	switch stopType {
	case stopReasonEndTurn:
		return true, nil
	case stopReasonRequiresAction:
		return false, errors.New(requiresActionMessage)
	case "":
		return false, errors.New("QCA ended the session without completing the turn")
	default:
		return false, fmt.Errorf("QCA ended the turn without completing it (stop_reason=%s)", stopType)
	}
}

func (r *turnReader) emit(text string, onDelta func(string) error) error {
	if text == "" {
		return nil
	}
	r.text.WriteString(text)
	return onDelta(text)
}

func decodeSSEFrame(name, id, rawData string) (sseFrame, error) {
	var data streamEvent
	if err := common.Unmarshal([]byte(rawData), &data); err != nil {
		return sseFrame{}, fmt.Errorf("QCA returned an invalid SSE event: %w", err)
	}
	return sseFrame{Name: name, ID: id, Data: data}, nil
}

// turnAccumulator tracks the text already emitted per QCA event id, so a final
// agent.message that repeats the streamed deltas is not sent to the client twice.
type turnAccumulator struct {
	sent map[string]string
}

func newTurnAccumulator() *turnAccumulator {
	return &turnAccumulator{sent: make(map[string]string)}
}

func (a *turnAccumulator) contentDelta(frame sseFrame) (string, bool) {
	delta := frame.Data.Delta
	if delta == nil || delta.Type != deltaTypeContent || delta.Content == nil || delta.Content.Type != contentTypeText {
		return "", false
	}
	key := frame.Data.EventID
	if key == "" {
		key = frame.ID
	}
	if key == "" {
		key = defaultEventKey
	}
	a.sent[key] += delta.Content.Text
	return delta.Content.Text, true
}

// messageSuffix returns the part of a final agent.message that has not been
// streamed yet.
func (a *turnAccumulator) messageSuffix(frame sseFrame) (string, bool) {
	var message strings.Builder
	for _, block := range frame.Data.Content {
		if block.Type == contentTypeText {
			message.WriteString(block.Text)
		}
	}
	key := frame.Data.ID
	if key == "" {
		key = frame.ID
	}
	if key == "" {
		key = defaultEventKey
	}
	full := message.String()
	already := a.sent[key]
	switch {
	case strings.HasPrefix(full, already):
		a.sent[key] = full
		return full[len(already):], true
	case strings.HasPrefix(already, full):
		return "", true
	default:
		a.sent[key] = already + full
		return full, true
	}
}
