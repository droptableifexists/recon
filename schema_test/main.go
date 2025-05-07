package main

import (
	"database/sql"
	"fmt"
	"io/ioutil"
	"os"

	_ "github.com/lib/pq"
)

func migrateDB(sqlFile, connStr string) error {
	// Open database connection
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to open database: %v", err)
	}
	defer db.Close()

	// Read SQL file
	sqlBytes, err := ioutil.ReadFile(sqlFile)
	if err != nil {
		return fmt.Errorf("failed to read SQL file: %v", err)
	}
	sqlScript := string(sqlBytes)

	// Execute SQL commands
	_, err = db.Exec(sqlScript)
	if err != nil {
		return fmt.Errorf("failed to execute SQL: %v", err)
	}
	db.Exec("CREATE DATABASE test;")

	fmt.Println("Database migration successful!")
	return nil
}

func main() {
	connStr := "host=localhost port=5432 user=postgres password=postgres dbname=postgres sslmode=disable"
	if err := migrateDB("test.sql", connStr); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	connStr2 := "host=localhost port=5432 user=postgres password=postgres dbname=test sslmode=disable"
	if err := migrateDB("test2.sql", connStr2); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
