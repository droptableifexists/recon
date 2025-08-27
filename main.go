package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type Query struct {
	Query string `json:"Query"`
}

type TableDiff struct {
	Added        bool                  `json:"added,omitempty"`
	Removed      bool                  `json:"removed,omitempty"`
	SchemaChange string                `json:"schema_change,omitempty"`
	Columns      map[string]ColumnDiff `json:"columns,omitempty"`
	Indexes      []IndexDiff           `json:"indexes,omitempty"`
	Constraints  []ConstraintDiff      `json:"constraints,omitempty"`
}

type ColumnDiff struct {
	Added       bool   `json:"added,omitempty"`
	Removed     bool   `json:"removed,omitempty"`
	TypeChanged string `json:"type_changed,omitempty"`
	NullChanged bool   `json:"null_changed,omitempty"`
}

type IndexDiff struct {
	Old string `json:"old,omitempty"`
	New string `json:"new,omitempty"`
}

type ConstraintDiff struct {
	Name    string   `json:"name"`
	Added   bool     `json:"added,omitempty"`
	Removed bool     `json:"removed,omitempty"`
	Type    string   `json:"type,omitempty"`
	Columns []string `json:"columns,omitempty"`
}

type DatabaseSchema struct {
	Database string
	Tables   map[string]TableSchema
}

type TableSchema struct {
	Name        string
	Schema      string
	Columns     []ColumnSchema
	Indexes     []IndexSchema
	Constraints []ConstraintSchema
}

type ColumnSchema struct {
	Name     string
	Type     string
	Nullable bool
	Default  string
}

type IndexSchema struct {
	Definition string
}

type ConstraintSchema struct {
	Definition string
}

type TableChanges struct {
	Database string       `json:"database"`
	Schema   string       `json:"schema"`
	Table    string       `json:"table"`
	Old      *TableSchema `json:"old,omitempty"`
	New      *TableSchema `json:"new,omitempty"`
}

