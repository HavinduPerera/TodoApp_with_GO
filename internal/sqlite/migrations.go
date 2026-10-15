package sqlite

import "embed"

// migrationsFS holds every migrations/*.sql file inside the binary. The embed
// directive is resolved at compile time relative to this file's package
// directory (a pattern cannot reach outside it), so the binary carries its own
// schema history and never looks for SQL files on disk.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS
