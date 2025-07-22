package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	queryDiff := diffQueries(string(body), queriesBaseline)

	// Just use the new queries without plans
	queryDiffJSON, err := json.Marshal(queryDiff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal query diff: %v\n", err)
		os.Exit(1)
	}

	// Generate schema SQL
	databaseSchema := GetDatabaseSchema(os.Getenv("DB_CONNECTION_STRING"))
	schemaJSON, err := json.Marshal(databaseSchema)
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

	schemaDiff := CompareSchema(databaseSchema, baselineSchema)
	schemaDiffJSON, err := json.Marshal(schemaDiff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal schema diff: %v\n", err)
		os.Exit(1)
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
	apiURL = fmt.Sprintf("%s?per_page=100&name=%s", apiURL, url.QueryEscape(name))
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

func diffQueries(current, baseline string) []Query {
	var currentQueries, baselineQueries []Query
	json.Unmarshal([]byte(current), &currentQueries)
	json.Unmarshal([]byte(baseline), &baselineQueries)

	fmt.Fprintf(os.Stderr, "Debug: Current queries count: %d\n", len(currentQueries))
	fmt.Fprintf(os.Stderr, "Debug: Baseline queries count: %d\n", len(baselineQueries))
	fmt.Fprintf(os.Stderr, "Debug: Baseline string length: %d\n", len(baseline))

	// Create a map of baseline queries for quick lookup
	baselineMap := make(map[string]bool)
	for _, q := range baselineQueries {
		baselineMap[q.Query] = true
	}

	fmt.Fprintf(os.Stderr, "Debug: Baseline map size: %d\n", len(baselineMap))

	// Find new queries
	var newQueries []Query
	for _, q := range currentQueries {
		if !baselineMap[q.Query] {
			newQueries = append(newQueries, q)
		}
	}

	fmt.Fprintf(os.Stderr, "Debug: New queries count: %d\n", len(newQueries))
	return newQueries
}
