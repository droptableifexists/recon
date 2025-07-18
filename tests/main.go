package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
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

	// Test connection limiting
	testConnectionLimiting()

	// Test even more aggressive connection creation
	testAggressiveConnectionLimiting()

	// Test for connection leaks
	testConnectionLeaks()

	// Test specifically for EOF and connection closure issues
	testEOFAndConnectionIssues()

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

func testConnectionLimiting() {
	fmt.Println("=== Testing Connection Limiting ===")

	// Connection parameters
	connStr := "postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"

	// Test configuration - try to create MANY more connections to trigger limits
	numConnections := 10000 // Increased from 1000 to 10000
	concurrency := 500      // Increased from 100 to 500

	fmt.Printf("Attempting to create %d connections with %d concurrent workers\n",
		numConnections, concurrency)

	// Create a worker pool
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)

	startTime := time.Now()
	successfulConnections := 0
	failedConnections := 0
	connectionRefusedCount := 0
	var mu sync.Mutex

	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		semaphore <- struct{}{} // Acquire semaphore

		go func(connID int) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release semaphore

			// Create connection
			db, err := sql.Open("postgres", connStr)
			if err != nil {
				mu.Lock()
				failedConnections++
				if isConnectionRefused(err) {
					connectionRefusedCount++
				}
				mu.Unlock()
				if connID%1000 == 0 {
					log.Printf("Connection %d: Failed to open connection: %v", connID, err)
				}
				return
			}
			defer db.Close()

			// Test connection with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := db.PingContext(ctx); err != nil {
				mu.Lock()
				failedConnections++
				if isConnectionRefused(err) {
					connectionRefusedCount++
				}
				mu.Unlock()
				if connID%1000 == 0 {
					log.Printf("Connection %d: Failed to ping: %v", connID, err)
				}
				return
			}

			// Execute a simple query
			_, err = db.QueryContext(ctx, "SELECT 1")
			if err != nil {
				mu.Lock()
				failedConnections++
				if isConnectionRefused(err) {
					connectionRefusedCount++
				}
				mu.Unlock()
				if connID%1000 == 0 {
					log.Printf("Connection %d: Failed to query: %v", connID, err)
				}
				return
			}

			mu.Lock()
			successfulConnections++
			mu.Unlock()

			// Keep connection alive for a bit to simulate real usage
			time.Sleep(20 * time.Millisecond) // Reduced from 50ms to 20ms

			if connID%1000 == 0 {
				fmt.Printf("Connection %d: Success\n", connID)
			}
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()

	duration := time.Since(startTime)

	fmt.Printf("\n=== Connection Limit Test Results ===\n")
	fmt.Printf("Total connections attempted: %d\n", numConnections)
	fmt.Printf("Successful connections: %d\n", successfulConnections)
	fmt.Printf("Failed connections: %d\n", failedConnections)
	fmt.Printf("Connection refused errors: %d\n", connectionRefusedCount)
	fmt.Printf("Duration: %v\n", duration)
	fmt.Printf("Connections per second: %.2f\n", float64(successfulConnections)/duration.Seconds())

	if connectionRefusedCount > 0 {
		fmt.Printf("\n✅ SUCCESS: Connection limiting is working! %d connections were refused as expected.\n", connectionRefusedCount)
	} else {
		fmt.Printf("\n⚠️  WARNING: No connections were refused. The proxy might not be limiting connections properly.\n")
		fmt.Printf("   Consider checking if the proxy has connection limits configured.\n")
	}

	// Check if we have a reasonable number of successful connections
	if successfulConnections < 1000 {
		fmt.Printf("\n❌ ERROR: Too few successful connections (%d). The proxy might be too restrictive.\n", successfulConnections)
		fmt.Printf("FAILING CI: Proxy is not handling connections properly\n")
		os.Exit(1) // Fail the CI
	} else {
		fmt.Printf("\n✅ SUCCESS: Proxy handled %d connections successfully.\n", successfulConnections)
	}
}

