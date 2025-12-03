package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
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

// isValidUTF8String 检查字符串是否为有效的UTF-8编码
func isValidUTF8String(s string) bool {
	return utf8.ValidString(s)
}

// isValidUTF8Bytes 检查字节切片是否为有效的UTF-8编码
func isValidUTF8Bytes(b []byte) bool {
	return utf8.Valid(b)
}

// cleanInvalidUTF8Chars 清理字符串中的无效UTF-8字符
func cleanInvalidUTF8Chars(s string) string {
	var result strings.Builder
	
	for _, r := range s {
		// 跳过无效的UTF-8字符
		if !utf8.ValidRune(r) {
			continue
		}
		
		// 跳过控制字符（除了常见的空白字符）
		if unicode.IsControl(r) && !strings.ContainsRune("\r\n\t", r) {
			continue
		}
		
		result.WriteRune(r)
	}
	
	return result.String()
}

// cleanInvalidUTF8Bytes 清理字节切片中的无效UTF-8字符
func cleanInvalidUTF8Bytes(b []byte) []byte {
	// 将字节切片转换为字符串，清理后再转回字节切片
	return []byte(strings.ToValidUTF8(string(b), ""))
}

// ClaudeMessagesToResponsesRequest 将 Claude Messages 请求转换为 Responses API 格式
// 参数:
//   - c: Gin 上下文
//   - claudeRequest: Claude Messages 请求对象
//   - info: 转发信息
// 返回:
//   - *dto.OpenAIResponsesRequest: 转换后的 Responses API 请求对象
//   - error: 转换失败时返回错误
func ClaudeMessagesToResponsesRequest(c *gin.Context, claudeRequest *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) (*dto.OpenAIResponsesRequest, error) {
	if claudeRequest == nil {
		return nil, fmt.Errorf("claude request is nil")
	}
	if claudeRequest.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// 创建Responses请求对象
	responsesReq := &dto.OpenAIResponsesRequest{
		Model:  info.UpstreamModelName,
		Stream: claudeRequest.Stream,
		TopP:   claudeRequest.TopP,
		User:   claudeRequest.User,
	}

	if claudeRequest.Temperature != nil {
		responsesReq.Temperature = *claudeRequest.Temperature
	}

	// 映射max_tokens到max_output_tokens
	if claudeRequest.MaxTokens > 0 {
		responsesReq.MaxOutputTokens = claudeRequest.MaxTokens
	} else if claudeRequest.MaxCompletionTokens > 0 {
		responsesReq.MaxOutputTokens = claudeRequest.MaxCompletionTokens
	}

	// 处理reasoning_effort参数
	if claudeRequest.ReasoningEffort != "" {
		responsesReq.Reasoning = &dto.Reasoning{
			Effort: claudeRequest.ReasoningEffort,
		}
	}

// 提取系统消息并设置为instructions
	systemMessage := extractSystemMessageFromClaude(claudeRequest.Messages)
	if systemMessage != "" {
		// 先序列化为 JSON 字符串，再转换为 RawMessage
		instructionsBytes, err := json.Marshal(systemMessage)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal instructions: %w", err)
		}
		responsesReq.Instructions = json.RawMessage(instructionsBytes)
	}

	// 转换messages为input格式
	inputs, err := convertClaudeMessagesToInputs(claudeRequest.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert claude messages to inputs: %w", err)
	}
	
	// 将inputs序列化为JSON RawMessage
	if len(inputs) > 0 {
		inputData, err := json.Marshal(inputs)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal inputs: %w", err)
		}
		responsesReq.Input = json.RawMessage(inputData)
	}

	// 处理tools参数
	if len(claudeRequest.Tools) > 0 {
		toolsData, err := json.Marshal(claudeRequest.Tools)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tools: %w", err)
		}
		responsesReq.Tools = json.RawMessage(toolsData)
	}

	// 处理tool_choice参数
	if claudeRequest.ToolChoice != nil {
		toolChoiceData, err := json.Marshal(claudeRequest.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool_choice: %w", err)
		}
		responsesReq.ToolChoice = json.RawMessage(toolChoiceData)
	}

	// 处理parallel_tool_calls参数
	if claudeRequest.ParallelToolCalls != nil {
		parallelData, err := json.Marshal(claudeRequest.ParallelToolCalls)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal parallel_tool_calls: %w", err)
		}
		responsesReq.ParallelToolCalls = json.RawMessage(parallelData)
	}

	// 处理其他可传递的参数
	// 注意：stop 和 response_format 参数在 Responses API 中可能不被支持
	// 这些参数会被忽略，不会传递给上游 API

	return responsesReq, nil
}

