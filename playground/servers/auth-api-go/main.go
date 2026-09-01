package main

import (
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("auth-api-go ok\n"))
	})
	log.Println("auth-api-go escuchando en :8091")
	log.Fatal(http.ListenAndServe(":8091", nil))
}
