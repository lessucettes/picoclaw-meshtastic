// SPDX-License-Identifier: GPL-3.0-only

package meshtastic

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	mesh "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

const (
	dataPayloadLimit = 233
	loraPayloadLimit = 255
	meshHeaderBytes  = 16
	pkiReserveBytes  = 12
	maxTextChunks    = 8
)

type tokenScanner struct {
	s  string
	at int
}

func (s *tokenScanner) next() (string, bool) {
	for s.at < len(s.s) {
		r, n := utf8.DecodeRuneInString(s.s[s.at:])
		if !unicode.IsSpace(r) {
			break
		}
		s.at += n
	}
	if s.at == len(s.s) {
		return "", false
	}
	start := s.at
	for s.at < len(s.s) {
		r, n := utf8.DecodeRuneInString(s.s[s.at:])
		if unicode.IsSpace(r) {
			break
		}
		s.at += n
	}
	return s.s[start:s.at], true
}

func visiblePrefix(index, total int) string {
	if total <= 1 {
		return ""
	}
	return fmt.Sprintf("[%d/%d] ", index, total)
}

func fitsPhysical(text string, replyID uint32, direct bool) bool {
	if len(text) == 0 || len(text) > dataPayloadLimit || !utf8.ValidString(text) {
		return false
	}
	zero := uint32(0)
	d := &mesh.Data{Portnum: mesh.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text), ReplyId: replyID, Bitfield: &zero}
	n := proto.Size(d) + meshHeaderBytes
	if direct {
		n += pkiReserveBytes
	}
	return n <= loraPayloadLimit
}

// scanChunks performs both count-only and final passes without retaining tokens
// or future payloads. emit may be nil for a count-only pass.
func scanChunks(text string, soft, total, max int, replyID uint32, direct bool, emit func(string) error) (int, error) {
	if soft <= 0 {
		return 0, fmt.Errorf("soft chunk target must be positive")
	}
	s := tokenScanner{s: text}
	count := 0
	var cur strings.Builder
	emitCur := func() error {
		if cur.Len() == 0 {
			return nil
		}
		count++
		if count > max {
			return nil
		}
		if emit != nil {
			payload := visiblePrefix(count, total) + cur.String()
			if err := emit(payload); err != nil {
				return err
			}
		}
		cur.Reset()
		return nil
	}
	for {
		tok, ok := s.next()
		if !ok {
			break
		}
		if count >= max {
			return max + 1, nil
		}
		prefix := visiblePrefix(count+1, total)
		candidate := tok
		if cur.Len() != 0 {
			candidate = cur.String() + " " + tok
		}
		if len(prefix)+len(candidate) <= soft && fitsPhysical(prefix+candidate, replyID, direct) {
			if cur.Len() != 0 {
				cur.WriteByte(' ')
			}
			cur.WriteString(tok)
			continue
		}
		if cur.Len() != 0 {
			if err := emitCur(); err != nil {
				return count, err
			}
			if count >= max {
				return max + 1, nil
			}
			prefix = visiblePrefix(count+1, total)
		}
		if fitsPhysical(prefix+tok, replyID, direct) {
			cur.WriteString(tok)
			if len(prefix)+len(tok) > soft {
				if err := emitCur(); err != nil {
					return count, err
				}
			}
			continue
		}
		// The token cannot fit a physical frame. Each longest valid prefix is
		// a standalone fragment and the following token starts a new chunk.
		for len(tok) != 0 {
			if count >= max {
				return max + 1, nil
			}
			prefix = visiblePrefix(count+1, total)
			best := 0
			for end := 0; end < len(tok); {
				_, n := utf8.DecodeRuneInString(tok[end:])
				end += n
				if fitsPhysical(prefix+tok[:end], replyID, direct) {
					best = end
				} else {
					break
				}
			}
			if best == 0 {
				return count, fmt.Errorf("numbering prefix leaves no room for one UTF-8 rune")
			}
			cur.WriteString(tok[:best])
			if err := emitCur(); err != nil {
				return count, err
			}
			tok = tok[best:]
		}
	}
	if err := emitCur(); err != nil {
		return count, err
	}
	return count, nil
}

func chunkCount(text string, soft int, replyID uint32, direct bool) (count int, total int, oversized bool, err error) {
	if !utf8.ValidString(text) {
		return 0, 0, false, fmt.Errorf("outbound text is invalid UTF-8")
	}
	count, err = scanChunks(text, soft, 0, maxTextChunks, replyID, direct, nil)
	if err != nil || count == 0 {
		return count, count, false, err
	}
	if count > maxTextChunks {
		return 1, 1, true, nil
	}
	if count == 1 {
		return 1, 1, false, nil
	}
	total = count
	for {
		if total == math.MaxInt {
			return 0, 0, false, fmt.Errorf("chunk count overflow")
		}
		m, scanErr := scanChunks(text, soft, total, maxTextChunks, replyID, direct, nil)
		if scanErr != nil {
			return 0, 0, false, scanErr
		}
		if m > maxTextChunks {
			return 1, 1, true, nil
		}
		if m == total {
			return m, total, false, nil
		}
		if m < total {
			return 0, 0, false, fmt.Errorf("chunk count decreased")
		}
		total = m
	}
}
