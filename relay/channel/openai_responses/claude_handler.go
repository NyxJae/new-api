package openai_responses

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// ResponsesToClaudeHandler 处理从 Responses API 到 Claude Messages API 的响应转换
// 用于智能路由场景：当 Claude 请求被路由到 Responses 渠道时
func ResponsesToClaudeHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// 获取原始请求（用于转换时参考）
	originalRequest, exists := c.Get("original_claude_request")
	if !exists {
		return nil, types.NewError(fmt.Errorf("original claude request not found"), types.ErrorCodeInvalidRequest)
	}

	claudeRequest, ok := originalRequest.(*dto.ClaudeRequest)
	if !ok {
		return nil, types.NewError(fmt.Errorf("invalid original request type"), types.ErrorCodeInvalidRequest)
	}

	// 读取 Responses API 响应
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	// 检查并清理响应体中的无效UTF-8字符
	if !utf8.Valid(responseBody) {
		responseBody = []byte(strings.ToValidUTF8(string(responseBody), ""))
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

	// 转换为 Claude Messages 格式
	claudeResponse, err := ResponsesToClaudeResponse(&responsesResponse, claudeRequest)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("Failed to convert responses to claude format: %v", err))
		return nil, types.NewError(err, types.ErrorCodeBadResponse)
	}

	// 序列化 Claude 响应
	jsonData, err := json.Marshal(claudeResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}

	// 验证并清理生成的JSON中的无效UTF-8字符
	if !isValidUTF8Bytes(jsonData) {
		jsonData = cleanInvalidUTF8Bytes(jsonData)
	}

	// 写入转换后的响应体
	service.IOCopyBytesGracefully(c, resp, jsonData)

	// 计算使用量
	usage := dto.Usage{}
	if responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
	}

	return &usage, nil
}

