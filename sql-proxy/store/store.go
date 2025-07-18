package store

import "sync"

type QueryExecuted struct {
	Query string
}

type QueryStore struct {
	queryMap map[string]QueryExecuted
	mu       sync.RWMutex
}

func MakeQueryStore() *QueryStore {
	return &QueryStore{
		queryMap: make(map[string]QueryExecuted),
	}
}

func (qs *QueryStore) AddQuery(q QueryExecuted) {
	qs.mu.Lock()
	defer qs.mu.Unlock()
	qs.queryMap[q.Query] = q
}

func (qs *QueryStore) ListQueries() []QueryExecuted {
	qs.mu.RLock()
	defer qs.mu.RUnlock()

	queries := make([]QueryExecuted, 0, len(qs.queryMap))
	for _, query := range qs.queryMap {
		queries = append(queries, query)
	}
	return queries
}