func main() {
	// Call the proxy's API
	apiAddress := os.Getenv("SQL_PROXY_API_ADDRESS")
	fmt.Println("Calling proxy API on address", apiAddress)
	resp, err := http.Get("http://" + apiAddress + "/queries")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to call proxy API: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read proxy response: %v\n", err)
		os.Exit(1)
	}

	// Fetch baseline artifact queries (optional)
	testSuiteName := os.Getenv("TEST_SUITE_NAME")
	if testSuiteName == "" {
		testSuiteName = "default"
	}
	queriesBaseline := getArtifactFromMain(fmt.Sprintf("sql-queries-%s", testSuiteName))

	// Generate JSON diff
	queryDiff := diffQueries(body, queriesBaseline)

	// Just use the new queries without plans
	queryDiffJSON, err := json.Marshal(queryDiff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal query diff: %v\n", err)
		os.Exit(1)
	}

	// Check if schema comparison should be skipped
	skipSchemaComparison := os.Getenv("SKIP_SCHEMA_DUMP") == "true"

	var schemaJSON []byte
	var schemaDiffJSON []byte

	if skipSchemaComparison {
		fmt.Println("SKIP_SCHEMA_DUMP=true, skipping schema comparison and using baseline schema")

		// Get baseline schema from main
		schemaBaseline := getArtifactFromMain(fmt.Sprintf("full-schema-%s", testSuiteName))

		// Use baseline schema as current schema
		schemaJSON = []byte(schemaBaseline)

		// Create empty schema diff (no changes)
		schemaDiffJSON = []byte("[]")
	} else {
		// Generate schema SQL
		var schemaResp *http.Response
		var schemaBody []byte
		var schemaErr error

		// Retry logic for schema endpoint
		maxRetries := 25
		for attempt := 1; attempt <= maxRetries; attempt++ {
			fmt.Printf("Calling schema API (attempt %d/%d)...\n", attempt, maxRetries)
			schemaResp, schemaErr = http.Get("http://" + apiAddress + "/schema")
			if schemaErr != nil {
				fmt.Fprintf(os.Stderr, "Failed to call schema API: %v\n", schemaErr)
				if attempt == maxRetries {
					os.Exit(1)
				}
				time.Sleep(30 * time.Second)
				continue
			}

			if schemaResp.StatusCode == http.StatusServiceUnavailable {
				fmt.Printf("Schema dump not finished, retrying in 30 seconds...\n")
				schemaResp.Body.Close()
				if attempt == maxRetries {
					fmt.Fprintf(os.Stderr, "Schema dump failed to complete after %d attempts\n", maxRetries)
					os.Exit(1)
				}
				time.Sleep(30 * time.Second)
				continue
			}

			if schemaResp.StatusCode != http.StatusOK {
				fmt.Fprintf(os.Stderr, "Schema API returned status %d\n", schemaResp.StatusCode)
				body, _ := io.ReadAll(schemaResp.Body)
				fmt.Fprintf(os.Stderr, "Schema API response body: %s\n", string(body))
				schemaResp.Body.Close()
				if attempt == maxRetries {
					os.Exit(1)
				}
				time.Sleep(2 * time.Second)
				continue
			}

			// Success - read the response
			schemaBody, schemaErr = io.ReadAll(schemaResp.Body)
			schemaResp.Body.Close()
			if schemaErr != nil {
				fmt.Fprintf(os.Stderr, "Failed to read schema response: %v\n", schemaErr)
				if attempt == maxRetries {
					os.Exit(1)
				}
				time.Sleep(2 * time.Second)
				continue
			}

			break // Success, exit retry loop
		}

		// Parse the schema response
		var databaseSchema []DatabaseSchema
		if err := json.Unmarshal(schemaBody, &databaseSchema); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to unmarshal schema response: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Database schema: %v\n", databaseSchema)

		schemaJSON, err = json.Marshal(databaseSchema)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to marshal database schema: %v\n", err)
			os.Exit(1)
		}

		schemaBaseline := getArtifactFromMain(fmt.Sprintf("full-schema-%s", testSuiteName))

		// Parse the baseline schema from JSON string
		var baselineSchema []DatabaseSchema
		if err := json.Unmarshal([]byte(schemaBaseline), &baselineSchema); err != nil {
			baselineSchema = []DatabaseSchema{}
		}

		schemaDiff := compareSchema(databaseSchema, baselineSchema)

		schemaDiffJSON, err = json.Marshal(schemaDiff)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to marshal schema diff: %v\n", err)
			os.Exit(1)
		}
	}

	// Create individual JSON files in the root directory
	fmt.Printf("Creating JSON files...\n")

	// SQL queries artifact
	sqlQueriesPath := "sql-queries.json"
	if err := os.WriteFile(sqlQueriesPath, body, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write sql-queries.json: %v\n", err)
		os.Exit(1)
	}

	// Queries diff artifact
	queriesDiffPath := "queries-diff.json"
	if err := os.WriteFile(queriesDiffPath, queryDiffJSON, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write queries-diff.json: %v\n", err)
		os.Exit(1)
	}

	// Schema artifact
	schemaPath := "full-schema.json"
	if err := os.WriteFile(schemaPath, schemaJSON, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write full-schema.json: %v\n", err)
		os.Exit(1)
	}

	// Schema diff artifact
	schemaDiffPath := "schema-diff.json"
	if err := os.WriteFile(schemaDiffPath, schemaDiffJSON, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write schema-diff.json: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Successfully created JSON files:")
	fmt.Println("- sql-queries.json")
	fmt.Println("- queries-diff.json")
	fmt.Println("- full-schema.json")
	fmt.Println("- schema-diff.json")
	fmt.Println("")
	fmt.Println("To upload these as individual GitHub Actions artifacts, add these steps to your workflow:")
	fmt.Println("- name: Upload SQL queries")
	fmt.Println("  uses: actions/upload-artifact@v4")
	fmt.Println("  with:")
	fmt.Println("    name: sql-queries")
	fmt.Println("    path: sql-queries.json")
	fmt.Println("")
	fmt.Println("- name: Upload queries diff")
	fmt.Println("  uses: actions/upload-artifact@v4")
	fmt.Println("  with:")
	fmt.Println("    name: queries-diff")
	fmt.Println("    path: queries-diff.json")
	fmt.Println("")
	fmt.Println("- name: Upload full schema")
	fmt.Println("  uses: actions/upload-artifact@v4")
	fmt.Println("  with:")
	fmt.Println("    name: full-schema")
	fmt.Println("    path: full-schema.json")
	fmt.Println("")
	fmt.Println("- name: Upload schema diff")
	fmt.Println("  uses: actions/upload-artifact@v4")
	fmt.Println("  with:")
	fmt.Println("    name: schema-diff")
	fmt.Println("    path: schema-diff.json")
}

