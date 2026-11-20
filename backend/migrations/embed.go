// Package migrations embeds the SQL migrations (golang-migrate naming: NNNNNN_name.up.sql / .down.sql)
// so the API, tests and tools can apply them without reading from disk.
// Files under _legacy_unapplied/ are reference only and are not embedded.
package migrations

import "embed"

// FS holds every *.up.sql and *.down.sql file in this directory.
//
//go:embed *.up.sql *.down.sql
var FS embed.FS
