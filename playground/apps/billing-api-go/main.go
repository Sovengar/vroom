package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"service": "billing-api-go", "status": "ok"})
	})
	log.Println("billing-api-go escuchando en :8082")
	log.Fatal(http.ListenAndServe(":8082", nil))
}
