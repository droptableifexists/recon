package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
)

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

type SchemaDiff []TableChanges

type TableChanges struct {
	Database string         `json:"database"`
	Schema   string         `json:"schema"`
	Table    string         `json:"table"`
	Changes  []SchemaChange `json:"changes"`
}

type SchemaChange struct {
	Type   string                 `json:"type"` // "column_added", "column_removed", "column_modified", "index_added", "index_removed", "constraint_added", "constraint_removed"
	Change map[string]interface{} `json:"change"`
}

func GetDatabaseSchema(connectionString string) []DatabaseSchema {
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return []DatabaseSchema{}
	}
	defer db.Close()

	databases, err := getDatabases(connectionString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get databases: %v\n", err)
		return []DatabaseSchema{}
	}

	databaseSchemas := []DatabaseSchema{}
	for _, database := range databases {
		tables, err := getTables(connectionString, database)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get tables: %v\n", err)
			return []DatabaseSchema{}
		}
		databaseSchemas = append(databaseSchemas, DatabaseSchema{
			Database: database,
			Tables:   tables,
		})
	}
	return databaseSchemas
}

func getDatabases(connectionString string) ([]string, error) {
	connectionString = fmt.Sprintf("%s dbname=postgres", connectionString)
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if rows, err := db.Query("SELECT datname FROM pg_database WHERE datistemplate = false"); err != nil {
		return nil, err
	} else {
		defer rows.Close()
		databases := []string{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			databases = append(databases, name)
		}
		return databases, nil
	}
}

