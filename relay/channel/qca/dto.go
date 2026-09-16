package qca

// ChatRequest is the converted per-turn request. new-api marshals the value
// returned by ConvertOpenAIRequest and hands it back to DoRequest, so this is
// an internal hand-off struct rather than a QCA wire type.
type ChatRequest struct {
	AgentID       string `json:"agent_id"`
	EnvironmentID string `json:"environment_id"`
	// Model is the client-facing model name echoed back in responses; the QCA
	// Agent ID is an internal detail and must not leak to callers.
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	// SessionTitle labels the QCA session in the QCA console.
	SessionTitle string `json:"session_title,omitempty"`
}

// sessionRequest is the POST /sessions body.
type sessionRequest struct {
	Agent         string `json:"agent"`
	EnvironmentID string `json:"environment_id"`
	Title         string `json:"title,omitempty"`
}

// sessionEventRequest is the POST /sessions/{id}/events body.
type sessionEventRequest struct {
	Events []sessionEvent `json:"events"`
}

type sessionEvent struct {
	Type    string         `json:"type"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// resourceEnvelope decodes QCA's {"data": {...}} wrapper. Some endpoints return
// the resource at the top level, so both shapes are accepted.
type resourceEnvelope struct {
	Data *resourceID `json:"data"`
	ID   string      `json:"id"`
}

type resourceID struct {
	ID string `json:"id"`
}

// streamEvent is one decoded QCA event payload.
type streamEvent struct {
	Type       string         `json:"type"`
	ID         string         `json:"id,omitempty"`
	EventID    string         `json:"event_id,omitempty"`
	Delta      *eventDelta    `json:"delta,omitempty"`
	Content    []contentBlock `json:"content,omitempty"`
	StopReason *stopReason    `json:"stop_reason,omitempty"`
}

type eventDelta struct {
	Type    string        `json:"type"`
	Content *contentBlock `json:"content,omitempty"`
}

type stopReason struct {
	Type     string   `json:"type"`
	EventIDs []string `json:"event_ids,omitempty"`
}

// sseFrame is one server-sent event with its decoded payload.
type sseFrame struct {
	// Name is the SSE event field, "message" when the frame did not carry one.
	Name string
	// ID is the SSE id field.
	ID   string
	Data streamEvent
}

// eventType resolves the QCA event type, which upstream sends either in the
// JSON payload or only in the SSE event field.
func (f sseFrame) eventType() string {
	if f.Data.Type != "" {
		return f.Data.Type
	}
	return f.Name
}
