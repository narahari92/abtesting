// Package migrations embeds the SQL schema files applied at startup.
package migrations

import "embed"

// Files contains NNNN_name.sql files applied in lexical order.
//
//go:embed *.sql
var Files embed.FS