// Fetch and extract the sql-queries-main artifact content (JSON string)
func getArtifactFromMain(name string) string {
	repo := os.Getenv("GITHUB_REPOSITORY") // owner/repo
	token := os.Getenv("GITHUB_TOKEN")     // GitHub token

	if repo == "" || token == "" {
		fmt.Fprintf(os.Stderr, "Warning: GITHUB_REPOSITORY or GITHUB_TOKEN not set\n")
		return ""
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/artifacts", repo)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Authorization", "token "+token)
	client := &http.Client{}

	type Artifact struct {
		Name        string `json:"name"`
		ArchiveURL  string `json:"archive_download_url"`
		CreatedAt   string `json:"created_at"`
		WorkflowRun struct {
			HeadBranch string `json:"head_branch"`
		} `json:"workflow_run"`
	}
	type ArtifactsResponse struct {
		TotalCount int        `json:"total_count"`
		Artifacts  []Artifact `json:"artifacts"`
	}

	// Add name parameter and increase per_page to 100
	apiURL = fmt.Sprintf("%s?per_page=1000&name=%s", apiURL, url.QueryEscape(name))
	req, _ = http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Authorization", "token "+token)
	fmt.Println("API URL:", apiURL)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to list artifacts: %v\n", err)
		return ""
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to read artifact list response: %v\n", err)
		return ""
	}

	var artifactsResp ArtifactsResponse
	if err := json.Unmarshal(body, &artifactsResp); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to decode artifact list: %v\n", err)
		return ""
	}

	fmt.Fprintf(os.Stderr, "Debug: Total artifacts found: %d\n", artifactsResp.TotalCount)
	fmt.Fprintf(os.Stderr, "Debug: All artifacts:\n")
	for _, a := range artifactsResp.Artifacts {
		fmt.Fprintf(os.Stderr, "  - '%s' (branch: %s)\n", a.Name, a.WorkflowRun.HeadBranch)
	}

	var candidates []Artifact
	for _, a := range artifactsResp.Artifacts {
		// Check if this is a main branch artifact
		if a.WorkflowRun.HeadBranch == "main" {
			// Check if name matches what we're looking for (base name + service name)
			baseNameMatch := strings.Contains(strings.ToLower(a.Name), strings.ToLower(name))
			fmt.Fprintf(os.Stderr, "Debug: Artifact '%s' - baseNameMatch: %t\n", a.Name, baseNameMatch)
			if baseNameMatch {
				candidates = append(candidates, a)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "Debug: Found %d candidate artifacts for name='%s'\n", len(candidates), name)

	if len(candidates) == 0 {
		fmt.Fprintf(os.Stderr, "Error: No baseline artifact found for name='%s'\n", name)
		return ""
	}

	sort.Slice(candidates, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, candidates[i].CreatedAt)
		tj, _ := time.Parse(time.RFC3339, candidates[j].CreatedAt)
		return ti.After(tj)
	})

	latest := candidates[0]
	fmt.Fprintf(os.Stderr, "Selected artifact: %s (created at: %s)\n", latest.Name, latest.CreatedAt)

	// Log all candidates with their timestamps for verification
	fmt.Fprintf(os.Stderr, "Debug: All candidates sorted by creation time (newest first):\n")
	for i, candidate := range candidates {
		createdTime, _ := time.Parse(time.RFC3339, candidate.CreatedAt)
		fmt.Fprintf(os.Stderr, "  %d. %s (created at: %s)\n", i+1, candidate.Name, createdTime.Format("2006-01-02 15:04:05"))
	}

	// Download the ZIP archive of the artifact
	reqZip, _ := http.NewRequest("GET", latest.ArchiveURL, nil)
	reqZip.Header.Set("Authorization", "token "+token)
	respZip, err := client.Do(reqZip)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to download artifact zip: %v\n", err)
		return ""
	}
	defer respZip.Body.Close()

	tmpFile, err := os.CreateTemp("", "artifact-*.zip")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to create temp file: %v\n", err)
		return ""
	}
	defer os.Remove(tmpFile.Name()) // clean up

	_, err = io.Copy(tmpFile, respZip.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to save artifact zip: %v\n", err)
		return ""
	}

	if err := tmpFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to close temp file: %v\n", err)
		return ""
	}

	// Open ZIP and find queries.json
	zipReader, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to open zip archive: %v\n", err)
		return ""
	}
	defer zipReader.Close()

	for _, file := range zipReader.File {
		if file.Name == "sql-queries.json" || file.Name == "full-schema.json" {
			rc, err := file.Open()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to open queries.json in zip: %v\n", err)
				return ""
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to read queries.json: %v\n", err)
				return ""
			}
			fmt.Fprintf(os.Stderr, "Successfully fetched baseline queries.json\n")
			return string(data)
		}
	}

	fmt.Fprintf(os.Stderr, "Warning: queries.json not found in artifact\n")
	return ""
}

