// Package migrations embeds the SQL schema migrations so a compiled binary
// can migrate a database without the repository being present.
package migrations

import "embed"

// FS holds every .sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS

// Dir is the path inside FS that holds the migrations.
const Dir = "."
