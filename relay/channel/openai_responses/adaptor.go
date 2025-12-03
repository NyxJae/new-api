package openai_responses

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// Adaptor OpenAI Responses API 专用适配器
// 该适配器专门处理 OpenAI Responses API 请求，不支持其他 OpenAI 接口
type Adaptor struct {
	ChannelType int // 渠道类型
}

// Init 初始化适配器
// 参数:
//   - info: 转发信息，包含渠道类型等配置
func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType
}

// GetRequestURL 获取请求 URL
// 该方法仅支持 Responses API 请求，其他请求类型将返回错误
// 参数:
//   - info: 转发信息，包含基础 URL 和请求路径
// 返回:
//   - string: 完整的请求 URL
//   - error: 如果不是 Responses API 请求则返回错误
func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.RelayMode != relayconstant.RelayModeResponses {
		return "", fmt.Errorf("OpenAI Responses 渠道仅支持 /v1/responses 接口，当前请求: %s", info.RequestURLPath)
	}
	return fmt.Sprintf("%s/v1/responses", info.ChannelBaseUrl), nil
}

// SetupRequestHeader 设置请求头
// 添加必要的认证信息和其他请求头
// 参数:
//   - c: Gin 上下文
//   - header: HTTP 请求头
//   - info: 转发信息，包含 API Key 等认证信息
// 返回:
//   - error: 设置失败时返回错误
func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

// ConvertClaudeRequest Claude 请求转换
// 支持 Claude Messages API 格式转换为 Responses API 格式
// 参数:
//   - c: Gin 上下文
//   - info: 转发信息
//   - request: Claude Messages API 请求对象
// 返回:
//   - any: 转换后的 Responses API 请求对象
//   - error: 转换失败时返回错误
func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	if request == nil {
		return nil, fmt.Errorf("claude request is nil")
	}
	if request.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// 标记这是一个转换后的请求，用于响应处理阶段
	c.Set("converted_from_claude", true)
	
	// 保存原始请求，用于响应转换时参考
	c.Set("original_claude_request", request)
	
	// 调用转换器进行格式转换
	responsesReq, err := ClaudeMessagesToResponsesRequest(c, request, info)
	if err != nil {
		return nil, fmt.Errorf("failed to convert claude messages request: %w", err)
	}
	
	// 更新 RelayMode 为 Responses 模式
	info.RelayMode = relayconstant.RelayModeResponses
	
	return responsesReq, nil
}

// ConvertGeminiRequest Gemini 请求转换（不支持）
// 该渠道不支持 Gemini 格式的请求
// 返回:
//   - error: 始终返回不支持的错误
func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	return nil, fmt.Errorf("OpenAI Responses 渠道不支持 Gemini 请求")
}

// ConvertOpenAIRequest OpenAI 通用请求转换
// 支持智能路由：自动检测并转换 Chat Completions 请求到 Responses API 格式
// 参数:
//   - request: OpenAI 通用请求对象
// 返回:
//   - any: 转换后的请求对象
//   - error: 转换失败时返回错误
func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}

	// 智能路由检测：如果是 Chat Completions 请求，自动转换为 Responses API 格式
	if info.RelayMode == relayconstant.RelayModeChatCompletions {
		// 标记这是一个转换后的请求，用于响应处理阶段
		c.Set("converted_from_chat", true)
		
		// 保存原始请求，用于响应转换时参考
		c.Set("original_chat_request", request)
		
		// 调用转换器进行格式转换
		responsesReq, err := ChatCompletionsToResponsesRequest(c, request, info)
		if err != nil {
			return nil, fmt.Errorf("failed to convert chat completions request: %w", err)
		}
		
		// 更新 RelayMode 为 Responses 模式
		info.RelayMode = relayconstant.RelayModeResponses
		
		return responsesReq, nil
	}

	// 如果是 Responses API 请求，直接返回
	if info.RelayMode == relayconstant.RelayModeResponses {
		return request, nil
	}

	// 不支持的请求模式
	return nil, fmt.Errorf("OpenAI Responses 渠道仅支持 Chat Completions 和 Responses API 请求")
}

