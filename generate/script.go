package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

const (
	mysqlHost     = "localhost"
	mysqlPort     = "3306"
	mysqlUser     = "root"
	mysqlPassword = "root"
	mysqlDb       = "testdb"
)

func main() {
	dataSourceName := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", mysqlUser, mysqlPassword, mysqlHost, mysqlPort, mysqlDb)
	targetDB, err := sql.Open("mysql", dataSourceName)
	if err != nil {
		log.Fatalf("failed to connect to target DB: %v", err)
	}
	defer targetDB.Close()

	ctx := context.Background()

	generateData(ctx, targetDB)
}

func generateData(ctx context.Context, db *sql.DB) {
	query := "INSERT INTO users (user_id, name) VALUES "
	values := []any{}
	placeholders := []string{}

	for i := 1; i <= 30000; i++ {
		placeholders = append(placeholders, "(?, ?)")
		values = append(values, i, fmt.Sprintf("name-%d", i))
	}

	query += strings.Join(placeholders, ",")
	_, err := db.ExecContext(ctx, query, values...)
	if err != nil {
		log.Printf("error writing batch to new table: %v", err)
	}
}
