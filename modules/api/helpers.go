package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

func queryToJSON(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query error: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}
	numColumns := len(columns)

	values := make([]any, numColumns)
	valuePtrs := make([]any, numColumns)
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	results := []map[string]any{}
	for rows.Next() {
		// Scan row into value pointers
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Create a map for the row
		rowMap := make(map[string]any, numColumns)
		for i, col := range columns {
			rowMap[col] = values[i]
		}

		// Append the row map to the results slice
		results = append(results, rowMap)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}
	return results, nil
}

func jsonToTable(db *sql.DB, tableName string, keyColumn string, keyValue any, jsonData []byte) error {
	var row map[string]any
	if err := json.Unmarshal(jsonData, &row); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	row[keyColumn] = keyValue

	columns := make([]string, 0, len(row))
	placeholders := make([]string, 0, len(row))
	updates := make([]string, 0, len(row))
	values := make([]any, 0, len(row))

	for col, value := range row {
		columns = append(columns, col)
		placeholders = append(placeholders, "?")
		values = append(values, value)
		if col != keyColumn {
			updates = append(updates, fmt.Sprintf("%s = EXCLUDED.%s", col, col))
		}
	}

	updateClause := "DO NOTHING"
	if len(updates) > 0 {
		updateClause = fmt.Sprintf("DO UPDATE SET %s", strings.Join(updates, ", "))
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (%s) %s",
		tableName,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
		keyColumn,
		updateClause,
	)

	_, err := db.Exec(query, values...)
	return err
}
