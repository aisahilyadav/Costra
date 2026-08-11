package main

// ChatRequest is the generic request shape Costra expects from clients,
// modeled after OpenAI's format since it's the most widely adopted shape.
// Regardless of which provider the request is actually going to, the
// CLIENT always sends this same shape - Costra translates it internally.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage is Costra's own canonical usage shape, used internally everywhere,
// regardless of provider. Each provider adapter converts its own response
// format into this shape - this is what lets the rest of the code (cost
// calculation, logging, DB inserts) stay identical for every provider.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// attribution groups the optional "who/what caused this request" metadata.
type attribution struct {
	SessionID string
	AgentID   string
	TaskID    string
	UserID    string
}
