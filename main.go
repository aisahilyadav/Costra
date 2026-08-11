package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

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

// streamPeek is just used to check the "stream" field without needing
// to fully understand the rest of the request body's structure.
type streamPeek struct {
	Stream bool `json:"stream"`
}

func proxyToGroq(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// Peek into the body just to check if this is a streaming request
	var peek streamPeek
	json.Unmarshal(body, &peek) // ignoring error here on purpose - if it fails, peek.Stream stays false

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
		streamResponse(w, resp.Body)
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			http.Error(w, "failed to read Groq response", http.StatusInternalServerError)
			return
		}
		w.Write(respBody)
	}
}

// streamResponse copies chunks from Groq's response to our client as they
// arrive, flushing after every write so the client sees tokens progressively
// instead of waiting for the whole response.
func streamResponse(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// If the underlying writer doesn't support flushing, just copy normally.
		io.Copy(w, body)
		return
	}

	buf := make([]byte, 1024) // 1KB buffer to read chunks into
	for {
		n, err := body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break // stream ended (io.EOF) or a real error - either way, stop
		}
	}
}