func testAggressiveConnectionLimiting() {
	fmt.Println("\n=== Testing Aggressive Connection Limiting ===")

	// Connection parameters
	connStr := "postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"

	// Even more aggressive test - create connections as fast as possible
	numConnections := 50000 // 50k connections
	concurrency := 1000     // 1k concurrent workers

	fmt.Printf("AGGRESSIVE TEST: Attempting to create %d connections with %d concurrent workers\n",
		numConnections, concurrency)

	// Create a worker pool
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)

	startTime := time.Now()
	successfulConnections := 0
	failedConnections := 0
	connectionRefusedCount := 0
	var mu sync.Mutex

	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		semaphore <- struct{}{} // Acquire semaphore

		go func(connID int) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release semaphore

			// Create connection
			db, err := sql.Open("postgres", connStr)
			if err != nil {
				mu.Lock()
				failedConnections++
				if isConnectionRefused(err) {
					connectionRefusedCount++
				}
				mu.Unlock()
				if connID%5000 == 0 {
					log.Printf("AGGRESSIVE Connection %d: Failed to open: %v", connID, err)
				}
				return
			}
			defer db.Close()

			// Test connection with shorter timeout
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			if err := db.PingContext(ctx); err != nil {
				mu.Lock()
				failedConnections++
				if isConnectionRefused(err) {
					connectionRefusedCount++
				}
				mu.Unlock()
				if connID%5000 == 0 {
					log.Printf("AGGRESSIVE Connection %d: Failed to ping: %v", connID, err)
				}
				return
			}

			// Execute a simple query
			_, err = db.QueryContext(ctx, "SELECT 1")
			if err != nil {
				mu.Lock()
				failedConnections++
				if isConnectionRefused(err) {
					connectionRefusedCount++
				}
				mu.Unlock()
				if connID%5000 == 0 {
					log.Printf("AGGRESSIVE Connection %d: Failed to query: %v", connID, err)
				}
				return
			}

			mu.Lock()
			successfulConnections++
			mu.Unlock()

			// Minimal delay to keep connection alive
			time.Sleep(5 * time.Millisecond)

			if connID%5000 == 0 {
				fmt.Printf("AGGRESSIVE Connection %d: Success\n", connID)
			}
		}(i)
	}

	// Wait for all workers to complete
	wg.Wait()

	duration := time.Since(startTime)

	fmt.Printf("\n=== Aggressive Connection Limit Test Results ===\n")
	fmt.Printf("Total connections attempted: %d\n", numConnections)
	fmt.Printf("Successful connections: %d\n", successfulConnections)
	fmt.Printf("Failed connections: %d\n", failedConnections)
	fmt.Printf("Connection refused errors: %d\n", connectionRefusedCount)
	fmt.Printf("Duration: %v\n", duration)
	fmt.Printf("Connections per second: %.2f\n", float64(successfulConnections)/duration.Seconds())

	if connectionRefusedCount > 0 {
		fmt.Printf("\n✅ SUCCESS: Aggressive test shows connection limiting is working! %d connections were refused.\n", connectionRefusedCount)
	} else {
		fmt.Printf("\n❌ PROBLEM: Even with %d aggressive connections, no connection refused errors occurred.\n", numConnections)
		fmt.Printf("   This suggests the proxy is NOT limiting connections and may be overwhelming the database.\n")
		fmt.Printf("FAILING CI: No connection limiting detected\n")
		os.Exit(1) // Fail the CI
	}

	// Check if we have a reasonable number of successful connections
	if successfulConnections < 5000 {
		fmt.Printf("\n❌ ERROR: Too few successful connections (%d). The proxy might be too restrictive.\n", successfulConnections)
		fmt.Printf("FAILING CI: Proxy is too restrictive\n")
		os.Exit(1) // Fail the CI
	} else {
		fmt.Printf("\n✅ SUCCESS: Proxy handled %d aggressive connections successfully.\n", successfulConnections)
	}
}

