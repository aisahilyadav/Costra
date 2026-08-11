package main

import "net/http"

// Provider is the contract every LLM provider adapter must satisfy.
// This is the core idea behind Costra being provider-agnostic: the main
// proxy logic (proxy.go) only ever talks to THIS interface - it never
// needs to know whether it's actually calling Groq, Gemini, or anything
// added later. Adding a new provider means writing a new small file that
// satisfies this interface, without touching proxy.go at all.
type Provider interface {
	// Name returns a short identifier, e.g. "groq" or "gemini", used for
	// logging and DB storage.
	Name() string

	// BuildRequest takes Costra's generic ChatRequest and turns it into a
	// real *http.Request ready to send to the provider - the correct URL,
	// headers, auth, and provider-specific JSON body all live here.
	BuildRequest(chatReq ChatRequest, apiKey string) (*http.Request, error)

	// ExtractUsage parses a COMPLETE (non-streaming) response body and
	// returns usage translated into Costra's canonical Usage shape.
	ExtractUsage(responseBody []byte) Usage

	// ExtractUsageFromStreamChunk inspects a single line from a streaming
	// response and returns usage if that particular chunk happens to
	// contain it, or nil if it doesn't (most chunks won't - only the
	// chunk(s) with real usage data will).
	ExtractUsageFromStreamChunk(line string) *Usage
}
