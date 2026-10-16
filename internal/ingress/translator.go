package ingress

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/jzPPP3333/allproxyapi/internal/ingress/anthropic"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
)

// OpenAIToGemini converts an OpenAI ChatCompletionRequest into a Gemini generateContent request map.
func OpenAIToGemini(req *openai.ChatCompletionRequest) (map[string]interface{}, error) {
	geminiReq := make(map[string]interface{})

	var contents []map[string]interface{}
	var systemInstruction map[string]interface{}

	for _, msg := range req.Messages {
		contentStr := ""
		switch v := msg.Content.(type) {
		case string:
			contentStr = v
		case []interface{}:
			var sb strings.Builder
			for _, part := range v {
				if partMap, ok := part.(map[string]interface{}); ok {
					if t, ok := partMap["text"].(string); ok {
						sb.WriteString(t)
					}
				}
			}
			contentStr = sb.String()
		}

		if msg.Role == "system" {
			systemInstruction = map[string]interface{}{
				"role": "user",
				"parts": []map[string]interface{}{
					{"text": contentStr},
				},
			}
			continue
		}

		geminiRole := "user"
		if msg.Role == "assistant" {
			geminiRole = "model"
		}

		var parts []map[string]interface{}
		if contentStr != "" {
			parts = append(parts, map[string]interface{}{"text": contentStr})
		}

		// Tool calls from assistant
		for _, tc := range msg.ToolCalls {
			var args map[string]interface{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			parts = append(parts, map[string]interface{}{
				"functionCall": map[string]interface{}{
					"name": tc.Function.Name,
					"args": args,
				},
			})
		}

		// Tool response from client
		if msg.Role == "tool" {
			geminiRole = "user"
			var parsedResp interface{}
			if err := json.Unmarshal([]byte(contentStr), &parsedResp); err != nil {
				parsedResp = contentStr
			}
			parts = append(parts, map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name": msg.Name,
					"response": map[string]interface{}{
						"result": parsedResp,
					},
				},
			})
		}

		if len(parts) > 0 {
			contents = append(contents, map[string]interface{}{
				"role":  geminiRole,
				"parts": parts,
			})
		}
	}

	geminiReq["contents"] = contents
	if systemInstruction != nil {
		geminiReq["systemInstruction"] = systemInstruction
	}

	genConfig := make(map[string]interface{})
	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genConfig["topP"] = *req.TopP
	}
	if req.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *req.MaxTokens
	}
	if len(genConfig) > 0 {
		geminiReq["generationConfig"] = genConfig
	}

	// Tools
	if len(req.Tools) > 0 {
		var decls []map[string]interface{}
		for _, t := range req.Tools {
			decl := map[string]interface{}{
				"name":        t.Function.Name,
				"description": t.Function.Description,
			}
			var params map[string]interface{}
			if len(t.Function.Parameters) > 0 {
				_ = json.Unmarshal(t.Function.Parameters, &params)
			}
			decl["parameters"] = SanitizeToolParameters(params)
			decls = append(decls, decl)
		}
		geminiReq["tools"] = []map[string]interface{}{
			{"functionDeclarations": decls},
		}
	}

	return geminiReq, nil
}

