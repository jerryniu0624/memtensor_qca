package qca

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderTranscript(t *testing.T) {
	toolCalls := dto.Message{Role: "assistant"}
	toolCalls.SetToolCalls([]dto.ToolCallRequest{{
		ID:       "call_1",
		Type:     "function",
		Function: dto.FunctionRequest{Name: "get_weather", Arguments: `{"city":"Shanghai"}`},
	}})

	tests := []struct {
		name     string
		messages []dto.Message
		want     string
		wantErr  string
	}{
		{
			name:     "single user message is sent verbatim",
			messages: []dto.Message{{Role: "user", Content: "hello"}},
			want:     "hello",
		},
		{
			name: "multi message history renders role blocks",
			messages: []dto.Message{
				{Role: "system", Content: "be terse"},
				{Role: "user", Content: "2+2?"},
				{Role: "assistant", Content: "4"},
				{Role: "user", Content: "and 3?"},
			},
			want: transcriptPreamble + "\n\n" +
				"<|system|>\nbe terse\n\n" +
				"<|user|>\n2+2?\n\n" +
				"<|assistant|>\n4\n\n" +
				"<|user|>\nand 3?",
		},
		{
			name: "developer role is bridged as system",
			messages: []dto.Message{
				{Role: "developer", Content: "rules"},
				{Role: "user", Content: "hi"},
			},
			want: transcriptPreamble + "\n\n<|system|>\nrules\n\n<|user|>\nhi",
		},
		{
			name: "assistant tool calls are rendered into the transcript",
			messages: []dto.Message{
				{Role: "user", Content: "weather?"},
				toolCalls,
				{Role: "tool", Content: "sunny", ToolCallId: "call_1"},
			},
			want: transcriptPreamble + "\n\n<|user|>\nweather?\n\n" +
				"<|assistant|>\n<tool_calls>\nget_weather({\"city\":\"Shanghai\"})\n</tool_calls>\n\n" +
				"<|tool|>\nsunny",
		},
		{
			name: "content parts are joined as text",
			messages: []dto.Message{{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "first"},
				map[string]any{"type": "text", "text": "second"},
			}}},
			want: "first\nsecond",
		},
		{
			name:     "empty messages are rejected",
			messages: nil,
			wantErr:  "non-empty",
		},
		{
			name:     "history must end with a user or tool message",
			messages: []dto.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}},
			wantErr:  "last message must have role",
		},
		{
			name:     "unknown roles are rejected",
			messages: []dto.Message{{Role: "function", Content: "x"}, {Role: "user", Content: "hi"}},
			wantErr:  "not supported",
		},
		{
			name: "non-text content is rejected instead of dropped",
			messages: []dto.Message{{Role: "user", Content: []any{
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
			}}},
			wantErr: "relays text only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderTranscript(tt.messages)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConsumeTurn(t *testing.T) {
	tests := []struct {
		name        string
		sse         string
		wantText    string
		wantDeltas  []string
		wantErrCont string
	}{
		{
			name: "final agent.message repeating the deltas is deduplicated",
			sse: "event: event_delta\n" +
				`data: {"type":"event_delta","event_id":"ev_1","delta":{"type":"content_delta","content":{"type":"text","text":"在的"}}}` + "\n\n" +
				"event: agent.message\n" +
				`data: {"type":"agent.message","id":"ev_1","content":[{"type":"text","text":"在的"}]}` + "\n\n" +
				"event: session.status_idle\n" +
				`data: {"type":"session.status_idle","stop_reason":{"type":"end_turn"}}` + "\n\n",
			wantText:   "在的",
			wantDeltas: []string{"在的"},
		},
		{
			name: "final agent.message longer than the deltas emits only the tail",
			sse: `data: {"type":"event_delta","event_id":"ev_1","delta":{"type":"content_delta","content":{"type":"text","text":"你好"}}}` + "\n\n" +
				`data: {"type":"agent.message","id":"ev_1","content":[{"type":"text","text":"你好世界"}]}` + "\n\n" +
				`data: {"type":"session.status_idle","stop_reason":{"type":"end_turn"}}` + "\n\n",
			wantText:   "你好世界",
			wantDeltas: []string{"你好", "世界"},
		},
		{
			name:        "a turn paused for a client-side tool result fails",
			sse:         `data: {"type":"session.status_idle","stop_reason":{"type":"requires_action","event_ids":["ev_9"]}}` + "\n\n",
			wantErrCont: "client-side tool result",
		},
		{
			name:        "an unexpected stop reason fails",
			sse:         `data: {"type":"session.status_idle","stop_reason":{"type":"max_tokens"}}` + "\n\n",
			wantErrCont: "stop_reason=max_tokens",
		},
		{
			name:        "a terminated session fails",
			sse:         `data: {"type":"session.status_terminated"}` + "\n\n",
			wantErrCont: "terminal state",
		},
		{
			name:        "a stream that ends before status_idle fails",
			sse:         `data: {"type":"event_delta","event_id":"ev_1","delta":{"type":"content_delta","content":{"type":"text","text":"partial"}}}` + "\n\n",
			wantErrCont: "ended before the turn completed",
		},
		{
			name:        "an unparseable event fails",
			sse:         "data: not-json\n\n",
			wantErrCont: "invalid SSE event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := &http.Response{Body: io.NopCloser(strings.NewReader(tt.sse))}
			var deltas []string
			text, err := consumeTurn(response, func(delta string) error {
				deltas = append(deltas, delta)
				return nil
			})
			if tt.wantErrCont != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrCont)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantText, text)
			assert.Equal(t, tt.wantDeltas, deltas)
		})
	}
}

