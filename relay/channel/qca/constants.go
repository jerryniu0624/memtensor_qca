package qca

// ChannelName is the provider name reported to the admin console and logs.
const ChannelName = "QCA"

// DefaultBaseURL is the Qoder Cloud Agent API root used when a channel leaves
// its base URL empty.
const DefaultBaseURL = "https://api.qoder.com/api/v1/cloud"

// ModelList stays empty on purpose. QCA serves pre-provisioned Agents, so the
// models a channel exposes are chosen by the administrator and bound to Agent
// IDs through the channel model mapping.
var ModelList []string

// QCA event names as they appear either in the SSE event field or in the JSON
// payload type.
const (
	eventTypeDelta           = "event_delta"
	eventTypeAgentMessage    = "agent.message"
	eventTypeStatusIdle      = "session.status_idle"
	eventTypeTerminated      = "session.status_terminated"
	eventTypeDeleted         = "session.deleted"
	deltaTypeContent         = "content_delta"
	contentTypeText          = "text"
	stopReasonEndTurn        = "end_turn"
	stopReasonRequiresAction = "requires_action"
	defaultEventKey          = "default"
)

// transcriptPreamble introduces a rendered multi-message transcript. QCA has no
// per-request system prompt and accepts exactly one user.message per turn, so
// the whole OpenAI message list is serialized into that single message.
const transcriptPreamble = "The following is the conversation context. Follow any system instructions and reply to the final user message."

// requiresActionMessage explains the one QCA stop reason this channel cannot
// serve: the Agent paused for a client-side custom tool result. Resuming it
// needs per-session state, so tools must run server-side on the Agent instead.
const requiresActionMessage = "QCA paused the turn waiting for a client-side tool result (stop_reason=requires_action). " +
	"Give the Agent server-side tools (agent_toolset or mcp_servers) instead of relying on client-declared tools."
