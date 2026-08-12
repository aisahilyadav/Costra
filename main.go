package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

var db *sql.DB

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, relying on system environment variables")
	}

	var err error
	db, err = sql.Open("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Println("Failed to open database connection:", err)
		os.Exit(1)
	}
	if err := db.Ping(); err != nil {
		fmt.Println("Failed to connect to database:", err)
		os.Exit(1)
	}
	fmt.Println("Connected to Postgres successfully")

	loadModelRoutes()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Costra is alive")
	})

	// {provider} is a URL path wildcard (Go 1.22+ routing feature) -
	// r.PathValue("provider") retrieves it inside proxyHandler.
	http.HandleFunc("/v1/chat/completions/{provider}", proxyHandler)
	http.HandleFunc("/admin/keys", adminRegisterKey)

	fmt.Println("Costra server starting on :8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println("Server failed to start:", err)
	}
}
