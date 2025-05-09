package main

import (
	"database/sql"
	"fmt"
	"os"
)

type QueryWithPlan struct {
	Query string
	Plan  string
}

func AddQueryPlansForChanges(connStr string, queries []Query) []QueryWithPlan {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return nil
	}
	defer db.Close()

	queryWithPlans := []QueryWithPlan{}
	for _, query := range queries {
		// Get the query plan using EXPLAIN ANALYZE
		var plan string
		err := db.QueryRow("EXPLAIN ANALYZE " + query.Query).Scan(&plan)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get plan for query: %v\n", err)
			continue
		}
		fmt.Printf("Plan for query: %s\n%s\n", query.Query, plan)
		queryWithPlans = append(queryWithPlans, QueryWithPlan{Query: query.Query, Plan: plan})
	}
	return queryWithPlans
}
