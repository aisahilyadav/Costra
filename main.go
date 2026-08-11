package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, relying on system environment variables")
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Costra is alive")
	})

	http.HandleFunc("/v1/chat/completions", proxyToGroq)

	fmt.Println("Costra server starting on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println("Server failed to start:", err)
	}
}

type streamPeek struct {
	Stream bool   `json:"stream"`
	Model  string `json:"model"`
}

// Usage matches the shape of the "usage" object Groq/OpenAI return.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// chatResponse is just enough of the non-streaming response shape
// for us to pull "usage" out of it.
type chatResponse struct {
	Usage Usage `json:"usage"`
}

// streamChunk is just enough of a single streaming chunk's shape
// to check if it happens to contain the final usage object.
type streamChunk struct {
	Usage *Usage `json:"usage"` // pointer, because most chunks WON'T have this field at all
}

// pricePerMillion holds cost in USD per 1,000,000 tokens, split by
// input (prompt) and output (completion) tokens, since providers
// price these differently. Values here are illustrative placeholders —
// swap in real current pricing from Groq's pricing page.
var pricePerMillion = map[string]struct {
	Input  float64
	Output float64
}{
	"llama-3.1-8b-instant": {Input: 0.05, Output: 0.08},
}

func calculateCost(model string, promptTokens, completionTokens int) float64 {
	price, ok := pricePerMillion[model]
	if !ok {
		return 0 // unknown model - no pricing entry yet
	}
	inputCost := (float64(promptTokens) / 1_000_000) * price.Input
	outputCost := (float64(completionTokens) / 1_000_000) * price.Output
	return inputCost + outputCost
}

func logUsage(model string, usage Usage, latency time.Duration, streaming bool) {
	cost := calculateCost(model, usage.PromptTokens, usage.CompletionTokens)
	fmt.Printf(
		"[USAGE] model=%s stream=%v prompt_tokens=%d completion_tokens=%d total_tokens=%d cost=$%.6f latency=%s\n",
		model, streaming, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, cost, latency,
	)
}

func proxyToGroq(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now() // mark when we started, to measure latency later

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var peek streamPeek
	json.Unmarshal(body, &peek)

	groqURL := "https://api.groq.com/openai/v1/chat/completions"
	req, err := http.NewRequest("POST", groqURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to build request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("GROQ_API_KEY"))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to reach Groq", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	if peek.Stream {
		streamAndCapture(w, resp.Body, peek.Model, startTime)
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read Groq response", http.StatusInternalServerError)
			return
		}
		w.Write(respBody)

		var parsed chatResponse
		json.Unmarshal(respBody, &parsed)
		latency := time.Since(startTime)
		logUsage(peek.Model, parsed.Usage, latency, false)
	}
}

// streamAndCapture forwards chunks to the client as they arrive (same as
// Phase 2), but ALSO inspects each chunk to catch the final one that
// contains usage data, so we can log it once the stream ends.
func streamAndCapture(w http.ResponseWriter, body io.Reader, model string, startTime time.Time) {
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

			// Each SSE line looks like: "data: {...}\n"
			// We strip the "data: " prefix and try to parse the JSON,
			// checking if THIS particular chunk has a usage field.
			trimmed := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if trimmed != "" && trimmed != "[DONE]" {
				var chunk streamChunk
				if jsonErr := json.Unmarshal([]byte(trimmed), &chunk); jsonErr == nil {
					if chunk.Usage != nil {
						capturedUsage = *chunk.Usage
					}
				}
			}
		}
		if err != nil {
			break // stream ended
		}
	}

	latency := time.Since(startTime)
	logUsage(model, capturedUsage, latency, true)
}
