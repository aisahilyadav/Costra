package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// GeminiProvider implements the Provider interface for Google's Gemini API,
// which uses a genuinely different request/response shape than OpenAI/Groq.
// This is exactly the case the Provider interface exists to handle.
type GeminiProvider struct{}

func (g GeminiProvider) Name() string {
	return "gemini"
}

// Gemini expects: {"contents": [{"role": "user", "parts": [{"text": "..."}]}]}
// - very different from OpenAI's {"messages": [{"role": "user", "content": "..."}]}
type geminiPart struct {
	Text string `json:"text"`
}
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}
type geminiRequestBody struct {
	Contents []geminiContent `json:"contents"`
}

func (g GeminiProvider) BuildRequest(chatReq ChatRequest, apiKey string) (*http.Request, error) {
	// Translate our generic Messages into Gemini's Contents shape.
	var contents []geminiContent
	for _, m := range chatReq.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model" // Gemini calls the AI's turn "model", not "assistant"
		}
		contents = append(contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
		})
	}

	bodyBytes, err := json.Marshal(geminiRequestBody{Contents: contents})
	if err != nil {
		return nil, err
	}

	method := "generateContent"
	streamParam := ""
	if chatReq.Stream {
		method = "streamGenerateContent"
		// alt=sse asks Gemini to use the same "data: {...}" SSE format we
		// already know how to stream/parse, instead of its other formats.
		streamParam = "?alt=sse"
	}

	url := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:%s%s",
		chatReq.Model, method, streamParam,
	)

	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey) // Gemini's auth header - completely different from Groq's "Authorization: Bearer"
	return req, nil
}

// geminiUsageMetadata matches Gemini's field names, which are entirely
// different from Groq/OpenAI's (promptTokenCount vs prompt_tokens, etc).
type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type geminiResponse struct {
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
}

func (g GeminiProvider) ExtractUsage(responseBody []byte) Usage {
	var parsed geminiResponse
	json.Unmarshal(responseBody, &parsed)
	if parsed.UsageMetadata == nil {
		return Usage{}
	}
	return Usage{
		PromptTokens:     parsed.UsageMetadata.PromptTokenCount,
		CompletionTokens: parsed.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      parsed.UsageMetadata.TotalTokenCount,
	}
}

func (g GeminiProvider) ExtractUsageFromStreamChunk(line string) *Usage {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if trimmed == "" {
		return nil
	}
	var chunk geminiResponse
	if err := json.Unmarshal([]byte(trimmed), &chunk); err != nil || chunk.UsageMetadata == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
		CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
	}
}
