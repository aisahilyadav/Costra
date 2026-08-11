package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// modelRoutes holds the "requested model" -> "actual model to use" overrides,
// loaded once at startup from model_routes.json.
var modelRoutes = map[string]string{}

// loadModelRoutes reads model_routes.json from disk into the modelRoutes map.
// If the file doesn't exist, that's fine - it just means no overrides are
// configured, so we continue with an empty map instead of crashing.
func loadModelRoutes() {
	data, err := os.ReadFile("model_routes.json")
	if err != nil {
		fmt.Println("No model_routes.json found, running with no model overrides")
		return
	}
	if err := json.Unmarshal(data, &modelRoutes); err != nil {
		fmt.Println("Failed to parse model_routes.json:", err)
		return
	}
	fmt.Println("Loaded model routing overrides:", modelRoutes)
}

// providers holds one instance of each provider adapter, keyed by the name
// used in the URL, e.g. /v1/chat/completions/groq or /v1/chat/completions/gemini.
var providers = map[string]Provider{
	"groq":   GroqProvider{},
	"gemini": GeminiProvider{},
}

// apiKeyEnvVar maps a provider name to the env var holding its API key.
var apiKeyEnvVar = map[string]string{
	"groq":   "GROQ_API_KEY",
	"gemini": "GEMINI_API_KEY",
}

// pricePerMillion holds cost in USD per 1,000,000 tokens. Model names are
// unique enough across providers that a flat map works fine for now.
var pricePerMillion = map[string]struct {
	Input  float64
	Output float64
}{
	"llama-3.1-8b-instant": {Input: 0.05, Output: 0.08},
	"gemini-3.6-flash":     {Input: 0.075, Output: 0.30}, // placeholder pricing - confirm current rate on Google's pricing page
}

func calculateCost(model string, promptTokens, completionTokens int) float64 {
	price, ok := pricePerMillion[model]
	if !ok {
		return 0
	}
	inputCost := (float64(promptTokens) / 1_000_000) * price.Input
	outputCost := (float64(completionTokens) / 1_000_000) * price.Output
	return inputCost + outputCost
}

func logUsage(providerName, model string, usage Usage, latency time.Duration, streaming bool, status string, attr attribution) {
	cost := calculateCost(model, usage.PromptTokens, usage.CompletionTokens)
	latencyMs := int(latency.Milliseconds())

	fmt.Printf(
		"[USAGE] provider=%s model=%s stream=%v prompt_tokens=%d completion_tokens=%d total_tokens=%d cost=$%.6f latency=%s session=%s agent=%s\n",
		providerName, model, streaming, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, cost, latency,
		attr.SessionID, attr.AgentID,
	)

	_, err := db.Exec(
		`INSERT INTO requests
			(provider, model, prompt_tokens, completion_tokens, total_tokens, cost, latency_ms, streaming, status,
			 session_id, agent_id, task_id, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		providerName, model, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		cost, latencyMs, streaming, status,
		attr.SessionID, attr.AgentID, attr.TaskID, attr.UserID,
	)
	if err != nil {
		fmt.Println("Failed to write usage log to database:", err)
	}
}

// proxyHandler is completely generic across every provider - it never
// mentions "Groq" or "Gemini" by name. It looks up the right adapter based
// on the URL, then only ever talks to it through the Provider interface.
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	providerName := r.PathValue("provider") // pulls {provider} out of the URL path
	provider, ok := providers[providerName]
	if !ok {
		http.Error(w, "unknown provider: "+providerName, http.StatusBadRequest)
		return
	}

	attr := attribution{
		SessionID: r.Header.Get("X-Session-Id"),
		AgentID:   r.Header.Get("X-Agent-Id"),
		TaskID:    r.Header.Get("X-Task-Id"),
		UserID:    r.Header.Get("X-User-Id"),
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var chatReq ChatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Check if this model has a configured override - if so, swap it
	// before we ever build the provider request, so everything downstream
	// (billing, logging) reflects the model actually used.
	originalModel := chatReq.Model
	if override, exists := modelRoutes[chatReq.Model]; exists {
		fmt.Printf("[MODEL SWITCH] requested=%s -> actual=%s\n", originalModel, override)
		chatReq.Model = override
	}

	apiKey := os.Getenv(apiKeyEnvVar[providerName])

	providerReq, err := provider.BuildRequest(chatReq, apiKey)
	if err != nil {
		http.Error(w, "failed to build provider request", http.StatusInternalServerError)
		return
	}

	client := &http.Client{}
	resp, err := client.Do(providerReq)
	if err != nil {
		http.Error(w, "failed to reach provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	status := "success"
	if resp.StatusCode >= 400 {
		status = "error"
	}

	if chatReq.Stream {
		streamAndCapture(w, resp.Body, provider, chatReq.Model, startTime, status, attr)
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read provider response", http.StatusInternalServerError)
			return
		}
		w.Write(respBody)

		usage := provider.ExtractUsage(respBody)
		latency := time.Since(startTime)
		logUsage(provider.Name(), chatReq.Model, usage, latency, false, status, attr)
	}
}

func streamAndCapture(w http.ResponseWriter, body io.Reader, provider Provider, model string, startTime time.Time, status string, attr attribution) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		io.Copy(w, body)
		return
	}

	var capturedUsage Usage
	reader := bufio.NewReader(body)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			w.Write([]byte(line))
			flusher.Flush()

			if u := provider.ExtractUsageFromStreamChunk(line); u != nil {
				capturedUsage = *u
			}
		}
		if err != nil {
			break
		}
	}

	latency := time.Since(startTime)
	logUsage(provider.Name(), model, capturedUsage, latency, true, status, attr)
}
