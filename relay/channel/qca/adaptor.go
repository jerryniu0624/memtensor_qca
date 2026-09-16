package qca

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type Adaptor struct{}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	// Responses are presented to the relay in OpenAI shape, so the OpenAI
	// handlers write them; they rely on the state initialized here.
	(&openai.Adaptor{}).Init(info)
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return baseURL(info) + "/sessions", nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("Authorization", "Bearer "+info.ApiKey)
	req.Set("Content-Type", "application/json")
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	prompt, err := RenderTranscript(request.Messages)
	if err != nil {
		return nil, err
	}
	// The channel model mapping binds a client-facing model name to the QCA
	// Agent that serves it, so the mapped upstream name is the Agent ID.
	agentID := strings.TrimSpace(info.UpstreamModelName)
	if agentID == "" {
		return nil, fmt.Errorf("model %q is not bound to a QCA Agent: add it to the channel model mapping", info.OriginModelName)
	}
	environmentID := strings.TrimSpace(info.ChannelOtherSettings.QCAEnvironmentID)
	if environmentID == "" {
		return nil, errors.New("the QCA channel is missing qca_environment_id in its settings")
	}
	return &ChatRequest{
		AgentID:       agentID,
		EnvironmentID: environmentID,
		Model:         info.OriginModelName,
		Prompt:        prompt,
		Stream:        info.IsStream,
		SessionTitle:  "new-api " + c.GetString(common.RequestIdKey),
	}, nil
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return a.convertViaOpenAI(c, info, request)
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return a.convertViaOpenAI(c, info, request)
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	return a.convertViaOpenAI(c, info, request)
}

// convertViaOpenAI routes a non-OpenAI client request through the shared
// protocol converters: a QCA turn is always built from one flat transcript, so
// every client protocol first becomes an OpenAI chat request.
func (a *Adaptor) convertViaOpenAI(c *gin.Context, info *relaycommon.RelayInfo, request any) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	openaiRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	return a.ConvertOpenAIRequest(c, info, openaiRequest)
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, errors.New("the QCA channel does not support rerank")
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, errors.New("the QCA channel does not support embeddings")
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, errors.New("the QCA channel does not support audio")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, errors.New("the QCA channel does not support image generation")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	body, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, err
	}
	var chatRequest ChatRequest
	if err := common.Unmarshal(body, &chatRequest); err != nil {
		return nil, err
	}
	response, err := startTurn(c, info, &chatRequest)
	if err != nil {
		var upstream *upstreamError
		if errors.As(err, &upstream) {
			// Hand the upstream response back so the relay reports QCA's own
			// status code and error body instead of a generic 500.
			return upstream.response, nil
		}
		return nil, err
	}
	return response, nil
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	// The synthesized upstream body is always OpenAI chat shape, so the OpenAI
	// handlers own client-format conversion for OpenAI, Claude and Gemini.
	if info.RelayFormat == types.RelayFormatOpenAIResponses {
		if info.IsStream {
			return responsesStreamHandler(c, info, resp)
		}
		return responsesHandler(c, info, resp)
	}
	if info.IsStream {
		return openai.OaiStreamHandler(c, info, resp)
	}
	return openai.OpenaiHandler(c, info, resp)
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}

func baseURL(info *relaycommon.RelayInfo) string {
	if base := strings.TrimSuffix(info.ChannelBaseUrl, "/"); base != "" {
		return base
	}
	return strings.TrimSuffix(DefaultBaseURL, "/")
}
