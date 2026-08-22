package trailer

import (
	"crypto/rand"
	"fmt"
	"time"
)

// Per-record identifiers.
//
// Every move record carries an id, because the only correction a record ever
// gets is a RETRACTION: a later commit says "record <id> was wrong", and the
// reader folds the two. That needs a name for a record that is stable, unique
// across machines with no coordination, and sortable -- so a hand-rolled ULID
// is what it is: 48 bits of millisecond timestamp followed by 80 bits from
// crypto/rand, rendered in Crockford base32.
//
// Hand-rolled rather than a dependency: this is thirty lines, the format is
// frozen, and safegit's dependency set is deliberately small.
//
// Crockford's alphabet omits I, L, O and U, so an id read aloud or retyped from
// a commit message cannot become a different id through the usual
// transcription slips. Base32 also sorts the same way the bytes do, which is
// what makes the timestamp prefix worth having: ids minted later sort after ids
// minted earlier, so a set of records has a natural chronological order without
// a separate date field.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// idLength is the rendered length of every id: 10 characters of timestamp plus
// 16 of randomness.
const idLength = 26

const (
	idTimeChars   = 10
	idRandomChars = 16
	idRandomBytes = 10
)

// NewID mints a fresh record id. It fails only when the system's entropy source
// does, which is not a condition to paper over: an id drawn from a degraded
// source could collide with another record's, and a retraction naming it would
// then retract the wrong record.
func NewID() (string, error) {
	var random [idRandomBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("reading entropy for a move-record id: %w", err)
	}
	return renderID(uint64(time.Now().UnixMilli()), random), nil
}

// renderID is the pure half of NewID, so the format can be exercised with a
// supplied clock and supplied entropy.
func renderID(millis uint64, random [idRandomBytes]byte) string {
	timestamp := []byte{
		byte(millis >> 40), byte(millis >> 32), byte(millis >> 24),
		byte(millis >> 16), byte(millis >> 8), byte(millis),
	}
	return encodeCrockford(timestamp, idTimeChars) + encodeCrockford(random[:], idRandomChars)
}

// encodeCrockford renders src as exactly chars base32 characters, reading src
// as one big-endian number right-aligned in the available bits. Bits above what
// src supplies are zero, which is why a 48-bit timestamp fits in 10 characters
// (50 bits) with the top two always clear.
func encodeCrockford(src []byte, chars int) string {
	out := make([]byte, chars)
	bits := len(src) * 8
	for i := 0; i < chars; i++ {
		// The character at position i holds the five bits starting at this
		// offset from the least significant end.
		offset := (chars - 1 - i) * 5
		value := 0
		for b := 0; b < 5; b++ {
			pos := offset + b
			if pos >= bits {
				continue
			}
			byteIndex := len(src) - 1 - pos/8
			bit := (src[byteIndex] >> (pos % 8)) & 1
			value |= int(bit) << b
		}
		out[i] = crockfordAlphabet[value]
	}
	return string(out)
}

// ValidID reports whether s is a well-formed record id.
//
// It is strict about case: the encoder emits upper case, and accepting lower
// case would make two spellings of one id, which is exactly what a retraction
// must not have to guess about.
func ValidID(s string) bool {
	if len(s) != idLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		found := false
		for j := 0; j < len(crockfordAlphabet); j++ {
			if s[i] == crockfordAlphabet[j] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
