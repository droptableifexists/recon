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
		if qe, err := json.Marshal(api.queryStore.ListQueries()); err == nil {
			w.Write(qe)
		} else {
			fmt.Print(err)
		}
	})

	// Start the server on port 8080
	apiPort := os.Getenv("API_PORT")
	fmt.Println("Starting API on port", apiPort)
	http.ListenAndServe(":"+apiPort, nil)
}
