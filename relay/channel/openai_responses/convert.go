package openai_responses

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// ChatCompletionsToResponsesRequest 将Chat Completions请求转换为Responses API格式
// 参数:
//   - c: Gin 上下文
//   - chatRequest: Chat Completions请求对象
//   - info: 转发信息，包含模型映射等信息
// 返回:
//   - *dto.OpenAIResponsesRequest: 转换后的Responses API请求对象
//   - error: 转换失败时返回错误
func ChatCompletionsToResponsesRequest(c *gin.Context, chatRequest *dto.GeneralOpenAIRequest, info *relaycommon.RelayInfo) (*dto.OpenAIResponsesRequest, error) {
	if chatRequest == nil {
		return nil, fmt.Errorf("chat request is nil")
	}
	if chatRequest.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// 创建Responses请求对象
	responsesReq := &dto.OpenAIResponsesRequest{
		Model:  info.UpstreamModelName,
		Stream: chatRequest.Stream,
		TopP:   chatRequest.TopP,
		User:   chatRequest.User,
	}

	if chatRequest.Temperature != nil {
		responsesReq.Temperature = *chatRequest.Temperature
	}

	// 映射max_tokens到max_output_tokens
	if chatRequest.MaxTokens > 0 {
		responsesReq.MaxOutputTokens = chatRequest.MaxTokens
	} else if chatRequest.MaxCompletionTokens > 0 {
		responsesReq.MaxOutputTokens = chatRequest.MaxCompletionTokens
	}

	// 处理reasoning_effort参数
	if chatRequest.ReasoningEffort != "" {
		responsesReq.Reasoning = &dto.Reasoning{
			Effort: chatRequest.ReasoningEffort,
		}
	}

	// 提取系统消息并设置为instructions
	systemMessage := extractSystemMessage(chatRequest.Messages)
	if systemMessage != "" {
		instructions := json.RawMessage([]byte(systemMessage))
		responsesReq.Instructions = instructions
	}

	// 转换messages为input格式
	inputs, err := convertMessagesToInputs(chatRequest.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages to inputs: %w", err)
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
	if len(chatRequest.Tools) > 0 {
		toolsData, err := json.Marshal(chatRequest.Tools)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tools: %w", err)
		}
		responsesReq.Tools = json.RawMessage(toolsData)
	}

	// 处理tool_choice参数
	if chatRequest.ToolChoice != nil {
		toolChoiceData, err := json.Marshal(chatRequest.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool_choice: %w", err)
		}
		responsesReq.ToolChoice = json.RawMessage(toolChoiceData)
	}

// 处理parallel_tool_calls参数
	if chatRequest.ParallelToolCalls != nil {
		parallelData, err := json.Marshal(chatRequest.ParallelToolCalls)
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

// extractSystemMessage 从消息列表中提取系统消息
// 参数:
//   - messages: 消息列表
// 返回:
//   - string: 系统消息内容，如果没有系统消息则返回空字符串
func extractSystemMessage(messages []dto.Message) string {
	for _, message := range messages {
		if message.Role == "system" {
			// 处理不同类型的content
			if str, ok := message.Content.(string); ok {
				return str
			}
			
			// 如果content是复杂类型，尝试转换为字符串
			if contentBytes, err := json.Marshal(message.Content); err == nil {
				return string(contentBytes)
			}
		}
	}
	return ""
}

// convertMessagesToInputs 将Chat Completions的messages转换为Responses API的inputs格式
// 参数:
//   - messages: Chat Completions消息列表
// 返回:
//   - []dto.Input: 转换后的Input数组
//   - error: 转换失败时返回错误
func convertMessagesToInputs(messages []dto.Message) ([]dto.Input, error) {
	var inputs []dto.Input
	
	for _, message := range messages {
		// 跳过系统消息，因为它们被单独处理为instructions
		if message.Role == "system" {
			continue
		}
		
		input := dto.Input{
			Type:    "input",
			Role:    message.Role,
		}
		
		// 处理content字段
		if message.Content != nil {
			// 如果content是字符串，直接使用
			if str, ok := message.Content.(string); ok {
				contentBytes, err := json.Marshal(str)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal string content: %w", err)
				}
				input.Content = json.RawMessage(contentBytes)
			} else {
				// 如果content是复杂类型，直接序列化
				contentBytes, err := json.Marshal(message.Content)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal complex content: %w", err)
				}
				input.Content = json.RawMessage(contentBytes)
			}
		}
		
		inputs = append(inputs, input)
	}
return inputs, nil
}

