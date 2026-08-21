package conversation

// IdentifyNewMessages returns the append-only tail present in next but not in
// previous. Persisted conversations are published as complete atomic rewrites,
// so a message's stable identity is its slice position within the session.
// Re-reading the same snapshot, or briefly observing an older snapshot, cannot
// cause already-seen history to be returned again.
func IdentifyNewMessages(previous, next []Message) []Message {
	if len(next) <= len(previous) {
		return nil
	}
	return append([]Message(nil), next[len(previous):]...)
}
