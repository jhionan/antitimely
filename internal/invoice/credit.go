package invoice

// ApplyCredit returns how much of an outstanding advance credit this invoice
// consumes. Goodwill discount is applied first and the credit fills whatever
// room remains, so goodwill+credit can never exceed the line total and trip
// BuildDoc's validation.
//
// The lower clamp is load-bearing, not defensive: a negative balance is
// reachable (delete an advance after a partial drawdown) and an unclamped
// negative would make BuildDoc reject every subsequent invoice for that
// company, with no route back through the CLI.
func ApplyCredit(creditCents, lineTotalCents, goodwillCents int64) int64 {
	room := lineTotalCents - goodwillCents
	if room < 0 {
		room = 0
	}
	applied := creditCents
	if applied > room {
		applied = room
	}
	if applied < 0 {
		applied = 0
	}
	return applied
}
