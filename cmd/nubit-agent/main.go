package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nubitio/nubit-agent/internal/command"
)

func main() {
	address := os.Getenv("NUBIT_AGENT_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:9090"
	}
	stateDir := os.Getenv("NUBIT_AGENT_STATE_DIR")
	if stateDir == "" {
		stateDir = "/var/lib/nubit-agent"
	}
	if _, err := command.NewFileStore(filepath.Join(stateDir, "commands.json")); err != nil {
		log.Fatalf("initialize command store: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"status":"ok"}`)
	})

	server := &http.Server{
		Addr:    address,
		Handler: mux,
	}

	log.Printf("nubit-agent health endpoint listening on %s", address)
	log.Fatal(server.ListenAndServe())
}
