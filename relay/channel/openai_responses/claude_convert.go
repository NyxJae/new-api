package openai_responses

import (
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// ClaudeMessagesToResponsesRequest 将 Claude Messages API 请求转换为 Responses API 格式
// 参数:
//   - c: Gin 上下文
//   - claudeRequest: Claude Messages API 请求对象
//   - info: 转发信息，包含模型映射等信息
// 返回:
//   - *dto.OpenAIResponsesRequest: 转换后的 Responses API 请求对象
//   - error: 转换失败时返回错误
func ClaudeMessagesToResponsesRequest(c *gin.Context, claudeRequest *dto.ClaudeRequest, info *relaycommon.RelayInfo) (*dto.OpenAIResponsesRequest, error) {
	if claudeRequest == nil {
		return nil, fmt.Errorf("claude request is nil")
	}
	if claudeRequest.Model == "" {
		return nil, fmt.Errorf("model is required")
	}

	// 创建 Responses 请求对象
	responsesReq := &dto.OpenAIResponsesRequest{
		Model:  info.UpstreamModelName,
		Stream: claudeRequest.Stream,
		TopP:   claudeRequest.TopP,
	}

	// 处理 temperature 参数
	if claudeRequest.Temperature != nil {
		responsesReq.Temperature = *claudeRequest.Temperature
	}

	// 映射 max_tokens 到 max_output_tokens
	if claudeRequest.MaxTokens > 0 {
		responsesReq.MaxOutputTokens = claudeRequest.MaxTokens
	} else if claudeRequest.MaxTokensToSample > 0 {
		responsesReq.MaxOutputTokens = claudeRequest.MaxTokensToSample
	}

	// 处理 Claude 特有的参数
	if claudeRequest.TopK > 0 {
		// Responses API 不直接支持 top_k，但可以通过其他方式处理
		// 这里暂时忽略，或者可以记录日志
	}

	// 提取系统消息并设置为 instructions
	if claudeRequest.System != nil {
		instructions, err := extractClaudeSystemMessage(claudeRequest.System)
		if err != nil {
			return nil, fmt.Errorf("failed to extract system message: %w", err)
		}
		if instructions != "" {
			// 将 instructions 序列化为 JSON RawMessage
			instructionsBytes, err := json.Marshal(instructions)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal instructions: %w", err)
			}
			responsesReq.Instructions = json.RawMessage(instructionsBytes)
		}
	}

	// 转换 messages 为 input 格式
	inputs, err := convertClaudeMessagesToInputs(claudeRequest.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert claude messages to inputs: %w", err)
	}

	// 将 inputs 序列化为 JSON RawMessage
	if len(inputs) > 0 {
		inputData, err := json.Marshal(inputs)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal inputs: %w", err)
		}
		responsesReq.Input = json.RawMessage(inputData)
	}

	// 处理 tools 参数
	if claudeRequest.Tools != nil {
		toolsData, err := json.Marshal(claudeRequest.Tools)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tools: %w", err)
		}
		responsesReq.Tools = json.RawMessage(toolsData)
	}

	// 处理 tool_choice 参数
	if claudeRequest.ToolChoice != nil {
		toolChoiceData, err := json.Marshal(claudeRequest.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool_choice: %w", err)
		}
		responsesReq.ToolChoice = json.RawMessage(toolChoiceData)
	}

	// 处理 stop_sequences 参数
	if len(claudeRequest.StopSequences) > 0 {
		// Responses API 可能使用不同的 stop 参数格式
		// 这里可以转换为适当的格式或忽略
	}

	// 处理其他参数
	if claudeRequest.Metadata != nil {
		responsesReq.Metadata = claudeRequest.Metadata
	}

	return responsesReq, nil
}

// extractClaudeSystemMessage 从 Claude 的 system 字段提取系统消息
// Claude 的 system 字段可能是字符串或复杂结构
// 参数:
//   - system: Claude 请求的 system 字段
// 返回:
//   - string: 提取的系统消息内容
//   - error: 提取失败时返回错误
func extractClaudeSystemMessage(system any) (string, error) {
	if system == nil {
		return "", nil
	}

	// 如果是字符串，直接返回
	if str, ok := system.(string); ok {
		// 检查字符串是否包含无效的UTF-8字符
		if !isValidUTF8String(str) {
			str = cleanInvalidUTF8Chars(str)
		}
		return str, nil
	}

	// 如果是复杂类型，尝试转换为字符串
	systemBytes, err := json.Marshal(system)
	if err != nil {
		return "", fmt.Errorf("failed to marshal system message: %w", err)
	}

	// 验证生成的JSON是否有效
	if !isValidUTF8Bytes(systemBytes) {
		systemBytes = cleanInvalidUTF8Bytes(systemBytes)
	}

	return string(systemBytes), nil
}

// convertClaudeMessagesToInputs 将 Claude Messages API 的 messages 转换为 Responses API 的 inputs 格式
// 参数:
//   - messages: Claude Messages API 的消息列表
// 返回:
//   - []dto.Input: 转换后的 Input 数组
//   - error: 转换失败时返回错误
func convertClaudeMessagesToInputs(messages []dto.ClaudeMessage) ([]dto.Input, error) {
	var inputs []dto.Input

	for _, message := range messages {
		input := dto.Input{
			Type: "message",
			Role: message.Role,
		}

		// 处理 content 字段
		if message.Content != nil {
			// 验证 content 是否包含无效字符
			var contentBytes []byte
			var err error

			// 如果 content 是字符串，验证编码并使用
			if str, ok := message.Content.(string); ok {
				// 检查字符串是否包含无效的UTF-8字符
				if !isValidUTF8String(str) {
					str = cleanInvalidUTF8Chars(str)
				}
				contentBytes, err = json.Marshal(str)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal string content: %w", err)
				}
			} else {
				// 如果 content 是复杂类型，需要转换 Claude 的 content type 到 Responses 格式
				convertedContent, err := convertClaudeContentToResponses(message.Content)
				if err != nil {
					return nil, fmt.Errorf("failed to convert claude content to responses format: %w", err)
				}
				contentBytes, err = json.Marshal(convertedContent)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal converted content: %w", err)
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

// convertClaudeContentToResponses 将 Claude 的 content 转换为 Responses API 格式
func convertClaudeContentToResponses(content any) (any, error) {
	// 如果是数组，遍历处理每个元素
	if contentArray, ok := content.([]interface{}); ok {
		var newContentArray []map[string]interface{}
		for _, item := range contentArray {
			if itemMap, ok := item.(map[string]interface{}); ok {
				newItem := make(map[string]interface{})
				// 复制所有字段
				for k, v := range itemMap {
					newItem[k] = v
				}
				
				// 转换 type 字段
				if typeVal, ok := newItem["type"].(string); ok {
					switch typeVal {
					case "text":
						newItem["type"] = "input_text"
					case "image":
						newItem["type"] = "input_image"
					// 可以在这里添加其他类型的映射
					}
				}
				newContentArray = append(newContentArray, newItem)
			} else {
				// 如果不是 map，保持原样（虽然 Claude API 中 content 数组元素通常是对象）
				return content, nil
			}
		}
		return newContentArray, nil
	}
	
	// 如果不是数组，直接返回（可能是字符串或其他格式，虽然通常是数组）
	return content, nil
}