// extractSystemMessageFromClaude 从Claude消息列表中提取系统消息
// 参数:
//   - messages: Claude消息列表
// 返回:
//   - string: 系统消息内容，如果没有系统消息则返回空字符串
func extractSystemMessageFromClaude(messages []dto.Message) string {
	for _, message := range messages {
		if message.Role == "system" {
			// 处理不同类型的content
			if str, ok := message.Content.(string); ok {
				// 检查字符串是否包含无效的UTF-8字符
				if !isValidUTF8String(str) {
					// 清理无效字符
					str = cleanInvalidUTF8Chars(str)
				}
				return str
			}
			
			// 如果content是复杂类型，尝试转换为字符串
			if contentBytes, err := json.Marshal(message.Content); err == nil {
				// 验证生成的JSON是否有效
				if !isValidUTF8Bytes(contentBytes) {
					// 清理无效字符
					contentBytes = cleanInvalidUTF8Bytes(contentBytes)
				}
				return string(contentBytes)
			}
		}
	}
	return ""
}

// convertClaudeMessagesToInputs 将Claude的messages转换为Responses API的inputs格式
// 参数:
//   - messages: Claude消息列表
// 返回:
//   - []dto.Input: 转换后的Input数组
//   - error: 转换失败时返回错误
func convertClaudeMessagesToInputs(messages []dto.Message) ([]dto.Input, error) {
	var inputs []dto.Input
	
	for _, message := range messages {
		// 跳过系统消息，因为它们被单独处理为instructions
		if message.Role == "system" {
			continue
		}
		
		input := dto.Input{
			Type:    "message",
			Role:    message.Role,
		}
		
		// 处理content字段
		if message.Content != nil {
			// 验证content是否包含无效字符
			var contentBytes []byte
			var err error
			
			// 如果content是字符串，验证编码并使用
			if str, ok := message.Content.(string); ok {
				// 检查字符串是否包含无效的UTF-8字符
				if !isValidUTF8String(str) {
					// 清理无效字符
					str = cleanInvalidUTF8Chars(str)
				}
				contentBytes, err = json.Marshal(str)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal string content: %w", err)
				}
			} else {
				// 如果content是复杂类型，先验证再序列化
				// 使用json.Marshal然后验证结果
				contentBytes, err = json.Marshal(message.Content)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal complex content: %w", err)
				}
				
				// 验证生成的JSON是否有效
				if !isValidUTF8Bytes(contentBytes) {
					return nil, fmt.Errorf("generated JSON contains invalid UTF-8 characters")
				}
			}
			input.Content = json.RawMessage(contentBytes)
		}
		
		inputs = append(inputs, input)
	}
	return inputs, nil
}

