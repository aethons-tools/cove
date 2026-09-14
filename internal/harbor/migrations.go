package harbor

import "embed"

// migrationFiles holds the ordered SQL migrations applied at PostgresStore
// startup (see pgstore.go migrate()). File names are "<zero-padded number>_<desc>.sql";
// the leading number is the migration version.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS
