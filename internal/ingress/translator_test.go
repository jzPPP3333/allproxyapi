package ingress

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jzPPP3333/allproxyapi/internal/egress/antigravity"
	"github.com/jzPPP3333/allproxyapi/internal/egress/workbuddy"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/anthropic"
	"github.com/jzPPP3333/allproxyapi/internal/ingress/openai"
)

func TestOpenAIToGemini(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model: "gemini-2.5-pro",
		Messages: []openai.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello world"},
		},
	}

	geminiReq, err := OpenAIToGemini(req)
	if err != nil {
		t.Fatalf("OpenAIToGemini error: %v", err)
	}

	sysInst, ok := geminiReq["systemInstruction"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected systemInstruction")
	}
	parts := sysInst["parts"].([]map[string]interface{})
	if parts[0]["text"] != "You are a helpful assistant." {
		t.Errorf("system text mismatch: %v", parts[0]["text"])
	}

	contents, ok := geminiReq["contents"].([]map[string]interface{})
	if !ok || len(contents) != 1 {
		t.Fatalf("expected 1 user content, got %v", contents)
	}
	if contents[0]["role"] != "user" {
		t.Errorf("expected role user, got %v", contents[0]["role"])
	}
}

func TestAnthropicToOpenAI(t *testing.T) {
	req := &anthropic.MessagesRequest{
		Model:     "claude-3-7-sonnet",
		System:    "Be concise",
		MaxTokens: 2048,
		Messages: []anthropic.Message{
			{Role: "user", Content: "Ping"},
		},
	}

	oaiReq, err := AnthropicToOpenAI(req)
	if err != nil {
		t.Fatalf("AnthropicToOpenAI error: %v", err)
	}

	if len(oaiReq.Messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(oaiReq.Messages))
	}
	if oaiReq.Messages[0].Role != "system" || oaiReq.Messages[0].Content != "Be concise" {
		t.Errorf("system message mismatch: %v", oaiReq.Messages[0])
	}
	if oaiReq.Messages[1].Role != "user" || oaiReq.Messages[1].Content != "Ping" {
		t.Errorf("user message mismatch: %v", oaiReq.Messages[1])
	}
}

func TestAntigravityUnwrap(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}}`)
	unwrapped, err := antigravity.UnwrapJSON(raw)
	if err != nil {
		t.Fatalf("UnwrapJSON error: %v", err)
	}
	if strings.Contains(string(unwrapped), `"response"`) {
		t.Errorf("expected response to be stripped, got %s", string(unwrapped))
	}
	if !strings.Contains(string(unwrapped), `"Hi"`) {
		t.Errorf("expected 'Hi' in unwrapped result, got %s", string(unwrapped))
	}
}

func TestWorkBuddyFingerprint(t *testing.T) {
	id1 := workbuddy.DeriveID("user123", "machine")
	id2 := workbuddy.DeriveID("user123", "machine")
	if id1 != id2 {
		t.Errorf("DeriveID should be deterministic, got %s != %s", id1, id2)
	}
	if len(id1) != 32 && len(id1) != 36 {
		t.Errorf("expected 32/36 hex chars, got %d", len(id1))
	}

	reqID := workbuddy.GenerateRequestID("user123")
	if !strings.Contains(reqID, "-") {
		t.Errorf("expected hyphen in reqID: %s", reqID)
	}
}

func TestUnwrapStreamPipe(t *testing.T) {
	src := strings.NewReader("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]}}]}}\n\n")
	var dst bytes.Buffer
	err := antigravity.UnwrapStreamPipe(src, &dst)
	if err != nil {
		t.Fatalf("UnwrapStreamPipe error: %v", err)
	}
	out := dst.String()
	if !strings.HasPrefix(out, "data: ") || strings.Contains(out, `"response":`) {
		t.Errorf("unexpected output: %s", out)
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("missing text in output: %s", out)
	}
}

func TestSanitizeToolParameters(t *testing.T) {
	raw := map[string]interface{}{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"title":                "TestTool",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"const":   "execute",
				"default": "execute",
				"title":   "Action Name",
			},
			"multi_type": map[string]interface{}{
				"type": []interface{}{"string", "null"},
			},
			"complex": map[string]interface{}{
				"oneOf": []interface{}{
					map[string]interface{}{"type": "string"},
					map[string]interface{}{"const": "special_val"},
				},
			},
		},
	}

	sanitized := SanitizeToolParameters(raw)

	// Check top level
	if _, ok := sanitized["$schema"]; ok {
		t.Errorf("expected $schema to be removed")
	}
	if _, ok := sanitized["title"]; ok {
		t.Errorf("expected title to be removed")
	}
	if _, ok := sanitized["additionalProperties"]; ok {
		t.Errorf("expected additionalProperties to be removed")
	}

	// Check action property
	props := sanitized["properties"].(map[string]interface{})
	action := props["action"].(map[string]interface{})
	if _, ok := action["const"]; ok {
		t.Errorf("expected const to be removed from action")
	}
	if _, ok := action["default"]; ok {
		t.Errorf("expected default to be removed from action")
	}
	if action["type"] != "string" {
		t.Errorf("expected type string for action, got %v", action["type"])
	}
	enumArr, ok := action["enum"].([]interface{})
	if !ok || len(enumArr) != 1 || enumArr[0] != "execute" {
		t.Errorf("expected enum ['execute'], got %v", enumArr)
	}

	// Check multi_type
	multi := props["multi_type"].(map[string]interface{})
	if multi["type"] != "string" {
		t.Errorf("expected type string for multi_type, got %v", multi["type"])
	}
	if multi["nullable"] != true {
		t.Errorf("expected nullable true for multi_type, got %v", multi["nullable"])
	}

	// Check complex oneOf
	complexProp := props["complex"].(map[string]interface{})
	oneOfList := complexProp["oneOf"].([]interface{})
	branch2 := oneOfList[1].(map[string]interface{})
	if _, ok := branch2["const"]; ok {
		t.Errorf("expected const to be removed from oneOf branch")
	}
	if branch2["type"] != "string" {
		t.Errorf("expected type string for oneOf branch, got %v", branch2["type"])
	}
	b2Enum := branch2["enum"].([]interface{})
	if len(b2Enum) != 1 || b2Enum[0] != "special_val" {
		t.Errorf("expected enum ['special_val'], got %v", b2Enum)
	}
}
