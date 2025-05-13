package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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

type TableChanges struct {
	Database string       `json:"database"`
	Schema   string       `json:"schema"`
	Table    string       `json:"table"`
	Old      *TableSchema `json:"old,omitempty"`
	New      *TableSchema `json:"new,omitempty"`
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
	defaultDatabase := os.Getenv("DEFAULT_DATABASE")
	connectionString = fmt.Sprintf("%s dbname=%s", connectionString, defaultDatabase)
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

	sql := `SELECT
		table_schema, table_name, column_name, data_type, is_nullable
	FROM
		information_schema.columns
	WHERE
		table_schema NOT IN ('pg_catalog', 'information_schema', 'crdb_internal')`

	fmt.Println(sql)
	rows, err := db.Query(sql)
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
		// Use fully qualified table name as the key
		tableKey := fmt.Sprintf("%s.%s", schema, name)
		if t, ok := tableSchemas[tableKey]; !ok {
			tableSchemas[tableKey] = TableSchema{
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
			tableSchemas[tableKey] = t
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
		pg_get_constraintdef(oid) AS constraint_definition
	FROM
		pg_constraint
	WHERE
		conrelid::regclass::text = $1 AND
		connamespace::regnamespace::text = $2`, table, schema)
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

func CompareSchema(current, baseline []DatabaseSchema) []TableChanges {
	var tableChanges []TableChanges
	currentDB := getDatabaseSchemaMap(current)
	baselineDB := getDatabaseSchemaMap(baseline)

	for _, currentDB := range currentDB {
		if baselineDB, exists := baselineDB[currentDB.Database]; !exists {
			continue
		} else {
			for _, currentTable := range currentDB.Tables {
				if baselineTable, exists := baselineDB.Tables[currentTable.Name]; !exists {
					continue
				} else {
					if reflect.DeepEqual(currentTable, baselineTable) {
						continue
					} else {
						jsonCurrent, _ := json.Marshal(currentTable)
						jsonBaseline, _ := json.Marshal(baselineTable)
						fmt.Print("\n currentTable: \n")
						fmt.Print(string(jsonCurrent))
						fmt.Print("\n baselineTable: \n")
						fmt.Print(string(jsonBaseline))
						oldTable := baselineTable
						newTable := currentTable
						tableChanges = append(tableChanges, TableChanges{
							Database: currentDB.Database,
							Schema:   currentTable.Schema,
							Table:    currentTable.Name,
							Old:      &oldTable,
							New:      &newTable,
						})
					}
				}
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
