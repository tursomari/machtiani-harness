package llm

import "testing"

func TestEstimateTokensBasic(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		expect int
	}{
		{"empty", "", 0},
		{"single word", "hello", 1},
		{"words and spaces", "hello world", 2},
		{"punctuation", "foo, bar!", 4},
		{"numbers", "123 45", 2},
		{"mixed", "sum = a+b", 5},
		{"newlines", "alpha\n beta\tgamma", 3},
	}

	for _, tc := range cases {
		got := EstimateTokens(tc.text)
		if got != tc.expect {
			t.Errorf("%s: expected %d tokens, got %d", tc.name, tc.expect, got)
		}
	}
}
