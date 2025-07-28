package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/droptableifexists/recon/sql-proxy/store"
)

type QueriesExecutedAPI struct {
	queryStore  *store.QueryStore
	schemaStore *store.SchemaStore
}

func MakeQueriesExecutedAPI(qs *store.QueryStore, ss *store.SchemaStore) *QueriesExecutedAPI {
	return &QueriesExecutedAPI{
		queryStore:  qs,
		schemaStore: ss,
	}
}

func (api QueriesExecutedAPI) RunApi() {
	http.HandleFunc("/queries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Use a custom encoder that doesn't escape HTML characters
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)

		if err := encoder.Encode(api.queryStore.ListQueries()); err != nil {
			fmt.Printf("Error encoding JSON: %v\n", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/schema_dump", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Use a custom encoder that doesn't escape HTML characters
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)

		api.schemaStore.StartSchemaDump()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Schema dump started"))
	})

	http.HandleFunc("/schema", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Use a custom encoder that doesn't escape HTML characters
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)

		finished, schema, errors := api.schemaStore.ListFullSchema()
		if len(errors) > 0 {
			// Convert errors to strings for JSON serialization
			errorMessages := make([]string, len(errors))
			for i, err := range errors {
				errorMessages[i] = err.Error()
			}

			w.WriteHeader(http.StatusInternalServerError)
			encoder.Encode(map[string]interface{}{
				"status":  "error",
				"message": "Failed to fetch schema",
				"errors":  errorMessages,
			})
			return
		}

		if !finished {
			w.Header().Set("Retry-After", "10")
			http.Error(w, "Schema dump not finished, retry in 10 seconds", http.StatusServiceUnavailable)
			return
		}

		if err := encoder.Encode(schema); err != nil {
			fmt.Printf("Error encoding JSON: %v\n", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := map[string]interface{}{
			"status":    "healthy",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"service":   "sql-proxy-api",
		}

		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)

		if err := encoder.Encode(response); err != nil {
			fmt.Printf("Error encoding health check JSON: %v\n", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
	})

	// Start the server on port 8080
	apiPort := os.Getenv("API_PORT")
	fmt.Println("Starting API on port", apiPort)
	http.ListenAndServe(":"+apiPort, nil)
}
