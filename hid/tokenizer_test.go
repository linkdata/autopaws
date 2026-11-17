package hid

import (
	"reflect"
	"strings"
	"testing"
)

func TestPythonTokenizerGoldens(t *testing.T) {
	var golden struct {
		Tokenizer []struct {
			Name   string   `json:"name"`
			Input  string   `json:"input"`
			Tokens []string `json:"tokens"`
		} `json:"tokenizer"`
		ValidTags []struct {
			Name  string `json:"name"`
			Input string `json:"input"`
			Valid bool   `json:"valid"`
		} `json:"valid_tags"`
	}
	readJSON(t, "python_tokenizer.json", &golden)
	if len(golden.Tokenizer) != 34 || len(golden.ValidTags) != 15 {
		t.Fatalf("incomplete Python fixture: %d tokenizer, %d tag cases", len(golden.Tokenizer), len(golden.ValidTags))
	}
	for _, vector := range golden.Tokenizer {
		t.Run(vector.Name, func(t *testing.T) {
			got := tokenize(vector.Input, false)
			if len(got) != len(vector.Tokens) {
				t.Fatalf("tokens = %v; want %q", got, vector.Tokens)
			}
			for i, want := range vector.Tokens {
				// Python marks tags with U+0001. A literal marker is a
				// one-rune token, and must stay literal in the Go port.
				tag := strings.HasPrefix(want, "\x01") && len(want) > 1
				if tag {
					want = want[1:]
				}
				if got[i].text != want || got[i].tag != tag {
					t.Errorf("token %d = %+v; want text %q, tag %v", i, got[i], want, tag)
				}
			}
		})
	}
	for _, vector := range golden.ValidTags {
		if got := validTag(vector.Input); got != vector.Valid {
			t.Errorf("validTag(%q) = %v; want %v", vector.Input, got, vector.Valid)
		}
	}
}

func TestTokenizerPositionsAndRawMode(t *testing.T) {
	for _, vector := range []struct {
		input string
		raw   bool
		want  []textToken
	}{
		{"å{{{shift+å}}}x", false, []textToken{
			{text: "å", position: 0},
			{text: "{", position: 1},
			{text: "shift+å", position: 3, tag: true},
			{text: "}", position: 12},
			{text: "x", position: 14},
		}},
		{"a\r\nb\rc\n", true, []textToken{
			{text: "a", position: 0},
			{text: "\n", position: 1},
			{text: "b", position: 3},
			{text: "\n", position: 4},
			{text: "c", position: 5},
			{text: "\n", position: 6},
		}},
		{"{{enter}}\\n\r", true, []textToken{
			{text: "{", position: 0},
			{text: "{", position: 1},
			{text: "e", position: 2},
			{text: "n", position: 3},
			{text: "t", position: 4},
			{text: "e", position: 5},
			{text: "r", position: 6},
			{text: "}", position: 7},
			{text: "}", position: 8},
			{text: "\\", position: 9},
			{text: "n", position: 10},
			{text: "\n", position: 11},
		}},
		{"\x01{\x01}", false, []textToken{
			{text: "\x01", position: 0}, {text: "\x01", position: 1, tag: true},
		}},
	} {
		if got := tokenize(vector.input, vector.raw); !reflect.DeepEqual(got, vector.want) {
			t.Errorf("tokenize(%q, %v) = %+v; want %+v", vector.input, vector.raw, got, vector.want)
		}
	}
	for _, tag := range []string{"", "ctrl+", "hyper+a", "0x100", "ctrl++"} {
		if validTag(tag) {
			t.Errorf("invalid tag %q accepted", tag)
		}
	}
}

func FuzzTokenizer(f *testing.F) {
	for _, seed := range []string{"", "hello{{world}}", "{bad{enter}}", "åäö{shift+å}", "\x01x", "{{{{}}}}", "{unknown}}", "\xff{{"} {
		f.Add(seed)
	}
	braces := strings.NewReplacer("{{", "{", "}}", "}")
	f.Fuzz(func(t *testing.T, input string) {
		var joined strings.Builder
		for _, token := range tokenize(input, false) {
			if token.tag {
				return
			}
			joined.WriteString(token.text)
		}
		// Rune scanning replaces invalid UTF-8 consistently before comparison.
		want := braces.Replace(string([]rune(input)))
		if joined.String() != want {
			t.Fatalf("literal tokens = %q; want %q", joined.String(), want)
		}
	})
}
