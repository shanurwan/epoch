package migrations

import _ "embed"

//go:embed 001_schema.sql
var Schema string
