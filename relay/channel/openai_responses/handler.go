package openai_responses

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// ResponsesToChatHandler 处理从 Responses API 到 Chat Completions 的响应转换
// 用于智能路由场景：当 Chat Completions 请求被路由到 Responses 渠道时
func ResponsesToChatHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// 获取原始请求（用于转换时参考）
	originalRequest, exists := c.Get("original_chat_request")
	if !exists {
		return nil, types.NewError(fmt.Errorf("original chat request not found"), types.ErrorCodeInvalidRequest)
	}

	chatRequest, ok := originalRequest.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, types.NewError(fmt.Errorf("invalid original request type"), types.ErrorCodeInvalidRequest)
	}

	// 读取 Responses API 响应
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	// 将响应体存储到 relayInfo 中
	info.ResponseBody = string(responseBody)

	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	// 检查错误响应
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// 转换为 Chat Completions 格式
	chatResponse, err := ResponsesToChatCompletionsResponse(&responsesResponse, chatRequest)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("Failed to convert responses to chat format: %v", err))
		return nil, types.NewError(err, types.ErrorCodeBadResponse)
	}

// 序列化 Chat Completions 响应
	jsonData, err := json.Marshal(chatResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}

	// 写入转换后的响应体
	service.IOCopyBytesGracefully(c, resp, jsonData)

	// 计算使用量
	usage := dto.Usage{}
	if responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
		if responsesResponse.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = responsesResponse.Usage.InputTokensDetails.CachedTokens
		}
	}

	// 处理内置工具用量统计
	if info != nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
		for _, tool := range responsesResponse.Tools {
			buildToolinfo, ok := info.ResponsesUsageInfo.BuiltInTools[common.Interface2String(tool["type"])]
			if !ok || buildToolinfo == nil {
				logger.LogError(c, fmt.Sprintf("BuiltInTools not found for tool type: %v", tool["type"]))
				continue
			}
			buildToolinfo.CallCount++
		}
	}

	return &usage, nil
}

// ResponsesToChatStreamHandler 处理从 Responses API 流式到 Chat Completions 流式的响应转换
// 用于智能路由场景：当 Chat Completions 流式请求被路由到 Responses 渠道时
func ResponsesToChatStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder

	// 用于收集完整的流式响应体
	var fullStreamResponse strings.Builder

	// 获取响应ID，用于流式响应
	var responseID string

	helper.StreamScannerHandler(c, resp, info, func(data string) bool {
		// 累积完整响应体用于日志记录
		if len(data) > 0 {
			fullStreamResponse.WriteString(data)
			fullStreamResponse.WriteString("\n")
		}

		// 解析 Responses API 流式响应
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err == nil {
			// 获取响应ID
			if streamResponse.Response != nil && streamResponse.Response.ID != "" {
				responseID = streamResponse.Response.ID
			}

			// 转换为 Chat Completions 流式格式
			chatStreamResp := ConvertResponsesStreamToChatStream(&streamResponse, responseID, info.UpstreamModelName)
			if chatStreamResp != nil {
				// 发送转换后的流式数据
				sendChatStreamData(c, *chatStreamResp)
			}

			// 处理使用量统计
			switch streamResponse.Type {
			case "response.completed":
				if streamResponse.Response != nil {
					if streamResponse.Response.Usage != nil {
						if streamResponse.Response.Usage.InputTokens != 0 {
							usage.PromptTokens = streamResponse.Response.Usage.InputTokens
						}
						if streamResponse.Response.Usage.OutputTokens != 0 {
							usage.CompletionTokens = streamResponse.Response.Usage.OutputTokens
						}
						if streamResponse.Response.Usage.TotalTokens != 0 {
							usage.TotalTokens = streamResponse.Response.Usage.TotalTokens
						}
						if streamResponse.Response.Usage.InputTokensDetails != nil {
							usage.PromptTokensDetails.CachedTokens = streamResponse.Response.Usage.InputTokensDetails.CachedTokens
						}
					}
				}
			case "response.output_text.delta":
				// 处理输出文本用于备用 token 计算
				responseTextBuilder.WriteString(streamResponse.Delta)
			case dto.ResponsesOutputTypeItemDone:
				// 函数调用处理
				if streamResponse.Item != nil {
					switch streamResponse.Item.Type {
					case dto.BuildInCallWebSearchCall:
						if info != nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
							if webSearchTool, exists := info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview]; exists && webSearchTool != nil {
								webSearchTool.CallCount++
							}
						}
					}
				}
			}
		} else {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
		}
		return true
	})

	// 将完整的流式响应体存储到 relayInfo 中
	info.ResponseBody = fullStreamResponse.String()

	// 备用 token 计算
	if usage.CompletionTokens == 0 {
		tempStr := responseTextBuilder.String()
		if len(tempStr) > 0 {
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			usage.CompletionTokens = completionTokens
		}
	}

	if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
		usage.PromptTokens = info.PromptTokens
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	return usage, nil
}

// sendChatStreamData 发送 Chat Completions 流式数据
func sendChatStreamData(c *gin.Context, response dto.ChatCompletionsStreamResponse) {
	jsonData, err := json.Marshal(response)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("Failed to marshal chat stream response: %v", err))
		return
	}

	// 构建 SSE 格式
	data := fmt.Sprintf("data: %s\n\n", string(jsonData))
	c.Writer.Write([]byte(data))
	c.Writer.Flush()
}