// AnthropicToOpenAI converts an Anthropic MessagesRequest into an OpenAI ChatCompletionRequest.
func AnthropicToOpenAI(req *anthropic.MessagesRequest) (*openai.ChatCompletionRequest, error) {
	openAIReq := &openai.ChatCompletionRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if req.MaxTokens > 0 {
		openAIReq.MaxTokens = &req.MaxTokens
	}

	// System prompt
	if req.System != nil {
		sysText := ""
		switch s := req.System.(type) {
		case string:
			sysText = s
		case []interface{}:
			var sb strings.Builder
			for _, item := range s {
				if m, ok := item.(map[string]interface{}); ok {
					if t, ok := m["text"].(string); ok {
						sb.WriteString(t)
						sb.WriteString("\n")
					}
				}
			}
			sysText = strings.TrimSpace(sb.String())
		}
		if sysText != "" {
			openAIReq.Messages = append(openAIReq.Messages, openai.ChatMessage{
				Role:    "system",
				Content: sysText,
			})
		}
	}

	// Messages
	for _, m := range req.Messages {
		switch content := m.Content.(type) {
		case string:
			openAIReq.Messages = append(openAIReq.Messages, openai.ChatMessage{
				Role:    m.Role,
				Content: content,
			})
		case []interface{}:
			var textBuf strings.Builder
			var toolCalls []openai.ToolCall

			for _, blk := range content {
				blkMap, ok := blk.(map[string]interface{})
				if !ok {
					continue
				}
				blkType, _ := blkMap["type"].(string)

				switch blkType {
				case "text":
					if t, ok := blkMap["text"].(string); ok {
						textBuf.WriteString(t)
					}
				case "thinking":
					// Preserve thinking if needed
					if th, ok := blkMap["thinking"].(string); ok {
						openAIReq.Messages = append(openAIReq.Messages, openai.ChatMessage{
							Role:             m.Role,
							ReasoningContent: th,
						})
					}
				case "tool_use":
					tcID, _ := blkMap["id"].(string)
					tcName, _ := blkMap["name"].(string)
					argsBytes, _ := json.Marshal(blkMap["input"])
					toolCalls = append(toolCalls, openai.ToolCall{
						ID:   tcID,
						Type: "function",
						Function: openai.FunctionCall{
							Name:      tcName,
							Arguments: string(argsBytes),
						},
					})
				case "tool_result":
					trID, _ := blkMap["tool_use_id"].(string)
					trContent := blkMap["content"]
					var trText string
					switch c := trContent.(type) {
					case string:
						trText = c
					default:
						b, _ := json.Marshal(c)
						trText = string(b)
					}
					openAIReq.Messages = append(openAIReq.Messages, openai.ChatMessage{
						Role:       "tool",
						ToolCallID: trID,
						Content:    trText,
					})
				}
			}

			if textBuf.Len() > 0 || len(toolCalls) > 0 {
				msg := openai.ChatMessage{
					Role:      m.Role,
					Content:   textBuf.String(),
					ToolCalls: toolCalls,
				}
				openAIReq.Messages = append(openAIReq.Messages, msg)
			}
		}
	}

	// Tools
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			openAIReq.Tools = append(openAIReq.Tools, openai.Tool{
				Type: "function",
				Function: openai.FunctionDefinition{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}

	return openAIReq, nil
}

// StreamGeminiToOpenAI reads unwrapped Gemini SSE from src and emits OpenAI chat.completion.chunk SSE to dst.
func StreamGeminiToOpenAI(src io.Reader, dst http.ResponseWriter, model string) error {
	flusher, _ := dst.(http.Flusher)
	dst.Header().Set("Content-Type", "text/event-stream")
	dst.Header().Set("Cache-Control", "no-cache")
	dst.Header().Set("Connection", "keep-alive")

	scanner := bufio.NewScanner(src)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	chatcmplID := fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano())

	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}

		payload := bytes.TrimSpace(trimmed[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		var geminiResp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text         string `json:"text"`
						Thought      bool   `json:"thought"`
						FunctionCall *struct {
							Name string                 `json:"name"`
							Args map[string]interface{} `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				TotalTokenCount      int `json:"totalTokenCount"`
			} `json:"usageMetadata"`
		}

		if err := json.Unmarshal(payload, &geminiResp); err != nil {
			continue
		}

		if len(geminiResp.Candidates) == 0 {
			continue
		}

		cand := geminiResp.Candidates[0]
		var textDelta, thoughtDelta string
		var toolCalls []openai.ToolCall

		for i, part := range cand.Content.Parts {
			if part.Thought {
				thoughtDelta += part.Text
			} else if part.Text != "" {
				textDelta += part.Text
			}
			if part.FunctionCall != nil {
				argsBytes, _ := json.Marshal(part.FunctionCall.Args)
				toolCalls = append(toolCalls, openai.ToolCall{
					Index: i,
					ID:    fmt.Sprintf("call_%d", i),
					Type:  "function",
					Function: openai.FunctionCall{
						Name:      part.FunctionCall.Name,
						Arguments: string(argsBytes),
					},
				})
			}
		}

		finishReason := cand.FinishReason
		var oaiFinish interface{}
		if finishReason != "" {
			if finishReason == "STOP" {
				oaiFinish = "stop"
			} else {
				oaiFinish = strings.ToLower(finishReason)
			}
		}

		chunk := openai.ChatCompletionChunk{
			ID:      chatcmplID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []openai.ChunkChoice{
				{
					Index: 0,
					Delta: openai.ChunkDelta{
						Content:          textDelta,
						ReasoningContent: thoughtDelta,
						ToolCalls:        toolCalls,
					},
					FinishReason: oaiFinish,
				},
			},
		}

		if geminiResp.UsageMetadata != nil {
			chunk.Usage = &openai.Usage{
				PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
				CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
			}
		}

		chunkBytes, _ := json.Marshal(chunk)
		fmt.Fprintf(dst, "data: %s\n\n", string(chunkBytes))
		if flusher != nil {
			flusher.Flush()
		}
	}

	fmt.Fprintf(dst, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return scanner.Err()
}

// StreamOpenAIToAnthropic reads OpenAI SSE stream from src and emits Anthropic Messages SSE to dst.
func StreamOpenAIToAnthropic(src io.Reader, dst http.ResponseWriter, model string) error {
	flusher, _ := dst.(http.Flusher)
	dst.Header().Set("Content-Type", "text/event-stream")
	dst.Header().Set("Cache-Control", "no-cache")
	dst.Header().Set("Connection", "keep-alive")

	msgID := fmt.Sprintf("msg_%x", time.Now().UnixNano())

	// 1. Emit message_start
	msgStart := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}
	b, _ := json.Marshal(msgStart)
	fmt.Fprintf(dst, "event: message_start\ndata: %s\n\n", string(b))
	if flusher != nil {
		flusher.Flush()
	}

	scanner := bufio.NewScanner(src)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	outputTokens := 0
	currBlockIndex := 0
	textBlockOpen := false
	hasToolCalls := false

	toolBlockMap := make(map[int]int)

	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}

		payload := bytes.TrimSpace(trimmed[5:])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}

		var chunk openai.ChatCompletionChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		// Thinking delta
		if delta.ReasoningContent != "" {
			cDelta := map[string]interface{}{
				"type":  "content_block_delta",
				"index": currBlockIndex,
				"delta": map[string]interface{}{
					"type":     "thinking_delta",
					"thinking": delta.ReasoningContent,
				},
			}
			b, _ = json.Marshal(cDelta)
			fmt.Fprintf(dst, "event: content_block_delta\ndata: %s\n\n", string(b))
			if flusher != nil {
				flusher.Flush()
			}
		}

		// Text content delta
		if delta.Content != "" {
			if !textBlockOpen {
				cStart := map[string]interface{}{
					"type":  "content_block_start",
					"index": currBlockIndex,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				}
				b, _ = json.Marshal(cStart)
				fmt.Fprintf(dst, "event: content_block_start\ndata: %s\n\n", string(b))
				textBlockOpen = true
			}

			outputTokens++
			cDelta := map[string]interface{}{
				"type":  "content_block_delta",
				"index": currBlockIndex,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": delta.Content,
				},
			}
			b, _ = json.Marshal(cDelta)
			fmt.Fprintf(dst, "event: content_block_delta\ndata: %s\n\n", string(b))
			if flusher != nil {
				flusher.Flush()
			}
		}

		// Tool calls delta
		if len(delta.ToolCalls) > 0 {
			hasToolCalls = true
			if textBlockOpen {
				cStop := map[string]interface{}{
					"type":  "content_block_stop",
					"index": currBlockIndex,
				}
				b, _ = json.Marshal(cStop)
				fmt.Fprintf(dst, "event: content_block_stop\ndata: %s\n\n", string(b))
				textBlockOpen = false
				currBlockIndex++
			}

			for _, tc := range delta.ToolCalls {
				tcIdx := tc.Index
				blkIdx, exists := toolBlockMap[tcIdx]
				if !exists {
					blkIdx = currBlockIndex
					toolBlockMap[tcIdx] = blkIdx
					currBlockIndex++

					tcID := tc.ID
					if tcID == "" {
						tcID = fmt.Sprintf("call_%d_%s", tcIdx, RandID())
					}

					cStart := map[string]interface{}{
						"type":  "content_block_start",
						"index": blkIdx,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    tcID,
							"name":  tc.Function.Name,
							"input": map[string]interface{}{},
						},
					}
					b, _ = json.Marshal(cStart)
					fmt.Fprintf(dst, "event: content_block_start\ndata: %s\n\n", string(b))
				}

				if tc.Function.Arguments != "" {
					cDelta := map[string]interface{}{
						"type":  "content_block_delta",
						"index": blkIdx,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": tc.Function.Arguments,
						},
					}
					b, _ = json.Marshal(cDelta)
					fmt.Fprintf(dst, "event: content_block_delta\ndata: %s\n\n", string(b))
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
	}

	// Close text block if still open
	if textBlockOpen {
		cStop := map[string]interface{}{
			"type":  "content_block_stop",
			"index": currBlockIndex,
		}
		b, _ = json.Marshal(cStop)
		fmt.Fprintf(dst, "event: content_block_stop\ndata: %s\n\n", string(b))
	}

	// Close all tool blocks
	for _, blkIdx := range toolBlockMap {
		cStop := map[string]interface{}{
			"type":  "content_block_stop",
			"index": blkIdx,
		}
		b, _ = json.Marshal(cStop)
		fmt.Fprintf(dst, "event: content_block_stop\ndata: %s\n\n", string(b))
	}

	// Emit message_delta
	stopReason := "end_turn"
	if hasToolCalls {
		stopReason = "tool_use"
	}

	mDelta := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": outputTokens,
		},
	}
	b, _ = json.Marshal(mDelta)
	fmt.Fprintf(dst, "event: message_delta\ndata: %s\n\n", string(b))

	// Emit message_stop
	fmt.Fprintf(dst, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	return scanner.Err()
}

func RandID() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000000))
	return fmt.Sprintf("%08x", n.Int64())
}