// ResponsesToClaudeMessagesResponse 将Responses API响应转换为Claude Messages格式
// 参数:
//   - responsesResponse: Responses API响应对象
//   - originalRequest: 原始Claude请求对象
// 返回:
//   - *dto.OpenAITextResponse: 转换后的Claude Messages响应对象
//   - error: 转换失败时返回错误
func ResponsesToClaudeMessagesResponse(responsesResponse *dto.OpenAIResponsesResponse, originalRequest *dto.GeneralOpenAIRequest) (*dto.OpenAITextResponse, error) {
	if responsesResponse == nil {
		return nil, fmt.Errorf("responses response is nil")
	}

	// 处理错误响应
	if responsesResponse.Error != nil {
		// 返回带有错误的响应
		return &dto.OpenAITextResponse{
			Id:    responsesResponse.ID,
			Model: responsesResponse.Model,
			Error: responsesResponse.Error,
		}, nil
	}

	// 提取内容
	content := extractContentFromOutput(responsesResponse.Output)
	
	// 确定finish_reason
	finishReason := extractFinishReasonFromResponses(responsesResponse.Status)
	
	// 构建Choices
	choices := []dto.OpenAITextResponseChoice{
		{
			Index: 0,
			Message: dto.Message{
				Role:    "assistant",
				Content: content,
			},
			FinishReason: finishReason,
		},
	}

	// 构建最终响应
	claudeResponse := &dto.OpenAITextResponse{
		Id:      responsesResponse.ID,
		Model:   responsesResponse.Model,
		Object:  "chat.completion",
		Created: int64(responsesResponse.CreatedAt),
		Choices: choices,
	}

	// 处理Usage
	if responsesResponse.Usage != nil {
		claudeResponse.Usage = *responsesResponse.Usage
	}

	return claudeResponse, nil
}

// extractContentFromOutput 从Responses API的Output中提取文本内容
// 参数:
//   - output: Responses API的Output数组
// 返回:
//   - string: 提取的文本内容
func extractContentFromOutput(output []dto.ResponsesOutput) string {
	var contentBuilder string
	for _, item := range output {
		if item.Type == "message" && item.Role == "assistant" {
			for _, contentItem := range item.Content {
				if contentItem.Type == "output_text" {
					contentBuilder += contentItem.Text
				}
			}
		}
	}
	return contentBuilder
}

// extractFinishReasonFromResponses 根据Responses API的状态确定finish_reason
// 参数:
//   - status: Responses API的响应状态
// 返回:
//   - string: Claude Messages的finish_reason
func extractFinishReasonFromResponses(status string) string {
	switch status {
	case "completed":
		return "stop"
	case "incomplete":
		return "length" // 或者 "content_filter" 等，视具体情况而定
	case "failed":
		return "error" // Claude Messages API没有error作为finish_reason，但这是最接近的
	case "cancelled":
		return "stop"
	default:
		return "stop"
	}
}

// ResponsesToClaudeStreamHandler 处理Responses API流式响应并转换为Claude Messages格式
// 参数:
//   - c: Gin 上下文
//   - resp: HTTP响应对象
//   - info: 转发信息
// 返回:
//   - usage: 使用量统计
//   - err: 错误信息
func ResponsesToClaudeStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	// 检查是否是从Claude转换的请求
	convertedFromClaude, exists := c.Get("converted_from_claude")
	if !exists || !convertedFromClaude.(bool) {
		// 如果不是转换的请求，使用原有的Claude流处理器
		return ClaudeStreamHandler(c, resp, info, RequestModeMessage)
	}

	defer service.CloseResponseBodyGracefully(resp)

	// 初始化Claude流式响应信息结构
	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}

	// 用于收集完整的流式响应体
	var fullStreamResponse strings.Builder



	// 使用helper.StreamScannerHandler处理流式响应
	helper.StreamScannerHandler(c, resp, info, func(data string) bool {
// 保留完整响应体以便在请求失败时进行问题排查
if len(data) > 0 {
			fullStreamResponse.WriteString(data)
			fullStreamResponse.WriteString("\n")
		}

		// 解析Responses API流式响应
		var streamResponse dto.ResponsesStreamResponse
		if parseErr := common.UnmarshalJsonStr(data, &streamResponse); parseErr == nil {
			// 转换为Claude Messages流式格式
			claudeStreamResp := ConvertResponsesStreamToClaudeStream(&streamResponse, claudeInfo.ResponseId, info.UpstreamModelName)
			if claudeStreamResp != nil {
				// 发送Claude格式的流式数据
				sendClaudeStreamData(c, claudeStreamResp)
			}

		// 处理使用量统计
		switch streamResponse.Type {
		case "response.done":
			if streamResponse.Response != nil && streamResponse.Response.Usage != nil {
				if streamResponse.Response.Usage.InputTokens != 0 {
					claudeInfo.Usage.PromptTokens = streamResponse.Response.Usage.InputTokens
				}
				if streamResponse.Response.Usage.OutputTokens != 0 {
					claudeInfo.Usage.CompletionTokens = streamResponse.Response.Usage.OutputTokens
				}
				if streamResponse.Response.Usage.TotalTokens != 0 {
					claudeInfo.Usage.TotalTokens = streamResponse.Response.Usage.TotalTokens
				}
			}
		case "response.output_text.delta":
			// 处理输出文本用于备用token计算
			claudeInfo.ResponseText.WriteString(streamResponse.Delta)
		}
		} else {
			logger.LogError(c, "failed to unmarshal responses stream response: "+parseErr.Error())
		}
		return true
	})

	// 将完整的流式响应体存储到relayInfo中
	info.ResponseBody = fullStreamResponse.String()

	// 备用token计算
	if claudeInfo.Usage.CompletionTokens == 0 {
		tempStr := claudeInfo.ResponseText.String()
		if len(tempStr) > 0 {
			completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
			claudeInfo.Usage.CompletionTokens = completionTokens
		}
	}

	if claudeInfo.Usage.PromptTokens == 0 && claudeInfo.Usage.CompletionTokens != 0 {
		claudeInfo.Usage.PromptTokens = info.PromptTokens
	}

	claudeInfo.Usage.TotalTokens = claudeInfo.Usage.PromptTokens + claudeInfo.Usage.CompletionTokens

