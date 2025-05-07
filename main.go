package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type Query struct {
	Query string `json:"Query"`
}

type SchemaDiff struct {
	AddedDatabases   []string                        `json:"added_databases,omitempty"`
	RemovedDatabases []string                        `json:"removed_databases,omitempty"`
	ModifiedTables   map[string]map[string]TableDiff `json:"modified_tables,omitempty"`
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

func main() {
	// Call the proxy's API
	resp, err := http.Get("http://proxy:8080/queries")
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
	queriesBaseline := getArtifactFromMain("queries")
	fmt.Print("queriesBaseline:")
	fmt.Print(queriesBaseline)
	fmt.Print("current:")
	fmt.Print(string(body))

	// Generate JSON diff
	queryDiff := generateQueryDiff(string(body), queriesBaseline)

	// Generate schema SQL
	connStr := "host=postgres port=5432 user=postgres password=postgres dbname=postgres sslmode=disable"
	databaseSchema := GetDatabaseSchema(connStr)
	schemaJSON, err := json.Marshal(databaseSchema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal database schema: %v\n", err)
		os.Exit(1)
	}

	schemaBaseline := getArtifactFromMain("schema")
	fmt.Print("schemaBaseline:")
	fmt.Print(schemaBaseline)
	fmt.Print("current:")
	fmt.Print(string(schemaJSON))
	schemaDiff := generateSchemaDiff(string(schemaJSON), schemaBaseline)

	// Write to GITHUB_OUTPUT
	outputPath := os.Getenv("GITHUB_OUTPUT")
	if outputPath == "" {
		fmt.Fprintf(os.Stderr, "GITHUB_OUTPUT not set\n")
		os.Exit(1)
	}
	fmt.Print("databaseSchema")
	fmt.Print(string(schemaJSON))

	output := fmt.Sprintf("sql-queries=%s\nqueries-diff=%s\nschema=%s\nschema-diff=%s\n",
		escapeMultiline(string(body)),
		escapeMultiline(queryDiff),
		escapeMultiline(string(schemaJSON)),
		escapeMultiline(schemaDiff))
	if err := os.WriteFile(outputPath, []byte(output), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write GITHUB_OUTPUT: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Successfully wrote queries, diff, and schema to GITHUB_OUTPUT.")
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
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to list artifacts: %v\n", err)
		return ""
	}
	defer resp.Body.Close()

	type Artifact struct {
		Name        string `json:"name"`
		ArchiveURL  string `json:"archive_download_url"`
		CreatedAt   string `json:"created_at"`
		WorkflowRun struct {
			HeadBranch string `json:"head_branch"`
		} `json:"workflow_run"`
	}
	type ArtifactsResponse struct {
		Artifacts []Artifact `json:"artifacts"`
	}

	var artifactsResp ArtifactsResponse
	if err := json.NewDecoder(resp.Body).Decode(&artifactsResp); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to decode artifact list: %v\n", err)
		return ""
	}

	var candidates []Artifact
	for _, a := range artifactsResp.Artifacts {
		if a.WorkflowRun.HeadBranch == "main" && strings.Contains(strings.ToLower(a.Name), name) {
			candidates = append(candidates, a)
		}
	}

	if len(candidates) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No suitable baseline artifact from main branch found\n")
		return ""
	}

	sort.Slice(candidates, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, candidates[i].CreatedAt)
		tj, _ := time.Parse(time.RFC3339, candidates[j].CreatedAt)
		return ti.After(tj)
	})

	latest := candidates[0]
	fmt.Fprintf(os.Stderr, "Found baseline artifact: %s (created at: %s)\n", latest.Name, latest.CreatedAt)

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
		if file.Name == "queries.json" || file.Name == "schema.json" {
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

// Compute diff: return queries that exist in current but not in baseline
func generateQueryDiff(currentStr string, baselineStr string) string {
	var current, baseline []Query
	json.Unmarshal([]byte(currentStr), &current)
	json.Unmarshal([]byte(baselineStr), &baseline)

	baselineMap := map[string]bool{}
	for _, q := range baseline {
		baselineMap[q.Query] = true
	}

	var newQueries []Query
	for _, q := range current {
		if !baselineMap[q.Query] {
			newQueries = append(newQueries, q)
		}
	}

	diffBytes, _ := json.Marshal(newQueries)
	return string(diffBytes)
}

func generateSchemaDiff(currentStr string, baselineStr string) string {
	var current, baseline []DatabaseSchema
	json.Unmarshal([]byte(currentStr), &current)
	json.Unmarshal([]byte(baselineStr), &baseline)

	diff := SchemaDiff{
		ModifiedTables: make(map[string]map[string]TableDiff),
	}

	// Create maps for easier lookup
	baselineDBs := make(map[string]DatabaseSchema)
	currentDBs := make(map[string]DatabaseSchema)

	for _, db := range baseline {
		baselineDBs[db.Database] = db
	}
	for _, db := range current {
		currentDBs[db.Database] = db
	}

	// Find added/removed databases
	for dbName := range currentDBs {
		if _, exists := baselineDBs[dbName]; !exists {
			diff.AddedDatabases = append(diff.AddedDatabases, dbName)
		}
	}
	for dbName := range baselineDBs {
		if _, exists := currentDBs[dbName]; !exists {
			diff.RemovedDatabases = append(diff.RemovedDatabases, dbName)
		}
	}

	// Compare tables in each database
	for dbName, currentDB := range currentDBs {
		baselineDB, exists := baselineDBs[dbName]
		if !exists {
			continue // Already handled in added databases
		}

		tableDiffs := make(map[string]TableDiff)

		// Check for added/modified tables
		for tableName, currentTable := range currentDB.Tables {
			baselineTable, tableExists := baselineDB.Tables[tableName]

			if !tableExists {
				// New table
				tableDiffs[tableName] = TableDiff{Added: true}
				continue
			}

			// Compare existing table
			tableDiff := TableDiff{
				Columns:     make(map[string]ColumnDiff),
				Indexes:     []IndexDiff{},
				Constraints: []ConstraintDiff{},
			}

			// Check schema changes
			if currentTable.Schema != baselineTable.Schema {
				tableDiff.SchemaChange = currentTable.Schema
			}

			// Compare columns
			currentColumns := make(map[string]ColumnSchema)
			baselineColumns := make(map[string]ColumnSchema)

			for _, col := range currentTable.Columns {
				currentColumns[col.Name] = col
			}
			for _, col := range baselineTable.Columns {
				baselineColumns[col.Name] = col
			}

			for colName, currentCol := range currentColumns {
				if baselineCol, exists := baselineColumns[colName]; !exists {
					tableDiff.Columns[colName] = ColumnDiff{Added: true}
				} else {
					// Check for column modifications
					colDiff := ColumnDiff{}
					if currentCol.Type != baselineCol.Type {
						colDiff.TypeChanged = currentCol.Type
					}
					if currentCol.Nullable != baselineCol.Nullable {
						colDiff.NullChanged = true
					}
					if colDiff.TypeChanged != "" || colDiff.NullChanged {
						tableDiff.Columns[colName] = colDiff
					}
				}
			}

			// Check for removed columns
			for colName := range baselineColumns {
				if _, exists := currentColumns[colName]; !exists {
					tableDiff.Columns[colName] = ColumnDiff{Removed: true}
				}
			}

			// Compare indexes
			currentIndexDefs := make(map[string]bool)
			baselineIndexDefs := make(map[string]bool)

			for _, idx := range currentTable.Indexes {
				currentIndexDefs[idx.Definition] = true
			}
			for _, idx := range baselineTable.Indexes {
				baselineIndexDefs[idx.Definition] = true
			}

			// Find changed indexes
			for _, idx := range currentTable.Indexes {
				if !baselineIndexDefs[idx.Definition] {
					tableDiff.Indexes = append(tableDiff.Indexes, IndexDiff{New: idx.Definition})
				}
			}
			for _, idx := range baselineTable.Indexes {
				if !currentIndexDefs[idx.Definition] {
					tableDiff.Indexes = append(tableDiff.Indexes, IndexDiff{Old: idx.Definition})
				}
			}

			// Compare constraints
			currentConstraints := make(map[string]ConstraintSchema)
			baselineConstraints := make(map[string]ConstraintSchema)

			for _, cons := range currentTable.Constraints {
				currentConstraints[cons.Name] = cons
			}
			for _, cons := range baselineTable.Constraints {
				baselineConstraints[cons.Name] = cons
			}

			// Find added/removed/modified constraints
			for name, cons := range currentConstraints {
				if _, exists := baselineConstraints[name]; !exists {
					tableDiff.Constraints = append(tableDiff.Constraints, ConstraintDiff{
						Name:    name,
						Added:   true,
						Type:    cons.Type,
						Columns: cons.Columns,
					})
				}
			}
			for name, cons := range baselineConstraints {
				if _, exists := currentConstraints[name]; !exists {
					tableDiff.Constraints = append(tableDiff.Constraints, ConstraintDiff{
						Name:    name,
						Removed: true,
						Type:    cons.Type,
						Columns: cons.Columns,
					})
				}
			}

			// Only add table diff if there are actual changes
			if len(tableDiff.Columns) > 0 || len(tableDiff.Indexes) > 0 ||
				len(tableDiff.Constraints) > 0 || tableDiff.SchemaChange != "" {
				tableDiffs[tableName] = tableDiff
			}
		}

		// Check for removed tables
		for tableName := range baselineDB.Tables {
			if _, exists := currentDB.Tables[tableName]; !exists {
				tableDiffs[tableName] = TableDiff{Removed: true}
			}
		}

		if len(tableDiffs) > 0 {
			diff.ModifiedTables[dbName] = tableDiffs
		}
	}

	diffBytes, _ := json.Marshal(diff)
	return string(diffBytes)
}

// Escape multiline output for GitHub output file
func escapeMultiline(input string) string {
	s := strings.ReplaceAll(input, "%", "%25")
	s = strings.ReplaceAll(s, "\n", "%0A")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return s
}