func testConnectionLeaks() {
	fmt.Println("\n=== Testing for Connection Leaks ===")

	// Connection parameters
	connStr := "postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"

	// Test configuration - create connections and check if they're properly closed
	numConnections := 1000
	concurrency := 50
	iterations := 5

	fmt.Printf("Testing for connection leaks: %d connections x %d iterations with %d concurrent workers\n",
		numConnections, iterations, concurrency)

	// Track connection behavior over multiple iterations
	var allSuccessfulConnections int
	var allFailedConnections int
	var allConnectionRefusedCount int

	for iter := 0; iter < iterations; iter++ {
		fmt.Printf("\n--- Iteration %d/%d ---\n", iter+1, iterations)

		// Create a worker pool
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, concurrency)

		startTime := time.Now()
		successfulConnections := 0
		failedConnections := 0
		connectionRefusedCount := 0
		var mu sync.Mutex

		for i := 0; i < numConnections; i++ {
			wg.Add(1)
			semaphore <- struct{}{} // Acquire semaphore

			go func(connID int) {
				defer wg.Done()
				defer func() { <-semaphore }() // Release semaphore

				// Create connection
				db, err := sql.Open("postgres", connStr)
				if err != nil {
					mu.Lock()
					failedConnections++
					if isConnectionRefused(err) {
						connectionRefusedCount++
					}
					mu.Unlock()
					return
				}
				defer db.Close() // This should close the connection

				// Test connection with timeout
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()

				if err := db.PingContext(ctx); err != nil {
					mu.Lock()
					failedConnections++
					if isConnectionRefused(err) {
						connectionRefusedCount++
					}
					mu.Unlock()
					return
				}

				// Execute a simple query
				_, err = db.QueryContext(ctx, "SELECT 1")
				if err != nil {
					mu.Lock()
					failedConnections++
					if isConnectionRefused(err) {
						connectionRefusedCount++
					}
					mu.Unlock()
					return
				}

				mu.Lock()
				successfulConnections++
				mu.Unlock()

				// Keep connection alive briefly
				time.Sleep(10 * time.Millisecond)
			}(i)
		}

		// Wait for all workers to complete
		wg.Wait()

		duration := time.Since(startTime)

		fmt.Printf("Iteration %d Results:\n", iter+1)
		fmt.Printf("  Successful: %d, Failed: %d, Refused: %d\n",
			successfulConnections, failedConnections, connectionRefusedCount)
		fmt.Printf("  Duration: %v, Rate: %.2f conn/s\n",
			duration, float64(successfulConnections)/duration.Seconds())

		// Track totals
		allSuccessfulConnections += successfulConnections
		allFailedConnections += failedConnections
		allConnectionRefusedCount += connectionRefusedCount

		// Check for increasing failure rates (indicates connection leaks)
		if iter > 0 && connectionRefusedCount > 0 {
			fmt.Printf("  ⚠️  Connection refused errors detected - possible connection leak\n")
		}

		// Wait a bit between iterations to see if connections are properly cleaned up
		time.Sleep(2 * time.Second)
	}

	fmt.Printf("\n=== Connection Leak Test Summary ===\n")
	fmt.Printf("Total iterations: %d\n", iterations)
	fmt.Printf("Total successful connections: %d\n", allSuccessfulConnections)
	fmt.Printf("Total failed connections: %d\n", allFailedConnections)
	fmt.Printf("Total connection refused errors: %d\n", allConnectionRefusedCount)

	// Analyze for connection leaks
	if allConnectionRefusedCount > 0 {
		fmt.Printf("\n❌ CONNECTION LEAK DETECTED: %d connection refused errors across %d iterations\n",
			allConnectionRefusedCount, iterations)
		fmt.Printf("   This suggests the proxy is not properly closing connections to the database.\n")
		fmt.Printf("   Each iteration should have similar success rates if connections are properly managed.\n")
		fmt.Printf("FAILING CI: Connection leaks detected\n")
		os.Exit(1) // Fail the CI
	} else {
		fmt.Printf("\n✅ NO CONNECTION LEAKS DETECTED: All %d connections were handled successfully\n",
			allSuccessfulConnections)
		fmt.Printf("   The proxy appears to be properly closing connections.\n")
	}

	// Check for consistent performance (indicates good connection management)
	avgSuccessPerIteration := allSuccessfulConnections / iterations
	fmt.Printf("\nAverage successful connections per iteration: %d\n", avgSuccessPerIteration)

	if avgSuccessPerIteration > 950 { // 95% success rate
		fmt.Printf("✅ Consistent performance across iterations - good connection management\n")
	} else {
		fmt.Printf("⚠️  Inconsistent performance - possible connection management issues\n")
		fmt.Printf("FAILING CI: Inconsistent connection performance\n")
		os.Exit(1) // Fail the CI
	}
}