func getTables(connectionString string, database string) (map[string]TableSchema, error) {
	connectionString = fmt.Sprintf("%s dbname=%s", connectionString, database)
	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT
			table_schema, table_name, column_name, data_type, is_nullable
		FROM
			information_schema.columns
		WHERE
			table_schema NOT IN ('pg_catalog', 'information_schema')`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tableSchemas := make(map[string]TableSchema)
	for rows.Next() {
		var schema string
		var name string
		var columnName string
		var dataType string
		var isNullable string
		if err := rows.Scan(&schema, &name, &columnName, &dataType, &isNullable); err != nil {
			return nil, err
		}
		if t, ok := tableSchemas[name]; !ok {
			tableSchemas[name] = TableSchema{
				Name:    name,
				Schema:  schema,
				Columns: []ColumnSchema{},
			}
		} else {
			t.Columns = append(t.Columns, ColumnSchema{
				Name:     columnName,
				Type:     dataType,
				Nullable: isNullable == "YES",
			})
			t.Indexes, err = getIndexes(db, schema, name)
			if err != nil {
				return nil, err
			}
			t.Constraints, err = getConstraints(db, schema, name)
			if err != nil {
				return nil, err
			}
			tableSchemas[name] = t
		}
	}
	return tableSchemas, nil
}

func getIndexes(db *sql.DB, schema string, table string) ([]IndexSchema, error) {
	rows, err := db.Query(`SELECT
		indexdef
	FROM
		pg_indexes
	WHERE
		tablename = $1 AND schemaname = $2`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	indexes := []IndexSchema{}
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			return nil, err
		}
		indexes = append(indexes, IndexSchema{
			Definition: definition,
		})
	}
	return indexes, nil
}

func getConstraints(db *sql.DB, schema string, table string) ([]ConstraintSchema, error) {
	rows, err := db.Query(`SELECT
		pg_get_constraintdef(conoid) AS constraint_definition
	FROM
		pg_constraint
	WHERE
		conrelid::regclass = $1 AND
		connamespace::regnamespace = $2`, table, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	constraints := []ConstraintSchema{}
	for rows.Next() {
		var constraintDefinition string
		if err := rows.Scan(&constraintDefinition); err != nil {
			return nil, err
		}
		constraints = append(constraints, ConstraintSchema{
			Definition: constraintDefinition,
		})
	}
	return constraints, nil
}

func CompareSchema(current, baseline []DatabaseSchema) SchemaDiff {
	var tableChanges []TableChanges
	old := getDatabaseSchemaMap(baseline)
	new := getDatabaseSchemaMap(current)

	// Compare tables in each database
	for dbName, currentDB := range new {
		baselineDB, exists := old[dbName]
		if !exists {
			continue
		}

		// Check for added/modified tables
		for tableName, currentTable := range currentDB.Tables {
			baselineTable, tableExists := baselineDB.Tables[tableName]

			if !tableExists {
				// New table
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   currentTable.Schema,
					Table:    tableName,
					Changes: []SchemaChange{
						{
							Type: "table_added",
							Change: map[string]interface{}{
								"name": tableName,
							},
						},
					},
				})
				continue
			}

			// Compare existing table
			var changes []SchemaChange

			// Compare columns
			currentColumns := make(map[string]ColumnSchema)
			baselineColumns := make(map[string]ColumnSchema)

			for _, col := range currentTable.Columns {
				currentColumns[col.Name] = col
			}
			for _, col := range baselineTable.Columns {
				baselineColumns[col.Name] = col
			}

			// Find new and modified columns
			for colName, currentCol := range currentColumns {
				if baselineCol, exists := baselineColumns[colName]; !exists {
					changes = append(changes, SchemaChange{
						Type: "column_added",
						Change: map[string]interface{}{
							"name":       colName,
							"definition": fmt.Sprintf("%s %s", currentCol.Type, map[bool]string{true: "NULL", false: "NOT NULL"}[currentCol.Nullable]),
						},
					})
				} else {
					// Check for column modifications
					if currentCol.Type != baselineCol.Type {
						changes = append(changes, SchemaChange{
							Type: "column_modified",
							Change: map[string]interface{}{
								"name":   colName,
								"old":    baselineCol.Type,
								"new":    currentCol.Type,
								"detail": "type",
							},
						})
					}
					if currentCol.Nullable != baselineCol.Nullable {
						changes = append(changes, SchemaChange{
							Type: "column_modified",
							Change: map[string]interface{}{
								"name":   colName,
								"old":    map[bool]string{true: "NULL", false: "NOT NULL"}[baselineCol.Nullable],
								"new":    map[bool]string{true: "NULL", false: "NOT NULL"}[currentCol.Nullable],
								"detail": "nullability",
							},
						})
					}
				}
			}

			// Find removed columns
			for colName := range baselineColumns {
				if _, exists := currentColumns[colName]; !exists {
					changes = append(changes, SchemaChange{
						Type: "column_removed",
						Change: map[string]interface{}{
							"name": colName,
							"old":  fmt.Sprintf("%s %s", baselineColumns[colName].Type, map[bool]string{true: "NULL", false: "NOT NULL"}[baselineColumns[colName].Nullable]),
						},
					})
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
					changes = append(changes, SchemaChange{
						Type: "index_added",
						Change: map[string]interface{}{
							"name":       extractIndexName(idx.Definition),
							"definition": idx.Definition,
						},
					})
				}
			}
			for _, idx := range baselineTable.Indexes {
				if !currentIndexDefs[idx.Definition] {
					changes = append(changes, SchemaChange{
						Type: "index_removed",
						Change: map[string]interface{}{
							"name": extractIndexName(idx.Definition),
							"old":  idx.Definition,
						},
					})
				}
			}

			// Compare constraints
			currentConstraintDefs := make(map[string]bool)
			baselineConstraintDefs := make(map[string]bool)

			for _, c := range currentTable.Constraints {
				currentConstraintDefs[c.Definition] = true
			}
			for _, c := range baselineTable.Constraints {
				baselineConstraintDefs[c.Definition] = true
			}

			// Find changed constraints
			for _, c := range currentTable.Constraints {
				if !baselineConstraintDefs[c.Definition] {
					changes = append(changes, SchemaChange{
						Type: "constraint_added",
						Change: map[string]interface{}{
							"definition": c.Definition,
						},
					})
				}
			}
			for _, bl := range baselineTable.Constraints {
				if !currentConstraintDefs[bl.Definition] {
					changes = append(changes, SchemaChange{
						Type: "constraint_removed",
						Change: map[string]interface{}{
							"definition": bl.Definition,
						},
					})
				}
			}

			if len(changes) > 0 {
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   currentTable.Schema,
					Table:    tableName,
					Changes:  changes,
				})
			}
		}

		// Check for removed tables
		for tableName := range baselineDB.Tables {
			if _, exists := currentDB.Tables[tableName]; !exists {
				tableChanges = append(tableChanges, TableChanges{
					Database: dbName,
					Schema:   baselineDB.Tables[tableName].Schema,
					Table:    tableName,
					Changes: []SchemaChange{
						{
							Type: "table_removed",
							Change: map[string]interface{}{
								"name": tableName,
							},
						},
					},
				})
			}
		}
	}

	return SchemaDiff(tableChanges)
}

func extractIndexName(definition string) string {
	// Simple extraction of index name from CREATE INDEX statement
	parts := strings.Split(definition, " ")
	if len(parts) >= 3 {
		return parts[2]
	}
	return definition
}

func getDatabaseSchemaMap(databases []DatabaseSchema) map[string]DatabaseSchema {
	databaseSchemaMap := map[string]DatabaseSchema{}
	for _, database := range databases {
		databaseSchemaMap[database.Database] = database
	}
	return databaseSchemaMap
}