return claudeInfo.Usage, nil
}

// ResponsesToClaudeHandler 处理非流式Responses API响应并转换为Claude Messages格式
// 参数:
//   - c: Gin 上下文
//   - resp: HTTP响应对象
//   - info: 转发信息
// 返回:
//   - usage: 使用量统计
//   - err: 错误信息
func ResponsesToClaudeHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// 读取Responses API响应
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, types.NewOpenAIError(readErr, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	// 检查并清理响应体中的无效UTF-8字符
	if !utf8.Valid(responseBody) {
		responseBody = []byte(strings.ToValidUTF8(string(responseBody), ""))
	}

	// 将响应体存储到 relayInfo 中
	info.ResponseBody = string(responseBody)

	unmarshalErr := common.Unmarshal(responseBody, &responsesResponse)
	if unmarshalErr != nil {
		return nil, types.NewOpenAIError(unmarshalErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	// 检查错误响应
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	// 获取原始请求
	originalRequest, exists := c.Get("original_claude_request")
	if !exists {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("original claude request not found in context"),
			types.ErrorCodeConvertRequestFailed,
			http.StatusInternalServerError,
		)
	}

	claudeRequest, ok := originalRequest.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("invalid original request type"),
			types.ErrorCodeConvertRequestFailed,
			http.StatusInternalServerError,
		)
	}

	// 转换为Claude Messages格式
	claudeResponse, convertErr := ResponsesToClaudeMessagesResponse(&responsesResponse, claudeRequest)
	if convertErr != nil {
		logger.LogError(c, fmt.Sprintf("Failed to convert responses to claude format: %v", convertErr))
		return nil, types.NewError(convertErr, types.ErrorCodeBadResponse)
	}

	// 序列化Claude响应
	jsonData, marshalErr := json.Marshal(claudeResponse)
	if marshalErr != nil {
		return nil, types.NewOpenAIError(marshalErr, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}

	// 验证并清理生成的JSON中的无效UTF-8字符
	if !isValidUTF8Bytes(jsonData) {
		jsonData = cleanInvalidUTF8Bytes(jsonData)
	}

	// 写入转换后的响应体
	service.IOCopyBytesGracefully(c, resp, jsonData)

	// 返回使用量统计
	return &claudeResponse.Usage, nil
}