// ResponsesToClaudeStreamHandler 处理从 Responses API 流式到 Claude Messages 流式的响应转换
// 用于智能路由场景：当 Claude 流式请求被路由到 Responses 渠道时
func ResponsesToClaudeStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
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

	// 用于跟踪是否已发送 message_start 事件
	messageStartSent := false

	helper.StreamScannerHandler(c, resp, info, func(data string) bool {
		// 收集流式响应数据
		fullStreamResponse.WriteString(data)
		fullStreamResponse.WriteString("\n")

		// 解析 Responses API 流式响应
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err == nil {
			// 获取响应ID
			if streamResponse.Response != nil && streamResponse.Response.ID != "" {
				responseID = streamResponse.Response.ID
			}

			// 如果是第一次收到有效数据，发送 message_start 事件
			if !messageStartSent && responseID != "" {
				// 发送 message_start 事件
				sendClaudeMessageStart(c, responseID, info.UpstreamModelName)
				// 发送 content_block_start 事件
				sendClaudeContentBlockStart(c, 0)
				messageStartSent = true
			}

			// 处理输出文本增量
			if streamResponse.Type == "response.output_text.delta" && streamResponse.Delta != "" {
				// 发送 content_block_delta 事件
				sendClaudeContentBlockDelta(c, 0, streamResponse.Delta)
				responseTextBuilder.WriteString(streamResponse.Delta)
			}

			// 处理使用量统计
			if streamResponse.Type == "response.done" && streamResponse.Response != nil {
				// 发送 content_block_stop 事件
				sendClaudeContentBlockStop(c, 0)
				// 发送 message_delta 事件 (包含 stop_reason)
				sendClaudeMessageDelta(c, "end_turn", streamResponse.Response.Usage)
				// 发送 message_stop 事件
				sendClaudeMessageStop(c)

				// 更新使用量
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

// ResponsesToClaudeResponse 将 Responses API 响应转换为 Claude Messages 格式
func ResponsesToClaudeResponse(responsesResponse *dto.OpenAIResponsesResponse, originalRequest *dto.ClaudeRequest) (*dto.ClaudeResponse, error) {
	if responsesResponse == nil {
		return nil, fmt.Errorf("responses response is nil")
	}

	// 提取内容
	content := extractContentFromOutput(responsesResponse.Output)

	// 确定 finish_reason
	stopReason := extractClaudeStopReason(responsesResponse.Status)

	// 构建 content 数组
	contentList := []dto.ClaudeMediaMessage{
		{
			Type: "text",
			Text: &content,
		},
	}

	// 构建使用量
	var usage *dto.ClaudeUsage
	if responsesResponse.Usage != nil {
		usage = &dto.ClaudeUsage{
			InputTokens:  responsesResponse.Usage.InputTokens,
			OutputTokens: responsesResponse.Usage.OutputTokens,
		}
	}

	// 构建 Claude 响应
	claudeResponse := &dto.ClaudeResponse{
		Id:         responsesResponse.ID,
		Type:       "message",
		Role:       "assistant",
		Content:    contentList,
		Model:      responsesResponse.Model,
		StopReason: stopReason,
		Usage:      usage,
	}

	return claudeResponse, nil
}

// extractClaudeStopReason 根据 Responses API 的状态确定 Claude 的 stop_reason
func extractClaudeStopReason(status string) string {
	switch status {
	case "completed":
		return "end_turn"
	case "incomplete":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// sendClaudeMessageStart 发送 message_start 事件
func sendClaudeMessageStart(c *gin.Context, id string, model string) {
	usage := &dto.ClaudeUsage{
		InputTokens:  0,
		OutputTokens: 0,
	}
	message := &dto.ClaudeMediaMessage{
		Type:  "message",
		Model: model,
		Role:  "assistant",
		Usage: usage, // 添加 usage 字段到 message 对象
	}
	resp := dto.ClaudeResponse{
		Type:    "message_start",
		Message: message,
		Usage:   usage,
	}
	sendClaudeStreamData(c, resp)
}

// sendClaudeContentBlockStart 发送 content_block_start 事件
func sendClaudeContentBlockStart(c *gin.Context, index int) {
	text := ""
	resp := dto.ClaudeResponse{
		Type: "content_block_start",
		ContentBlock: &dto.ClaudeMediaMessage{
			Type: "text",
			Text: &text,
		},
	}
	resp.SetIndex(index)
	sendClaudeStreamData(c, resp)
}

// sendClaudeContentBlockDelta 发送 content_block_delta 事件
func sendClaudeContentBlockDelta(c *gin.Context, index int, delta string) {
	resp := dto.ClaudeResponse{
		Type: "content_block_delta",
		Delta: &dto.ClaudeMediaMessage{
			Type: "text_delta",
			Text: &delta,
		},
	}
	resp.SetIndex(index)
	sendClaudeStreamData(c, resp)
}

// sendClaudeContentBlockStop 发送 content_block_stop 事件
func sendClaudeContentBlockStop(c *gin.Context, index int) {
	resp := dto.ClaudeResponse{
		Type: "content_block_stop",
	}
	resp.SetIndex(index)
	sendClaudeStreamData(c, resp)
}

// sendClaudeMessageDelta 发送 message_delta 事件
func sendClaudeMessageDelta(c *gin.Context, stopReason string, usage *dto.Usage) {
	outputTokens := 0
	if usage != nil {
		outputTokens = usage.OutputTokens
	}
	resp := dto.ClaudeResponse{
		Type: "message_delta",
		Delta: &dto.ClaudeMediaMessage{
			StopReason: &stopReason,
		},
		Usage: &dto.ClaudeUsage{
			OutputTokens: outputTokens,
		},
	}
	sendClaudeStreamData(c, resp)
}

// sendClaudeMessageStop 发送 message_stop 事件
func sendClaudeMessageStop(c *gin.Context) {
	resp := dto.ClaudeResponse{
		Type: "message_stop",
	}
	sendClaudeStreamData(c, resp)
}

// sendClaudeStreamData 发送 Claude 流式数据
func sendClaudeStreamData(c *gin.Context, response dto.ClaudeResponse) {
	jsonData, err := json.Marshal(response)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("Failed to marshal claude stream response: %v", err))
		return
	}
	// Claude 流式格式：event: type\ndata: json\n\n
	c.Writer.WriteString(fmt.Sprintf("event: %s\n", response.Type))
	c.Writer.WriteString(fmt.Sprintf("data: %s\n\n", string(jsonData)))
	c.Writer.Flush()
}