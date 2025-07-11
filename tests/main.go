package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/jackc/pgx/v4/pgxpool"
)

func main() {
	// Mock SQL queries that will be passed through the proxy
	queries := []string{
		"SELECT 1 as one;",
		"SELECT 3 as three;",
		"SELECT 2 as two;", // Simple test query first
	}

	// Connection parameters matching your working psql connection
	connStr := "postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"
	fmt.Printf("Connecting with: %s\n", connStr)

	// Create connection pool
	pool, err := pgxpool.Connect(context.Background(), connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	// Execute mock SQL queries through the proxy
	for _, query := range queries {
		fmt.Printf("Executing query: %s\n", query)
		rows, err := pool.Query(context.Background(), query)
		if err != nil {
			log.Fatalf("Error executing query: %v", err)
		}
		defer rows.Close()

		// Get column names
		fieldDescriptions := rows.FieldDescriptions()
		columns := make([]string, len(fieldDescriptions))
		for i, fd := range fieldDescriptions {
			columns[i] = string(fd.Name)
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

	test_transaction(pool)
	test_parameterized_transaction(pool)
	test_any_array_queries(pool)
	test_multiline_sql_queries(pool)

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

	// Pretty print the JSON response
	var parsedQueries []map[string]string
	if err := json.Unmarshal(body, &parsedQueries); err != nil {
		fmt.Printf("Error parsing JSON: %v\n", err)
		fmt.Println("Raw response:")
		fmt.Println(string(body))
		return
	}

	// Pretty print with indentation
	prettyJSON, err := json.MarshalIndent(parsedQueries, "", "  ")
	if err != nil {
		fmt.Printf("Error formatting JSON: %v\n", err)
		fmt.Println("Raw response:")
		fmt.Println(string(body))
		return
	}

	fmt.Printf("Found %d queries:\n", len(parsedQueries))
	fmt.Println(string(prettyJSON))
}

func test_transaction(pool *pgxpool.Pool) {
	fmt.Printf("Testing transactions\n")

	// Begin transaction
	tx, err := pool.Begin(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	// Use Exec instead of Query for simple queries in transaction
	_, err = tx.Exec(context.Background(), "SELECT 1 as oneinatransaction;")
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.Exec(context.Background(), "SELECT 2 as twoinatransaction;")
	if err != nil {
		log.Fatal(err)
	}

	_, err = tx.Exec(context.Background(), "SELECT 3 as threeinatransaction;")
	if err != nil {
		log.Fatal(err)
	}

	err = tx.Commit(context.Background())
	if err != nil {
		log.Fatal(err)
	}
}

func test_parameterized_transaction(pool *pgxpool.Pool) {
	fmt.Printf("Testing parameterized SQL in transaction\n")

	// Begin transaction
	tx, err := pool.Begin(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	// Test 1: Parameterized SELECT with single parameter
	fmt.Println("Test 1: Parameterized SELECT with single parameter")
	num := 42
	rows, err := tx.Query(context.Background(), "SELECT $1::int as param_value, ($1::int) * 2 as doubled;", num)
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
	num1, num2, num3 := 10, 20, 30
	rows2, err := tx.Query(context.Background(), "SELECT $1::int as first, $2::int as second, $3::int as third, ($1::int) + ($2::int) + ($3::int) as sum;", num1, num2, num3)
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
	_, err = tx.Exec(context.Background(), "SELECT $1::int as id, $2::text as name, $3::numeric as value;", 1, "test_item", 99.99)
	if err != nil {
		log.Fatal(err)
	}

	// Test 4: Parameterized UPDATE simulation
	fmt.Println("Test 4: Parameterized UPDATE simulation")
	_, err = tx.Exec(context.Background(), "SELECT $1::int as old_value, $2::int as new_value, 'updated' as status;", 50, 100)
	if err != nil {
		log.Fatal(err)
	}

	// Test 5: Mixed parameterized and non-parameterized queries
	fmt.Println("Test 5: Mixed parameterized and non-parameterized queries")
	_, err = tx.Exec(context.Background(), "SELECT 1 as constant, $1::text as parameter;", "mixed_test")
	if err != nil {
		log.Fatal(err)
	}

	// Commit the transaction
	err = tx.Commit(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Parameterized transaction test completed successfully")
}

func test_any_array_queries(pool *pgxpool.Pool) {
	fmt.Printf("Testing ANY(array) queries\n")

	// Begin transaction
	tx, err := pool.Begin(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	// Test 1: ANY(array) with string array - NO pq.Array() needed!
	fmt.Println("Test 1: ANY(array) with string array")
	statuses := []string{"active", "pending", "completed"}
	rows, err := tx.Query(context.Background(), "SELECT $1::text as status, $1::text = ANY($2::text[]) as is_in_array;", "active", statuses)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var isInArray bool
		err := rows.Scan(&status, &isInArray)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Status: %s, Is in array: %t\n", status, isInArray)
	}

	// Test 2: ANY(array) with integer array - NO pq.Array() needed!
	fmt.Println("Test 2: ANY(array) with integer array")
	numbers := []int{1, 2, 3, 4, 5}
	rows2, err := tx.Query(context.Background(), "SELECT $1::int as number, $1::int = ANY($2::int[]) as is_in_array;", 3, numbers)
	if err != nil {
		log.Fatal(err)
	}
	defer rows2.Close()

	for rows2.Next() {
		var number int
		var isInArray bool
		err := rows2.Scan(&number, &isInArray)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Number: %d, Is in array: %t\n", number, isInArray)
	}

	// Test 3: ANY(array) with mixed data types - NO pq.Array() needed!
	fmt.Println("Test 3: ANY(array) with mixed data types")
	values := []string{"apple", "banana", "cherry"}
	rows3, err := tx.Query(context.Background(), "SELECT $1::text as fruit, $1::text = ANY($2::text[]) as is_fruit;", "banana", values)
	if err != nil {
		log.Fatal(err)
	}
	defer rows3.Close()

	for rows3.Next() {
		var fruit string
		var isFruit bool
		err := rows3.Scan(&fruit, &isFruit)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Fruit: %s, Is fruit: %t\n", fruit, isFruit)
	}

	// Test 4: ANY(array) with empty array - NO pq.Array() needed!
	fmt.Println("Test 4: ANY(array) with empty array")
	emptyArray := []string{}
	rows4, err := tx.Query(context.Background(), "SELECT $1::text as value, $1::text = ANY($2::text[]) as is_in_empty_array;", "test", emptyArray)
	if err != nil {
		log.Fatal(err)
	}
	defer rows4.Close()

	for rows4.Next() {
		var value string
		var isInEmptyArray bool
		err := rows4.Scan(&value, &isInEmptyArray)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Value: %s, Is in empty array: %t\n", value, isInEmptyArray)
	}

	// Commit the transaction
	err = tx.Commit(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("ANY(array) queries test completed successfully")
}

func test_multiline_sql_queries(pool *pgxpool.Pool) {
	fmt.Printf("Testing multiline SQL queries\n")

	// Begin transaction
	tx, err := pool.Begin(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	// Test 1: SQL with newlines and tabs
	fmt.Println("Test 1: SQL with newlines and tabs")
	multilineQuery := `SELECT 
		$1::int as id,
		$2::text as name,
		$3::numeric as value
	FROM (SELECT 1) as dummy
	WHERE $1::int > 0
		AND $2::text IS NOT NULL
	ORDER BY $1::int;`

	rows, err := tx.Query(context.Background(), multilineQuery, 42, "multiline_test", 99.99)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var name string
		var value float64
		err := rows.Scan(&id, &name, &value)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("ID: %d, Name: %s, Value: %f\n", id, name, value)
	}

	// Test 2: SQL with complex formatting - NO pq.Array() needed!
	fmt.Println("Test 2: SQL with complex formatting")
	complexQuery := `SELECT 
		CASE 
			WHEN $1::int > 10 THEN 'high'
			WHEN $1::int > 5 THEN 'medium'
			ELSE 'low'
		END as category,
		$2::text as description,
		$3::int[] as numbers
	FROM (SELECT 1) as dummy
	WHERE $1::int BETWEEN 1 AND 100;`

	numbers := []int{1, 2, 3, 4, 5}
	rows2, err := tx.Query(context.Background(), complexQuery, 15, "complex formatting test", numbers)
	if err != nil {
		log.Fatal(err)
	}
	defer rows2.Close()

	for rows2.Next() {
		var category string
		var description string
		var numbersArray []int
		err := rows2.Scan(&category, &description, &numbersArray)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Category: %s, Description: %s, Numbers: %v\n", category, description, numbersArray)
	}

	// Test 3: SQL with subqueries and formatting
	fmt.Println("Test 3: SQL with subqueries and formatting")
	subquerySQL := `SELECT 
		$1::text as main_value,
		(SELECT $2::int * 2) as calculated_value,
		(SELECT $3::text || ' - processed') as processed_text
	FROM (SELECT 1) as dummy
	WHERE EXISTS (
		SELECT 1 
		FROM (SELECT $2::int as val) as sub 
		WHERE sub.val > 0
	);`

	rows3, err := tx.Query(context.Background(), subquerySQL, "main_value", 25, "original_text")
	if err != nil {
		log.Fatal(err)
	}
	defer rows3.Close()

	for rows3.Next() {
		var mainValue string
		var calculatedValue int
		var processedText string
		err := rows3.Scan(&mainValue, &calculatedValue, &processedText)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Main: %s, Calculated: %d, Processed: %s\n", mainValue, calculatedValue, processedText)
	}

	// Test 4: SQL with window functions and formatting
	fmt.Println("Test 4: SQL with window functions and formatting")
	windowQuery := `SELECT 
		$1::int as row_num,
		$2::text as name,
		$3::numeric as score,
		ROW_NUMBER() OVER (
			ORDER BY $3::numeric DESC
		) as rank
	FROM (SELECT 1) as dummy
	WHERE $1::int > 0;`

	rows4, err := tx.Query(context.Background(), windowQuery, 1, "window_test", 95.5)
	if err != nil {
		log.Fatal(err)
	}
	defer rows4.Close()

	for rows4.Next() {
		var rowNum int
		var name string
		var score float64
		var rank int
		err := rows4.Scan(&rowNum, &name, &score, &rank)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Row: %d, Name: %s, Score: %f, Rank: %d\n", rowNum, name, score, rank)
	}

	// Commit the transaction
	err = tx.Commit(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Multiline SQL queries test completed successfully")
}
