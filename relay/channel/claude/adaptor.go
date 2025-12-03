package claude

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	RequestModeCompletion = 1
	RequestModeMessage    = 2
)

type Adaptor struct {
	RequestMode int
}

func (a *Adaptor) ConvertGeminiRequest(*gin.Context, *relaycommon.RelayInfo, *dto.GeminiChatRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	if strings.HasPrefix(info.UpstreamModelName, "claude-2") || strings.HasPrefix(info.UpstreamModelName, "claude-instant") {
		a.RequestMode = RequestModeCompletion
	} else {
		a.RequestMode = RequestModeMessage
	}
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	baseURL := ""
	if a.RequestMode == RequestModeMessage {
		baseURL = fmt.Sprintf("%s/v1/messages", info.ChannelBaseUrl)
	} else {
		baseURL = fmt.Sprintf("%s/v1/complete", info.ChannelBaseUrl)
	}
	if info.IsClaudeBetaQuery {
		baseURL = baseURL + "?beta=true"
	}
	return baseURL, nil
}

func CommonClaudeHeadersOperation(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) {
	// common headers operation
	anthropicBeta := c.Request.Header.Get("anthropic-beta")
	if anthropicBeta != "" {
		req.Set("anthropic-beta", anthropicBeta)
	}
	model_setting.GetClaudeSettings().WriteHeaders(info.OriginModelName, req)
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	req.Set("x-api-key", info.ApiKey)
	anthropicVersion := c.Request.Header.Get("anthropic-version")
	if anthropicVersion == "" {
		anthropicVersion = "2023-06-01"
	}
	req.Set("anthropic-version", anthropicVersion)
	CommonClaudeHeadersOperation(c, req, info)
	return nil
}

// ClaudeSmartRoutingConfig 智能路由配置
type ClaudeSmartRoutingConfig struct {
	Enabled         bool     `json:"enabled"`
	ResponsesModels []string `json:"responses_models"`
	FallbackOnError bool     `json:"fallback_on_error"`
}

// shouldRouteToResponses 根据模型名称判断是否应该路由到 Responses 渠道
func (a *Adaptor) shouldRouteToResponses(modelName string) bool {
	// 定义应该路由到 Responses 渠道的模型列表
	responsesModels := []string{
		"claude-3.5-sonnet",
		"claude-3-opus", 
		"claude-3-haiku",
		// 可以根据实际情况扩展
	}
	
	for _, model := range responsesModels {
		if modelName == model {
			return true
		}
	}
	return false
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}

	// 智能路由检测：检查是否应该路由到 Responses 渠道
	if a.shouldRouteToResponses(info.OriginModelName) {
		// 标记这是一个转换后的请求，用于响应处理阶段
		c.Set("converted_from_claude", true)
		
		// 保存原始请求，用于响应转换时参考
		c.Set("original_claude_request", request)
		
		// 调用转换器进行格式转换 - 这里需要实现 ClaudeMessagesToResponsesRequest
		responsesReq, err := ClaudeMessagesToResponsesRequest(c, request, info)
if err != nil {
			// 转换失败时回退到原生 Claude 处理，保证服务可用性
			logger.LogWarn(c, fmt.Sprintf("Smart routing conversion failed for model %s: %v, fallback to native Claude", info.OriginModelName, err))
			if a.RequestMode == RequestModeCompletion {
				return RequestOpenAI2ClaudeComplete(*request), nil
			} else {
				return RequestOpenAI2ClaudeMessage(c, *request)
			}
		}
		
		// 更新 RelayMode 为 Responses 模式
		info.RelayMode = relayconstant.RelayModeResponses
		
		return responsesReq, nil
	}

	if a.RequestMode == RequestModeCompletion {
		return RequestOpenAI2ClaudeComplete(*request), nil
	} else {
		return RequestOpenAI2ClaudeMessage(c, *request)
	}
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	// TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// 检查是否是从Claude转换的请求
	convertedFromClaude, exists := c.Get("converted_from_claude")
	if exists && convertedFromClaude.(bool) {
		// 如果是转换的请求，使用Responses流处理器
		if info.IsStream {
			return ResponsesToClaudeStreamHandler(c, resp, info)
		} else {
			// 非流式响应处理 - 调用ResponsesToClaudeMessagesResponse进行转换
			return ResponsesToClaudeHandler(c, resp, info)
		}
	}

	// 原有的Claude响应处理逻辑
	if info.IsStream {
		return ClaudeStreamHandler(c, resp, info, a.RequestMode)
	} else {
		return ClaudeHandler(c, resp, info, a.RequestMode)
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
