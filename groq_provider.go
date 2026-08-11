package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// GroqProvider implements the Provider interface for Groq's API, which
// closely mirrors OpenAI's request/response shape - so this adapter is
// intentionally simple, close to a passthrough.
type GroqProvider struct{}

func (g GroqProvider) Name() string {
	return "groq"
}

func (g GroqProvider) BuildRequest(chatReq ChatRequest, apiKey string) (*http.Request, error) {
	// Groq accepts (almost) exactly OpenAI's shape, so we can marshal our
	// generic ChatRequest straight back into JSON - no real translation needed.
	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return req, nil
}

// groqUsage matches Groq/OpenAI's response JSON field names exactly.
type groqUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type groqResponse struct {
	Usage groqUsage `json:"usage"`
}

func (g GroqProvider) ExtractUsage(responseBody []byte) Usage {
	var parsed groqResponse
	json.Unmarshal(responseBody, &parsed)
	return Usage{
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      parsed.Usage.TotalTokens,
	}
}

type groqStreamChunk struct {
	Usage *groqUsage `json:"usage"`
}

func (g GroqProvider) ExtractUsageFromStreamChunk(line string) *Usage {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if trimmed == "" || trimmed == "[DONE]" {
		return nil
	}
	var chunk groqStreamChunk
	if err := json.Unmarshal([]byte(trimmed), &chunk); err != nil || chunk.Usage == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     chunk.Usage.PromptTokens,
		CompletionTokens: chunk.Usage.CompletionTokens,
		TotalTokens:      chunk.Usage.TotalTokens,
	}
}
