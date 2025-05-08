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
	queriesBaseline := getArtifactFromMain("sql-queries")

	// Generate JSON diff
	queryDiff := diffQueries(string(body), queriesBaseline)

	// Generate schema SQL
	connStr := "host=postgres port=5432 user=postgres password=postgres dbname=postgres sslmode=disable"
	databaseSchema := GetDatabaseSchema(connStr)
	schemaJSON, err := json.Marshal(databaseSchema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal database schema: %v\n", err)
		os.Exit(1)
	}

	schemaBaseline := getArtifactFromMain("full-schema")
	fmt.Print("schemaBaseline:")
	fmt.Print(schemaBaseline)

	// Parse the baseline schema from JSON string
	var baselineSchema []DatabaseSchema
	if err := json.Unmarshal([]byte(schemaBaseline), &baselineSchema); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse baseline schema: %v\n", err)
		os.Exit(1)
	}

	schemaDiff := CompareSchema(databaseSchema, baselineSchema)
	schemaDiffJSON, err := json.Marshal(schemaDiff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal schema diff: %v\n", err)
		os.Exit(1)
	}

	// Write to GITHUB_OUTPUT
	outputPath := os.Getenv("GITHUB_OUTPUT")
	if outputPath == "" {
		fmt.Fprintf(os.Stderr, "GITHUB_OUTPUT not set\n")
		os.Exit(1)
	}

	output := fmt.Sprintf("sql-queries=%s\nqueries-diff=%s\nschema=%s\nschema-diff=%s\n",
		escapeMultiline(string(body)),
		escapeMultiline(queryDiff),
		escapeMultiline(string(schemaJSON)),
		escapeMultiline(string(schemaDiffJSON)))
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
	apiURL = fmt.Sprintf("%s?per_page=100&name=%s", apiURL, name)
	req, _ = http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Authorization", "token "+token)

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

	fmt.Fprintf(os.Stderr, "Found %d total artifacts\n", len(artifactsResp.Artifacts))

	var candidates []Artifact
	for _, a := range artifactsResp.Artifacts {
		fmt.Fprintf(os.Stderr, "Artifact: %s (branch: %s)\n", a.Name, a.WorkflowRun.HeadBranch)
		// Check if this is a main branch artifact
		if a.WorkflowRun.HeadBranch == "main" {
			fmt.Fprintf(os.Stderr, "  - Found main branch artifact\n")
			// Check if name matches what we're looking for
			if strings.Contains(strings.ToLower(a.Name), strings.ToLower(name)) {
				fmt.Fprintf(os.Stderr, "  - Name matches '%s'\n", name)
				candidates = append(candidates, a)
			} else {
				fmt.Fprintf(os.Stderr, "  - Name doesn't match '%s'\n", name)
			}
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
	fmt.Fprintf(os.Stderr, "Selected artifact: %s (created at: %s)\n", latest.Name, latest.CreatedAt)

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

func diffQueries(current, baseline string) string {
	var currentQueries, baselineQueries []Query
	json.Unmarshal([]byte(current), &currentQueries)
	json.Unmarshal([]byte(baseline), &baselineQueries)

	// Create a map of baseline queries for quick lookup
	baselineMap := make(map[string]bool)
	for _, q := range baselineQueries {
		baselineMap[q.Query] = true
	}

	// Find new queries
	var newQueries []Query
	for _, q := range currentQueries {
		if !baselineMap[q.Query] {
			newQueries = append(newQueries, q)
		}
	}

	diffBytes, _ := json.Marshal(newQueries)
	return string(diffBytes)
}

// Escape multiline output for GitHub output file
func escapeMultiline(input string) string {
	s := strings.ReplaceAll(input, "%", "%25")
	s = strings.ReplaceAll(s, "\n", "%0A")
	s = strings.ReplaceAll(s, "\r", "%0D")
	return s
}