// ConvertResponsesStreamToClaudeStream 将Responses API流式响应转换为Claude Messages流式格式
// 参数:
//   - responsesStreamResp: Responses API流式响应对象
//   - responseID: 响应ID
//   - model: 模型名称
// 返回:
//   - *dto.ClaudeResponse: 转换后的Claude流式响应对象，如果是忽略的事件则返回nil
func ConvertResponsesStreamToClaudeStream(responsesStreamResp *dto.ResponsesStreamResponse, responseID string, model string) *dto.ClaudeResponse {
	if responsesStreamResp == nil {
		return nil
	}

	// 根据不同的事件类型进行处理
	switch responsesStreamResp.Type {
	case "response.created":
		// 响应创建事件 - 对应Claude的message_start
		if responsesStreamResp.Response != nil {
			claudeResp := &dto.ClaudeResponse{
				Type: "message_start",
				Message: &dto.ClaudeMediaMessage{
					Type:  "message",
					Id:    responsesStreamResp.Response.ID,
					Model: responsesStreamResp.Response.Model,
					Role:  "assistant",
				},
			}
			// 初始化usage
			if responsesStreamResp.Response.Usage != nil {
				claudeResp.Usage = &dto.ClaudeUsage{
					InputTokens:  responsesStreamResp.Response.Usage.InputTokens,
					OutputTokens: responsesStreamResp.Response.Usage.OutputTokens,
				}
			}
			return claudeResp
		}

	case "response.output_item.added":
		// 输出项添加事件 - 对应Claude的content_block_start
		if responsesStreamResp.Item != nil && responsesStreamResp.Item.Role == "assistant" {
			return &dto.ClaudeResponse{
				Type: "content_block_start",
				Index: common.GetPointer(0),
				ContentBlock: &dto.ClaudeMediaMessage{
					Type: "text",
					Text: common.GetPointer(""),
				},
			}
		}

	case "response.output_text.delta", "response.content_part.delta":
		// 内容增量事件 - 对应Claude的content_block_delta
		if responsesStreamResp.Delta != "" {
			return &dto.ClaudeResponse{
				Type:  "content_block_delta",
				Index: common.GetPointer(0),
				Delta: &dto.ClaudeMediaMessage{
					Type: "text_delta",
					Text: common.GetPointer(responsesStreamResp.Delta),
				},
			}
		}

	case "response.output_item.done":
		// 输出项完成事件 - 对应Claude的content_block_stop
		return &dto.ClaudeResponse{
			Type:  "content_block_stop",
			Index: common.GetPointer(0),
		}

case "response.done", "response.completed":
		// 响应完成事件 - 对应Claude的message_delta和message_stop
		if responsesStreamResp.Response != nil {
			// 先发送message_delta包含最终usage
			stopReason := extractFinishReasonFromResponses(responsesStreamResp.Response.Status)
			claudeResp := &dto.ClaudeResponse{
				Type: "message_delta",
				Delta: &dto.ClaudeMediaMessage{
					StopReason: &stopReason,
				},
			}
			if responsesStreamResp.Response.Usage != nil {
				claudeResp.Usage = &dto.ClaudeUsage{
					InputTokens:  responsesStreamResp.Response.Usage.InputTokens,
					OutputTokens: responsesStreamResp.Response.Usage.OutputTokens,
				}
			}
			return claudeResp
		}
	}

	// 忽略的事件类型返回nil
	return nil
}

// sendClaudeStreamData 发送Claude Messages流式数据
// 参数:
//   - c: Gin上下文
//   - claudeResp: Claude响应对象
func sendClaudeStreamData(c *gin.Context, claudeResp *dto.ClaudeResponse) {
	if claudeResp == nil {
		return
	}

	jsonData, err := json.Marshal(claudeResp)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("Failed to marshal claude stream response: %v", err))
		return
	}

	// 构建SSE格式
	data := fmt.Sprintf("data: %s\n\n", string(jsonData))
	c.Writer.Write([]byte(data))
	c.Writer.Flush()
}