func TestEstimateUsage(t *testing.T) {
	usage := estimateUsage("abcdefgh", "在的在的")
	assert.Equal(t, 2, usage.PromptTokens)
	assert.Equal(t, 1, usage.CompletionTokens)
	assert.Equal(t, 3, usage.TotalTokens)
	assert.Equal(t, 0, estimateUsage("", "").TotalTokens)
	assert.Equal(t, 1, estimateUsage("", "ab").CompletionTokens, "a non-empty turn bills at least one token")
}

// fakeQCA records the call order QCA requires and serves one scripted turn.
type fakeQCA struct {
	server      *httptest.Server
	mu          sync.Mutex
	calls       []string
	sessionBody string
	eventBody   string
	events      string
}

func newFakeQCA(t *testing.T, events string) *fakeQCA {
	fake := &fakeQCA{events: events}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sessions":
			fake.calls = append(fake.calls, "create_session")
			fake.sessionBody = string(body)
			assert.Equal(t, "Bearer pt-test", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"data":{"id":"sess_1"}}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sessions/sess_1/events/stream"):
			fake.calls = append(fake.calls, "open_stream")
			assert.Equal(t, "agent.message", r.URL.Query().Get("event_deltas[]"))
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(events))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sessions/sess_1/events"):
			fake.calls = append(fake.calls, "post_event")
			fake.eventBody = string(body)
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeQCA) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func testRelayInfo(baseURL string) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType:          64,
		ChannelBaseUrl:       baseURL,
		ApiKey:               "pt-test",
		UpstreamModelName:    "agent_1",
		ChannelOtherSettings: dto.ChannelOtherSettings{QCAEnvironmentID: "env_1"},
	}}
	return info
}

func testGinContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c
}

const scriptedTurn = "event: event_delta\n" +
	`data: {"type":"event_delta","event_id":"ev_1","delta":{"type":"content_delta","content":{"type":"text","text":"在的"}}}` + "\n\n" +
	"event: agent.message\n" +
	`data: {"type":"agent.message","id":"ev_1","content":[{"type":"text","text":"在的"}]}` + "\n\n" +
	"event: session.status_idle\n" +
	`data: {"type":"session.status_idle","stop_reason":{"type":"end_turn"}}` + "\n\n"

