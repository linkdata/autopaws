package hid

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// textToken keeps tags separate from literal characters, including U+0001.
// position is the zero-based rune index in the original input.
type textToken struct {
	text     string
	position int
	tag      bool
}

func tokenize(text string, raw bool) []textToken {
	characters := []rune(text)
	tokens := make([]textToken, 0, len(characters))
	for i := 0; i < len(characters); i++ {
		character := characters[i]
		position := i
		if raw {
			if character == '\r' {
				character = '\n'
				if i+1 < len(characters) && characters[i+1] == '\n' {
					i++
				}
			}
		} else if character == '{' || character == '}' {
			if i+1 < len(characters) && characters[i+1] == character {
				i++
			} else if character == '{' {
				if offset := slices.Index(characters[i+1:], '}'); offset >= 0 {
					end := i + 1 + offset
					candidate := string(characters[i+1 : end])
					if validTag(candidate) {
						tokens = append(tokens, textToken{text: candidate, position: position, tag: true})
						i = end
						continue
					}
				}
			}
		}
		tokens = append(tokens, textToken{text: string(character), position: position})
	}
	return tokens
}

func validTag(tag string) bool {
	parts := strings.Split(tag, "+")
	for _, name := range parts[:len(parts)-1] {
		if _, ok := Modifier(strings.TrimSpace(name)); !ok {
			return false
		}
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	_, special := SpecialKey(key)
	return special || utf8.RuneCountInString(key) == 1
}
