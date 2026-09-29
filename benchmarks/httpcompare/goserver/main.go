package main

import (
	"log"
	"net/http"
	"os"
)

var body = []byte("Hello, world")

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	server := &http.Server{Addr: ":" + os.Args[1], Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})}
	log.Fatal(server.ListenAndServe())
}
