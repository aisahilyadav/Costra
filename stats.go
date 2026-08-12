package main

import (
	"encoding/json"
	"net/http"
)

// enableCORS allows the Next.js dashboard (running on a different port,
// localhost:3000) to call these endpoints from the browser. Browsers block
// cross-origin requests by default unless the server explicitly allows it.
func enableCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
}

type SummaryStats struct {
	TotalRequests int     `json:"total_requests"`
	TotalTokens   int     `json:"total_tokens"`
	TotalCost     float64 `json:"total_cost"`
}

// statsSummary handles GET /stats/summary - overall totals across every request.
func statsSummary(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	var s SummaryStats
	err := db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost), 0) FROM requests`,
	).Scan(&s.TotalRequests, &s.TotalTokens, &s.TotalCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(s)
}

type ProviderStat struct {
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	Requests int     `json:"requests"`
	Tokens   int     `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// statsByProvider handles GET /stats/by-provider - usage grouped by
// provider and model, most expensive first.
func statsByProvider(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	rows, err := db.Query(
		`SELECT provider, model, COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost), 0)
		 FROM requests GROUP BY provider, model ORDER BY 5 DESC`,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	results := []ProviderStat{} // start as an empty slice, not nil, so JSON encodes as [] not null
	for rows.Next() {
		var p ProviderStat
		if err := rows.Scan(&p.Provider, &p.Model, &p.Requests, &p.Tokens, &p.Cost); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		results = append(results, p)
	}
	json.NewEncoder(w).Encode(results)
}

type AttributionStat struct {
	SessionID string  `json:"session_id"`
	AgentID   string  `json:"agent_id"`
	Requests  int     `json:"requests"`
	Tokens    int     `json:"tokens"`
	Cost      float64 `json:"cost"`
}

// statsByAgent handles GET /stats/by-agent - usage grouped by session/agent,
// only including requests that actually had attribution tags set.
func statsByAgent(w http.ResponseWriter, r *http.Request) {
	enableCORS(w)
	rows, err := db.Query(
		`SELECT COALESCE(session_id, ''), COALESCE(agent_id, ''), COUNT(*), COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost), 0)
		 FROM requests
		 WHERE session_id != '' OR agent_id != ''
		 GROUP BY session_id, agent_id ORDER BY 5 DESC`,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	results := []AttributionStat{}
	for rows.Next() {
		var a AttributionStat
		if err := rows.Scan(&a.SessionID, &a.AgentID, &a.Requests, &a.Tokens, &a.Cost); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		results = append(results, a)
	}
	json.NewEncoder(w).Encode(results)
}
