// Package dbenv builds a pgx DSN from the DB_* environment variables
// shared by every OpenTAMS scale-test tool (loader, bench, loadgen) and
// by the main `opentams serve` binary. Centralising it keeps the
// operator contract identical across tools: the same exported env points
// every tool at the same database.
package dbenv

import (
	"fmt"
	"os"
)

// DSN builds a pgx DSN from DB_HOST / DB_PORT / DB_NAME / DB_USER /
// DB_PASSWORD / DB_SSLMODE. DB_PORT defaults to 5432; DB_SSLMODE defaults
// to "require" (managed-Postgres-safe). The four identity vars are
// required; a missing one returns an error naming it.
func DSN() (string, error) {
	host := os.Getenv("DB_HOST")
	if host == "" {
		return "", fmt.Errorf("DB_HOST not set")
	}
	user := os.Getenv("DB_USER")
	if user == "" {
		return "", fmt.Errorf("DB_USER not set")
	}
	pw := os.Getenv("DB_PASSWORD")
	if pw == "" {
		return "", fmt.Errorf("DB_PASSWORD not set")
	}
	name := os.Getenv("DB_NAME")
	if name == "" {
		return "", fmt.Errorf("DB_NAME not set")
	}
	port := os.Getenv("DB_PORT")
	if port == "" {
		port = "5432"
	}
	ssl := os.Getenv("DB_SSLMODE")
	if ssl == "" {
		ssl = "require"
	}
	return fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
		host, port, name, user, pw, ssl), nil
}
