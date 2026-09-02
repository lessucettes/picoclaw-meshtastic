// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func mentionAt(s, name string) (start, end int, ok bool) {
	if name == "" {
		return 0, 0, false
	}
	for pos := 0; pos < len(s); {
		r, size := utf8.DecodeRuneInString(s[pos:])
		if r != '@' || (pos > 0 && isWordRune(prevRune(s[:pos]))) {
			pos += size
			continue
		}
		candidateEnd := pos + 1 + len(name)
		if candidateEnd > len(s) || !strings.EqualFold(s[pos+1:candidateEnd], name) {
			pos += size
			continue
		}
		if candidateEnd < len(s) {
			next, _ := utf8.DecodeRuneInString(s[candidateEnd:])
			if isWordRune(next) {
				pos += size
				continue
			}
		}
		return pos, candidateEnd, true
	}
	return 0, 0, false
}

// ownMentionAt prefers the canonical own-node ID even when a short-name mention appears earlier.
func ownMentionAt(s string, ownNode uint32, shortName string) (start, end int, ok bool) {
	if ownNode != 0 {
		if start, end, ok := mentionAt(s, nodeID(ownNode)); ok {
			return start, end, true
		}
	}
	return mentionAt(s, shortName)
}

func prevRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
