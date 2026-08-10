package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Costra is alive")
	})

	fmt.Println("Costra server starting on :8080")
	http.ListenAndServe(":8080", nil)
}