func diffQueries(current []byte, baseline string) []Query {
	currentQueries := []Query{}
	json.Unmarshal(current, &currentQueries)

	baselineQueries := []Query{}
	json.Unmarshal([]byte(baseline), &baselineQueries)

	// Create a hashmap of baseline queries for O(1) lookup
	baselineMap := make(map[string]bool)
	for _, q := range baselineQueries {
		baselineMap[q.Query] = true
	}

	// Find new queries by checking if they exist in baseline
	var newQueries []Query
	for _, q := range currentQueries {
		if !baselineMap[q.Query] {
			newQueries = append(newQueries, q)
		}
	}

	fmt.Fprintf(os.Stderr, "Debug: Current queries: %d, Baseline queries: %d, New queries: %d\n",
		len(currentQueries), len(baselineQueries), len(newQueries))

	return newQueries
}

func compareSchema(current, baseline []DatabaseSchema) []TableChanges {
	var tableChanges []TableChanges
	currentDBMap := getDatabaseSchemaMap(current)
	baselineDBMap := getDatabaseSchemaMap(baseline)

	// Compare current databases against baseline
	for dbName, currentDB := range currentDBMap {
		baselineDB, exists := baselineDBMap[dbName]
		if !exists {
			// Database doesn't exist in baseline - all tables are new
			for _, currentTable := range currentDB.Tables {
				tableCopy := currentTable // Create a copy to avoid pointer reuse
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   currentTable.Schema,
					Table:    currentTable.Name,
					New:      &tableCopy,
				})
			}
			continue
		}

		// Database exists in baseline - compare tables
		for tableName, currentTable := range currentDB.Tables {
			baselineTable, tableExists := baselineDB.Tables[tableName]
			if !tableExists {
				// Table doesn't exist in baseline - it's new
				currentTableCopy := currentTable // Create a copy to avoid pointer reuse
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   currentTable.Schema,
					Table:    tableName,
					New:      &currentTableCopy,
				})
				continue
			}

			// Table exists in both - compare for changes
			if !reflect.DeepEqual(currentTable, baselineTable) {
				// Tables are different - log the differences
				jsonCurrent, _ := json.Marshal(currentTable)
				jsonBaseline, _ := json.Marshal(baselineTable)
				fmt.Printf("\nTable changed: %s.%s\n", dbName, tableName)
				fmt.Printf("Current: %s\n", string(jsonCurrent))
				fmt.Printf("Baseline: %s\n", string(jsonBaseline))

				currentTableCopy := currentTable   // Create a copy to avoid pointer reuse
				baselineTableCopy := baselineTable // Create a copy to avoid pointer reuse
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   currentTable.Schema,
					Table:    tableName,
					Old:      &baselineTableCopy,
					New:      &currentTableCopy,
				})
			}
		}
	}

	// Check for tables that were removed (exist in baseline but not in current)
	for dbName, baselineDB := range baselineDBMap {
		currentDB, exists := currentDBMap[dbName]
		if !exists {
			// Database was removed - all its tables are removed
			for _, baselineTable := range baselineDB.Tables {
				baselineTableCopy := baselineTable // Create a copy to avoid pointer reuse
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   baselineTable.Schema,
					Table:    baselineTable.Name,
					Old:      &baselineTableCopy,
				})
			}
			continue
		}

		// Database exists in both - check for removed tables
		for tableName, baselineTable := range baselineDB.Tables {
			if _, tableExists := currentDB.Tables[tableName]; !tableExists {
				// Table exists in baseline but not in current - it was removed
				baselineTableCopy := baselineTable // Create a copy to avoid pointer reuse
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   baselineTable.Schema,
					Table:    tableName,
					Old:      &baselineTableCopy,
				})
			}
		}
	}

	return tableChanges
}

func getDatabaseSchemaMap(databases []DatabaseSchema) map[string]DatabaseSchema {
	databaseSchemaMap := map[string]DatabaseSchema{}
	for _, database := range databases {
		databaseSchemaMap[database.Database] = database
	}
	return databaseSchemaMap
}
