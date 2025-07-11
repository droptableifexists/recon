package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/droptableifexists/recon/sql-proxy/store"
)

type QueriesExecutedAPI struct {
	queryStore *store.QueryStore
}

func MakeQueriesExecutedAPI(qs *store.QueryStore) *QueriesExecutedAPI {
	return &QueriesExecutedAPI{
		queryStore: qs,
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

	// Start the server on port 8080
	apiPort := os.Getenv("API_PORT")
	fmt.Println("Starting API on port", apiPort)
	http.ListenAndServe(":"+apiPort, nil)
}
