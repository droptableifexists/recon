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
	test_parameterized_transaction()

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

	// Use Exec instead of Query for simple queries in transaction
	_, err = tx.Exec("SELECT 1 as oneinatransaction;")
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.Exec("SELECT 2 as twoinatransaction;")
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.Exec("SELECT 3 as threeinatransaction;")
	if err != nil {
		log.Fatal(err)
	}

	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}
}

func test_parameterized_transaction() {
	connStr := "host=localhost port=5433 user=postgres password=postgres dbname=postgres sslmode=disable"
	fmt.Printf("Testing parameterized SQL in transaction with: %s\n", connStr)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Start transaction
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	// Test 1: Parameterized SELECT with single parameter
	fmt.Println("Test 1: Parameterized SELECT with single parameter")
	rows, err := tx.Query("SELECT $1 as param_value, $1 * 2 as doubled;", 42)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var paramValue, doubled int
		err := rows.Scan(&paramValue, &doubled)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Parameter: %d, Doubled: %d\n", paramValue, doubled)
	}

	// Test 2: Parameterized SELECT with multiple parameters
	fmt.Println("Test 2: Parameterized SELECT with multiple parameters")
	rows2, err := tx.Query("SELECT $1 as first, $2 as second, $3 as third, $1 + $2 + $3 as sum;", 10, 20, 30)
	if err != nil {
		log.Fatal(err)
	}
	defer rows2.Close()

	for rows2.Next() {
		var first, second, third, sum int
		err := rows2.Scan(&first, &second, &third, &sum)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("First: %d, Second: %d, Third: %d, Sum: %d\n", first, second, third, sum)
	}

	// Test 3: Parameterized INSERT (if we had a table, this would work)
	fmt.Println("Test 3: Parameterized INSERT simulation")
	_, err = tx.Exec("SELECT $1 as id, $2 as name, $3 as value;", 1, "test_item", 99.99)
	if err != nil {
		log.Fatal(err)
	}

	// Test 4: Parameterized UPDATE simulation
	fmt.Println("Test 4: Parameterized UPDATE simulation")
	_, err = tx.Exec("SELECT $1 as old_value, $2 as new_value, 'updated' as status;", 50, 100)
	if err != nil {
		log.Fatal(err)
	}

	// Test 5: Mixed parameterized and non-parameterized queries
	fmt.Println("Test 5: Mixed parameterized and non-parameterized queries")
	_, err = tx.Exec("SELECT 1 as constant, $1 as parameter;", "mixed_test")
	if err != nil {
		log.Fatal(err)
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Parameterized transaction test completed successfully")
}
