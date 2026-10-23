// Package db holds sqlc-generated data access code. Queries live in queries/*.sql;
// every query on user data takes user_id explicitly.
package db

//go:generate go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.27.0 generate
