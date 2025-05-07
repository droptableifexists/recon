package store

type QueryExecuted struct {
	Query string
}

type QueryStore struct {
	queryMap []QueryExecuted
}

func MakeQueryStore() *QueryStore {
	return &QueryStore{}
}

func (qs *QueryStore) AddQuery(q QueryExecuted) {
	qs.queryMap = append(qs.queryMap, q)
}

func (qs QueryStore) ListQueries() []QueryExecuted {
	return qs.queryMap
}
