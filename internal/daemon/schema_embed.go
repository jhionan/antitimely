package daemon

// SchemaSQL is populated by main.go via SetSchema before Run is called.
// We can't //go:embed across module-internal boundaries (schema.sql lives at
// repo root, two directories up from this package), so embedding is done in
// the root package and injected here.
var SchemaSQL string

func SetSchema(s string) { SchemaSQL = s }
