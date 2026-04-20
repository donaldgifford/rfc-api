// Package db exposes the on-disk SQL migration files via an embed.FS
// so commands (cmd/rfc-api/migrate.go) and runtime code can reach them
// without touching the filesystem at runtime. Keeping the physical
// files at db/migrations/*.sql preserves the operator-facing layout
// documented in IMPL-0002 while letting the binary carry its own
// migrations.
package db

import "embed"

// Migrations is the forward/backward SQL migration set shipped with
// the binary. Files are named `NNNN_name.{up,down}.sql` per
// golang-migrate's source.iofs convention.
//
//go:embed migrations/*.sql
var Migrations embed.FS