func TestStartTurnBuffered(t *testing.T) {
	fake := newFakeQCA(t, scriptedTurn)
	c := testGinContext(t)
	info := testRelayInfo(fake.server.URL)

	resp, err := startTurn(c, info, &ChatRequest{
		AgentID: "agent_1", EnvironmentID: "env_1", Model: "deepseek-v4-flash-0731",
		Prompt: "只回复两个字：在的",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"create_session", "open_stream", "post_event"}, fake.recordedCalls(),
		"QCA only accepts the user event after the SSE subscription is open")
	assert.Contains(t, fake.sessionBody, `"agent":"agent_1"`)
	assert.Contains(t, fake.sessionBody, `"environment_id":"env_1"`)
	assert.Contains(t, fake.eventBody, `"type":"user.message"`)
	assert.Contains(t, fake.eventBody, "只回复两个字：在的")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var completion dto.OpenAITextResponse
	require.NoError(t, common.Unmarshal(body, &completion))
	assert.Equal(t, "chat.completion", completion.Object)
	assert.Equal(t, "deepseek-v4-flash-0731", completion.Model, "the client-facing model must not leak the Agent ID")
	require.Len(t, completion.Choices, 1)
	assert.Equal(t, "在的", completion.Choices[0].Message.StringContent())
	assert.Equal(t, "stop", completion.Choices[0].FinishReason)
	assert.Positive(t, completion.Usage.TotalTokens, "QCA reports no tokens, so usage must be estimated")
}

func TestStartTurnStreamed(t *testing.T) {
	fake := newFakeQCA(t, scriptedTurn)
	c := testGinContext(t)
	info := testRelayInfo(fake.server.URL)

	resp, err := startTurn(c, info, &ChatRequest{
		AgentID: "agent_1", EnvironmentID: "env_1", Model: "deepseek-v4-flash-0731",
		Prompt: "只回复两个字：在的", Stream: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, []string{"create_session", "open_stream", "post_event"}, fake.recordedCalls())

	var frames []string
	for _, line := range strings.Split(string(body), "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			frames = append(frames, payload)
		}
	}
	require.NotEmpty(t, frames)
	assert.Equal(t, "[DONE]", frames[len(frames)-1])

	var kinds []string
	var text strings.Builder
	for _, frame := range frames[:len(frames)-1] {
		var chunk dto.ChatCompletionsStreamResponse
		require.NoError(t, common.UnmarshalJsonStr(frame, &chunk))
		assert.Equal(t, "chat.completion.chunk", chunk.Object)
		switch {
		case chunk.Usage != nil:
			kinds = append(kinds, "usage")
			assert.Positive(t, chunk.Usage.TotalTokens)
			assert.Empty(t, chunk.Choices, "the usage frame carries an empty choices array")
		case len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil:
			kinds = append(kinds, "finish_reason="+*chunk.Choices[0].FinishReason)
		case len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Role != "":
			kinds = append(kinds, "role")
		default:
			kinds = append(kinds, "content")
			text.WriteString(chunk.Choices[0].Delta.GetContentString())
		}
	}
	assert.Equal(t, []string{"role", "content", "finish_reason=stop", "usage"}, kinds)
	assert.Equal(t, "在的", text.String())
}

func TestStartTurnSurfacesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid access token"}}`))
	}))
	t.Cleanup(server.Close)

	_, err := startTurn(testGinContext(t), testRelayInfo(server.URL), &ChatRequest{
		AgentID: "agent_1", EnvironmentID: "env_1", Prompt: "hi",
	})
	require.Error(t, err)
	var upstream *upstreamError
	require.ErrorAs(t, err, &upstream, "an expired PAT must keep its 401 so channel auto-disable still works")
	assert.Equal(t, http.StatusUnauthorized, upstream.response.StatusCode)
}
