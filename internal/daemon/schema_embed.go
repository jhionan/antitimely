package daemon

// Schema is now passed explicitly to Run() — see main.go. The previous
// package-level SchemaSQL/SetSchema pair was a global written after init,
// which the race detector would flag if Run were ever called concurrently
// with the initializer.
