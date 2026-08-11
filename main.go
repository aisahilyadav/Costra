package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq" // Postgres driver - imported for its side effect of registering itself with database/sql
)

// db is a package-level variable holding our database connection pool,
// so any function in this file can use it without passing it around manually.
var db *sql.DB

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, relying on system environment variables")
	}

	// Open a connection pool to Postgres. This doesn't actually connect yet -
	// connections are made lazily as needed.
	var err error
	db, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Println("Failed to open database connection:", err)
		os.Exit(1)
	}

	// Ping actually tests the connection right now, so we fail fast
	// with a clear error if Postgres isn't reachable, instead of failing
	// silently later on the first real request.
	if err := db.Ping(); err != nil {
		fmt.Println("Failed to connect to database:", err)
		os.Exit(1)
	}
	fmt.Println("Connected to Postgres successfully")

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Costra is alive")
	})

	http.HandleFunc("/v1/chat/completions", proxyToGroq)

	fmt.Println("Costra server starting on :8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println("Server failed to start:", err)
	}
}

type streamPeek struct {
	Stream bool   `json:"stream"`
	Model  string `json:"model"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatResponse struct {
	Usage Usage `json:"usage"`
}

type streamChunk struct {
	Usage *Usage `json:"usage"`
}

var pricePerMillion = map[string]struct {
	Input  float64
	Output float64
}{
	"llama-3.1-8b-instant": {Input: 0.05, Output: 0.08},
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

// attribution groups the optional "who/what caused this request" metadata
// together, so we don't have to pass four separate string parameters around.
type attribution struct {
	SessionID string
	AgentID   string
	TaskID    string
	UserID    string
}

// logUsage now inserts a row into Postgres instead of just printing.
// We still print too, for now, since it's a helpful sanity check while learning.
func logUsage(model string, usage Usage, latency time.Duration, streaming bool, status string, attr attribution) {
	cost := calculateCost(model, usage.PromptTokens, usage.CompletionTokens)
	latencyMs := int(latency.Milliseconds())

	fmt.Printf(
		"[USAGE] model=%s stream=%v prompt_tokens=%d completion_tokens=%d total_tokens=%d cost=$%.6f latency=%s session=%s agent=%s\n",
		model, streaming, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, cost, latency,
		attr.SessionID, attr.AgentID,
	)

	// $1, $2, $3... are placeholders - Postgres fills them in safely,
	// which protects us from SQL injection compared to building the query
	// string by hand.
	// Empty strings ("") get stored as empty strings, not NULL - that's fine
	// for our purposes here, and keeps the insert logic simple.
	_, err := db.Exec(
		`INSERT INTO requests
			(provider, model, prompt_tokens, completion_tokens, total_tokens, cost, latency_ms, streaming, status,
			 session_id, agent_id, task_id, user_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		"groq", model, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		cost, latencyMs, streaming, status,
		attr.SessionID, attr.AgentID, attr.TaskID, attr.UserID,
	)
	if err != nil {
		fmt.Println("Failed to write usage log to database:", err)
	}
}

func proxyToGroq(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	// Read optional attribution headers. r.Header.Get returns "" if the
	// header wasn't sent at all, which is exactly the behavior we want -
	// these are all optional.
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

	status := "success"
	if resp.StatusCode >= 400 {
		status = "error"
	}

	if peek.Stream {
		streamAndCapture(w, resp.Body, peek.Model, startTime, status, attr)
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
		logUsage(peek.Model, parsed.Usage, latency, false, status, attr)
	}
}

func streamAndCapture(w http.ResponseWriter, body io.Reader, model string, startTime time.Time, status string, attr attribution) {
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
			break
		}
	}

	latency := time.Since(startTime)
	logUsage(model, capturedUsage, latency, true, status, attr)
}
