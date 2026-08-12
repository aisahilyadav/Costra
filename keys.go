package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// storeProviderKey encrypts a plaintext API key and upserts it into the
// provider_keys table. "Upsert" means: insert if it doesn't exist yet,
// or update it if the provider already has a key stored.
func storeProviderKey(provider, plainKey string) error {
	encrypted, err := encrypt(plainKey)
	if err != nil {
		return err
	}

	_, err = db.Exec(
		`INSERT INTO provider_keys (provider, encrypted_key)
		 VALUES ($1, $2)
		 ON CONFLICT (provider) DO UPDATE SET encrypted_key = $2`,
		provider, encrypted,
	)
	return err
}

// getProviderKey retrieves and decrypts a provider's API key from the DB.
func getProviderKey(provider string) (string, error) {
	var encrypted string
	err := db.QueryRow(`SELECT encrypted_key FROM provider_keys WHERE provider = $1`, provider).Scan(&encrypted)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no API key stored for provider: %s", provider)
		}
		return "", err
	}
	return decrypt(encrypted)
}

// envKeyMapping maps a provider name to the .env variable holding its key.
// Add a line here whenever a new provider is added to Costra.
var envKeyMapping = map[string]string{
	"groq":   "GROQ_API_KEY",
	"gemini": "GEMINI_API_KEY",
}

// autoRegisterKeysFromEnv checks .env for provider keys on every startup,
// and if found, encrypts and saves them to the DB automatically - this is
// what lets a user go from "filled in .env" to "fully working" with zero
// manual API calls or dashboard clicks.
func autoRegisterKeysFromEnv() {
	for provider, envVar := range envKeyMapping {
		value := os.Getenv(envVar)
		if value == "" {
			continue // nothing set for this provider in .env - skip it
		}
		if err := storeProviderKey(provider, value); err != nil {
			fmt.Println("Failed to auto-register key for", provider, ":", err)
			continue
		}
		fmt.Println("Auto-registered API key for provider:", provider)
	}
}

type registerKeyRequest struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

// adminRegisterKey handles POST /admin/keys - lets you store a provider's
// real API key, encrypted, without ever touching the database by hand.
func adminRegisterKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req registerKeyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Provider == "" || req.APIKey == "" {
		http.Error(w, "provider and api_key are both required", http.StatusBadRequest)
		return
	}

	if err := storeProviderKey(req.Provider, req.APIKey); err != nil {
		http.Error(w, "failed to store key: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status": "ok", "provider": "%s"}`, req.Provider)
}
