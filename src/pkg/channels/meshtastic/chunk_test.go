// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func collectChunks(t *testing.T, text string, soft int, reply uint32, direct bool) []string {
	t.Helper()
	_, total, oversized, err := chunkCount(text, soft, reply, direct)
	if err != nil {
		t.Fatal(err)
	}
	if oversized {
		return nil
	}
	var out []string
	n, err := scanChunks(text, soft, total, maxTextChunks, reply, direct, func(s string) error {
		out = append(out, s)
		return nil
	})
	if err != nil || n != total {
		t.Fatalf("scan=(%d,%v), total=%d", n, err, total)
	}
	return out
}

func TestChunkingUTF8AndNumbering(t *testing.T) {
	for _, text := range []string{
		"one two three four five six",
		"alpha beta gamma delta epsilon",
		"one 😀 two 🌍 three",
		strings.Repeat("😀", 90),
		"https://example.invalid/" + strings.Repeat("long", 80),
	} {
		chunks := collectChunks(t, text, 24, 123, true)
		if len(chunks) == 0 {
			t.Fatalf("no chunks for %q", text)
		}
		var rebuilt strings.Builder
		for i, chunk := range chunks {
			if !utf8.ValidString(chunk) || !fitsPhysical(chunk, 123, true) {
				t.Fatalf("invalid physical chunk %q", chunk)
			}
			prefix := visiblePrefix(i+1, len(chunks))
			if !strings.HasPrefix(chunk, prefix) {
				t.Fatalf("chunk %d prefix %q, want %q", i, chunk, prefix)
			}
			body := strings.TrimPrefix(chunk, prefix)
			if rebuilt.Len() != 0 && !strings.HasPrefix(text, rebuilt.String()+body) {
				rebuilt.WriteByte(' ')
			}
			rebuilt.WriteString(body)
		}
		// Normalization collapses whitespace, but hard-split tokens concatenate.
		want := strings.Join(strings.Fields(text), " ")
		if rebuilt.String() != want {
			t.Fatalf("reconstructed %q, want %q", rebuilt.String(), want)
		}
	}
}

func TestChunkPhysicalBoundariesAndOversize(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, reply := range []uint32{0, 0xffffffff} {
			best := 0
			for n := 1; n <= dataPayloadLimit; n++ {
				if fitsPhysical(strings.Repeat("a", n), reply, direct) {
					best = n
				}
			}
			if best == 0 || !fitsPhysical(strings.Repeat("a", best), reply, direct) || fitsPhysical(strings.Repeat("a", best+1), reply, direct) {
				t.Fatalf("bad boundary direct=%v reply=%d best=%d", direct, reply, best)
			}
		}
	}
	text := strings.Repeat("word ", 1000)
	count, total, oversized, err := chunkCount(text, 32, 9, false)
	if err != nil || !oversized || count != 1 || total != 1 {
		t.Fatalf("oversize=(%d,%d,%v,%v)", count, total, oversized, err)
	}
}

func TestEmptyAndSingleChunkHaveNoPrefix(t *testing.T) {
	if got := collectChunks(t, "", 200, 0, false); len(got) != 0 {
		t.Fatalf("empty input emitted %q", got)
	}
	got := collectChunks(t, "hello", 200, 0, false)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("single chunk = %q", got)
	}
}