// ResponsesToChatCompletionsResponse 将Responses API响应转换为Chat Completions格式
// 参数:
//   - responsesResponse: Responses API响应对象
//   - originalRequest: 原始Chat Completions请求对象
// 返回:
//   - *dto.OpenAITextResponse: 转换后的Chat Completions响应对象
//   - error: 转换失败时返回错误
func ResponsesToChatCompletionsResponse(responsesResponse *dto.OpenAIResponsesResponse, originalRequest *dto.GeneralOpenAIRequest) (*dto.OpenAITextResponse, error) {
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
	finishReason := extractFinishReason(responsesResponse.Status)
	
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
	chatResponse := &dto.OpenAITextResponse{
		Id:      responsesResponse.ID,
		Model:   responsesResponse.Model,
		Object:  "chat.completion",
		Created: int64(responsesResponse.CreatedAt),
		Choices: choices,
	}

	// 处理Usage
	if responsesResponse.Usage != nil {
		chatResponse.Usage = *responsesResponse.Usage
	}

	return chatResponse, nil
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
				if contentItem.Type == "text" {
					contentBuilder += contentItem.Text
				}
			}
		}
	}
	return contentBuilder
}

// extractFinishReason 根据Responses API的状态确定finish_reason
// 参数:
//   - status: Responses API的响应状态
// 返回:
//   - string: Chat Completions的finish_reason
func extractFinishReason(status string) string {
	switch status {
	case "completed":
		return "stop"
	case "incomplete":
		return "length" // 或者 "content_filter" 等，视具体情况而定
	case "failed":
		return "error" // Chat Completions API没有error作为finish_reason，但这是最接近的
	case "cancelled":
		return "stop"
	default:
		return "stop"
	}
}

// ConvertResponsesStreamToChatStream 将Responses API流式事件转换为Chat Completions流式事件
// 参数:
//   - responsesStreamResp: Responses API流式响应对象
//   - responseID: 响应ID，如果为空则使用responsesStreamResp中的ID
//   - model: 模型名称
// 返回:
//   - *dto.ChatCompletionsStreamResponse: 转换后的Chat Completions流式响应对象，如果是忽略的事件则返回nil
func ConvertResponsesStreamToChatStream(responsesStreamResp *dto.ResponsesStreamResponse, responseID string, model string) *dto.ChatCompletionsStreamResponse {
	if responsesStreamResp == nil {
		return nil
	}

	// 获取ID
	currentID := responseID
	if responsesStreamResp.Response != nil && responsesStreamResp.Response.ID != "" {
		currentID = responsesStreamResp.Response.ID
	}

	// 初始化基本的Chat Completions流式响应
	chatStreamResp := &dto.ChatCompletionsStreamResponse{
		Id:      currentID,
		Object:  "chat.completion.chunk",
		Created: 0, // 这里的created通常是时间戳，流式中可能不包含，或者从Response中获取
		Model:   model,
		Choices: []dto.ChatCompletionsStreamResponseChoice{},
	}
	
	if responsesStreamResp.Response != nil {
		chatStreamResp.Created = int64(responsesStreamResp.Response.CreatedAt)
	}

	// 根据不同的事件类型进行处理
	switch responsesStreamResp.Type {
	case "response.content_part.delta":
		// 内容增量事件
		if responsesStreamResp.Delta != "" {
			content := responsesStreamResp.Delta
			choice := dto.ChatCompletionsStreamResponseChoice{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Content: &content,
				},
			}
			chatStreamResp.Choices = append(chatStreamResp.Choices, choice)
			return chatStreamResp
		}
	
	case "response.output_item.added":
		// 输出项添加事件，可能包含初始角色等信息
		if responsesStreamResp.Item != nil && responsesStreamResp.Item.Role == "assistant" {
			role := "assistant"
			content := "" // 初始内容为空
			choice := dto.ChatCompletionsStreamResponseChoice{
				Index: 0,
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Role:    role,
					Content: &content,
				},
			}
			chatStreamResp.Choices = append(chatStreamResp.Choices, choice)
			return chatStreamResp
		}

	case "response.done":
		// 响应完成事件，包含最终的使用量和状态
		if responsesStreamResp.Response != nil {
			finishReason := extractFinishReason(responsesStreamResp.Response.Status)
			choice := dto.ChatCompletionsStreamResponseChoice{
				Index:        0,
				FinishReason: &finishReason,
				Delta:        dto.ChatCompletionsStreamResponseChoiceDelta{}, // 空Delta
			}
			chatStreamResp.Choices = append(chatStreamResp.Choices, choice)
			
			// 如果有使用量信息，也包含进去
			if responsesStreamResp.Response.Usage != nil {
				chatStreamResp.Usage = responsesStreamResp.Response.Usage
			}
			
			return chatStreamResp
		}
		
	// 其他事件类型如 response.created, response.text.delta (如果与content_part.delta不同) 等可以根据需要添加
	// 目前忽略其他类型的事件
	default:
		return nil
	}

	return nil
}