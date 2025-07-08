package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"

	_ "github.com/lib/pq"
)

func main() {
	// Mock SQL queries that will be passed through the proxy
	queries := []string{
		"SELECT 1 as one;",
		"SELECT 3 as three;",
		"SELECT 2 as two;", // Simple test query first
	}

	// Connection parameters matching your working psql connection
	connStr := "host=localhost port=5433 user=postgres password=postgres dbname=postgres sslmode=disable"
	fmt.Printf("Connecting with: %s\n", connStr)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Execute mock SQL queries through the proxy
	for _, query := range queries {
		fmt.Printf("Executing query: %s\n", query)
		rows, err := db.Query(query)
		if err != nil {
			log.Fatalf("Error executing query: %v", err)
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			log.Fatalf("Error getting columns: %v", err)
		}

		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		for rows.Next() {
			err := rows.Scan(valuePtrs...)
			if err != nil {
				log.Fatalf("Error scanning row: %v", err)
			}

			for i, col := range columns {
				fmt.Printf("%s: %v\n", col, values[i])
			}
			fmt.Println("---")
		}

		if err != nil {
			log.Printf("Error executing query: %v", err)
		} else {
			fmt.Printf("Query executed successfully: %s\n", query)
		}
	}

	test_transaction()

	resp, err := http.Get("http://localhost:8080/queries")
	if err != nil {
		fmt.Println("Error calling /queries:", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return
	}

	fmt.Println("Response from /queries:")
	fmt.Println(string(body))
}

func test_transaction() {
	connStr := "host=localhost port=5433 user=postgres password=postgres dbname=postgres sslmode=disable"
	fmt.Printf("Connecting with: %s\n", connStr)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	rows, err := tx.Query("SELECT 1 as oneinatransaction;")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	rows, err = tx.Query("SELECT 2 as twoinatransaction;")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	rows, err = tx.Query("SELECT 3 as threeinatransaction;")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}
}
