package llm

import "unicode"

// EstimateTokens provides a fast, reasonably accurate approximation of the
// number of tokens an LLM prompt will consume. It treats contiguous
// alphanumeric sequences as a single token and counts punctuation characters
// individually. Whitespace characters only act as token boundaries.
func EstimateTokens(input string) int {
	tokens := 0
	inWord := false
	inPunct := false
	for _, r := range input {
		switch {
		case unicode.IsSpace(r):
			inWord = false
			inPunct = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				tokens++
			}
			inWord = true
			inPunct = false
		default:
			if !inPunct {
				tokens++
			}
			inWord = false
			inPunct = true
		}
	}
	return tokens
}