func testEOFAndConnectionIssues() {
	fmt.Println("\n=== Testing for EOF and Connection Closure Issues ===")

	// Connection parameters
	connStr := "postgres://postgres:postgres@localhost:5433/postgres?sslmode=disable"

	// Test configuration - create connections and check for EOF errors
	numConnections := 1000
	concurrency := 50
	iterations := 5

	fmt.Printf("Testing for EOF and connection closure issues: %d connections x %d iterations with %d concurrent workers\n",
		numConnections, iterations, concurrency)

	// Track connection behavior over multiple iterations
	var allSuccessfulConnections int
	var allFailedConnections int
	var allConnectionRefusedCount int
	var allEOFCount int
	var errorTypes = make(map[string]int)

	for iter := 0; iter < iterations; iter++ {
		fmt.Printf("\n--- Iteration %d/%d ---\n", iter+1, iterations)

		// Create a worker pool
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, concurrency)

		startTime := time.Now()
		successfulConnections := 0
		failedConnections := 0
		connectionRefusedCount := 0
		eofCount := 0
		var mu sync.Mutex

		for i := 0; i < numConnections; i++ {
			wg.Add(1)
			semaphore <- struct{}{} // Acquire semaphore

			go func(connID int) {
				defer wg.Done()
				defer func() { <-semaphore }() // Release semaphore

				// Create connection
				db, err := sql.Open("postgres", connStr)
				if err != nil {
					mu.Lock()
					failedConnections++
					if isConnectionRefused(err) {
						connectionRefusedCount++
					}
					// Track specific error types
					errStr := err.Error()
					errorTypes[errStr]++
					if contains(errStr, "EOF") {
						eofCount++
					}
					mu.Unlock()
					if connID%100 == 0 {
						log.Printf("Connection %d: Failed to open: %v", connID, err)
					}
					return
				}
				defer db.Close() // This should close the connection

				// Test connection with timeout
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()

				if err := db.PingContext(ctx); err != nil {
					mu.Lock()
					failedConnections++
					if isConnectionRefused(err) {
						connectionRefusedCount++
					}
					// Track specific error types
					errStr := err.Error()
					errorTypes[errStr]++
					if contains(errStr, "EOF") {
						eofCount++
					}
					mu.Unlock()
					if connID%100 == 0 {
						log.Printf("Connection %d: Failed to ping: %v", connID, err)
					}
					return
				}

				// Execute a simple query
				_, err = db.QueryContext(ctx, "SELECT 1")
				if err != nil {
					mu.Lock()
					failedConnections++
					if isConnectionRefused(err) {
						connectionRefusedCount++
					}
					// Track specific error types
					errStr := err.Error()
					errorTypes[errStr]++
					if contains(errStr, "EOF") {
						eofCount++
					}
					mu.Unlock()
					if connID%100 == 0 {
						log.Printf("Connection %d: Failed to query: %v", connID, err)
					}
					return
				}

				mu.Lock()
				successfulConnections++
				mu.Unlock()

				// Keep connection alive briefly
				time.Sleep(10 * time.Millisecond)
			}(i)
		}

		// Wait for all workers to complete
		wg.Wait()

		duration := time.Since(startTime)

		fmt.Printf("Iteration %d Results:\n", iter+1)
		fmt.Printf("  Successful: %d, Failed: %d, Refused: %d, EOF: %d\n",
			successfulConnections, failedConnections, connectionRefusedCount, eofCount)
		fmt.Printf("  Duration: %v, Rate: %.2f conn/s\n",
			duration, float64(successfulConnections)/duration.Seconds())

		// Track totals
		allSuccessfulConnections += successfulConnections
		allFailedConnections += failedConnections
		allConnectionRefusedCount += connectionRefusedCount
		allEOFCount += eofCount

		// Check for increasing failure rates (indicates connection leaks)
		if iter > 0 && (connectionRefusedCount > 0 || eofCount > 0) {
			fmt.Printf("  ⚠️  Connection errors detected - possible connection leak or EOF issues\n")
		}

		// Wait a bit between iterations to see if connections are properly cleaned up
		time.Sleep(2 * time.Second)
	}

	fmt.Printf("\n=== EOF and Connection Closure Test Summary ===\n")
	fmt.Printf("Total iterations: %d\n", iterations)
	fmt.Printf("Total successful connections: %d\n", allSuccessfulConnections)
	fmt.Printf("Total failed connections: %d\n", allFailedConnections)
	fmt.Printf("Total connection refused errors: %d\n", allConnectionRefusedCount)
	fmt.Printf("Total EOF errors: %d\n", allEOFCount)

	// Show most common error types
	if len(errorTypes) > 0 {
		fmt.Printf("\nMost common error types:\n")
		for errStr, count := range errorTypes {
			if count > 10 { // Only show errors that occurred more than 10 times
				fmt.Printf("  %s: %d occurrences\n", errStr, count)
			}
		}
	}

	// Analyze for EOF and connection closure issues
	if allEOFCount > 0 {
		fmt.Printf("\n❌ EOF ERRORS DETECTED: %d EOF errors across %d iterations\n",
			allEOFCount, iterations)
		fmt.Printf("   This suggests the proxy is closing connections unexpectedly or not handling them properly.\n")
		fmt.Printf("FAILING CI: EOF errors detected\n")
		os.Exit(1) // Fail the CI
	} else if allConnectionRefusedCount > 0 {
		fmt.Printf("\n❌ CONNECTION LEAK DETECTED: %d connection refused errors across %d iterations\n",
			allConnectionRefusedCount, iterations)
		fmt.Printf("   This suggests the proxy is not properly closing connections to the database.\n")
		fmt.Printf("FAILING CI: Connection leaks detected\n")
		os.Exit(1) // Fail the CI
	} else {
		fmt.Printf("\n✅ NO CONNECTION ISSUES DETECTED: All %d connections were handled successfully\n",
			allSuccessfulConnections)
		fmt.Printf("   The proxy appears to be properly managing connections.\n")
	}

	// Check for consistent performance (indicates good connection management)
	avgSuccessPerIteration := allSuccessfulConnections / iterations
	fmt.Printf("\nAverage successful connections per iteration: %d\n", avgSuccessPerIteration)

	if avgSuccessPerIteration > 950 { // 95% success rate
		fmt.Printf("✅ Consistent performance across iterations - good connection management\n")
	} else {
		fmt.Printf("⚠️  Inconsistent performance - possible connection management issues\n")
		fmt.Printf("FAILING CI: Inconsistent connection performance\n")
		os.Exit(1) // Fail the CI
	}
}

// isConnectionRefused checks if the error is a connection refused error
func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return contains(errStr, "connection refused") ||
		contains(errStr, "connection reset") ||
		contains(errStr, "no such host") ||
		contains(errStr, "timeout") ||
		contains(errStr, "too many open files") ||
		contains(errStr, "EOF") || // End of File - connection closed unexpectedly
		contains(errStr, "broken pipe") || // Connection broken
		contains(errStr, "connection reset by peer") || // Remote end closed connection
		contains(errStr, "use of closed network connection") || // Connection was closed
		contains(errStr, "driver: bad connection") || // Bad connection state
		contains(errStr, "invalid connection") // Invalid connection
}

// contains is a simple string contains function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			containsSubstring(s, substr))))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