// ConvertOpenAIResponsesRequest Responses API 请求转换
// 转换并验证 Responses API 请求，设置上游模型名称
// 参数:
//   - request: Responses API 请求对象
// 返回:
//   - any: 转换后的请求对象
//   - error: 验证失败时返回错误
func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	if request.Model == "" {
		return nil, errors.New("model is required")
	}
	request.Model = info.UpstreamModelName
	return request, nil
}

// ConvertRerankRequest Rerank 请求转换（不支持）
// 该渠道不支持 Rerank 接口
// 返回:
//   - error: 始终返回不支持的错误
func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, fmt.Errorf("OpenAI Responses 渠道不支持 Rerank 接口")
}

// ConvertEmbeddingRequest Embedding 请求转换（不支持）
// 该渠道不支持 Embedding 接口
// 返回:
//   - error: 始终返回不支持的错误
func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return nil, fmt.Errorf("OpenAI Responses 渠道不支持 Embedding 接口")
}

// ConvertAudioRequest Audio 请求转换（不支持）
// 该渠道不支持 Audio 接口
// 返回:
//   - error: 始终返回不支持的错误
func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	return nil, fmt.Errorf("OpenAI Responses 渠道不支持 Audio 接口")
}

// ConvertImageRequest Image 请求转换（不支持）
// 该渠道不支持 Image 接口
// 返回:
//   - error: 始终返回不支持的错误
func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	return nil, fmt.Errorf("OpenAI Responses 渠道不支持 Image 接口")
}

// DoRequest 执行 HTTP 请求
// 发送请求到上游 API 服务
// 参数:
//   - c: Gin 上下文
//   - info: 转发信息
//   - requestBody: 请求体
// 返回:
//   - any: 响应数据
//   - error: 请求失败时返回错误
func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

// DoResponse 处理 HTTP 响应
// 根据流式或非流式模式处理响应数据，支持智能响应转换
// 参数:
//   - c: Gin 上下文
//   - resp: HTTP 响应对象
//   - info: 转发信息
// 返回:
//   - usage: 使用量统计信息
//   - err: 处理失败时返回错误
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// 检查是否是从 Chat Completions 转换来的请求
	convertedFromChat, _ := c.Get("converted_from_chat")
	isConvertedFromChat := convertedFromChat == true

	// 检查是否是从 Claude Messages 转换来的请求
	convertedFromClaude, _ := c.Get("converted_from_claude")
	isConvertedFromClaude := convertedFromClaude == true

	// 如果是从 Chat Completions 转换来的请求，需要将响应转换回 Chat Completions 格式
	if isConvertedFromChat {
		if info.IsStream {
			// 流式响应转换：调用专用的转换处理器
			usage, err = ResponsesToChatStreamHandler(c, info, resp)
		} else {
			// 非流式响应转换：调用专用的转换处理器
			usage, err = ResponsesToChatHandler(c, info, resp)
		}
		return
	}

	// 如果是从 Claude Messages 转换来的请求，需要将响应转换回 Claude Messages 格式
	if isConvertedFromClaude {
		if info.IsStream {
			// 流式响应转换：调用 Claude 专用的转换处理器
			usage, err = ResponsesToClaudeStreamHandler(c, info, resp)
		} else {
			// 非流式响应转换：调用 Claude 专用的转换处理器
			usage, err = ResponsesToClaudeHandler(c, info, resp)
		}
		return
	}

	// 原生 Responses API 请求，直接处理
	if info.RelayMode != relayconstant.RelayModeResponses {
		return nil, types.NewError(
			fmt.Errorf("OpenAI Responses 渠道仅支持 /v1/responses 接口"),
			types.ErrorCodeBadResponse,
		)
	}
	
	if info.IsStream {
		usage, err = openai.OaiResponsesStreamHandler(c, info, resp)
	} else {
		usage, err = openai.OaiResponsesHandler(c, info, resp)
	}
	return
}

// GetModelList 获取支持的模型列表
// 返回该渠道支持的所有模型名称
// 返回:
//   - []string: 模型名称列表
func (a *Adaptor) GetModelList() []string {
	return ModelList
}

// GetChannelName 获取渠道名称
// 返回该渠道的显示名称
// 返回:
//   - string: 渠道名称
func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
