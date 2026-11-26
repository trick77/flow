package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// NewID is a UUIDv7: time-ordered, random below the millisecond (RFC 9562).
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16|uint64(b[6])<<8|uint64(b[7]))
	b[6] = 0x70 | b[6]&0x0f
	b[8] = 0x80 | b[8]&0x3f
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// TimeFormat is how times are stored: UTC RFC 3339 with milliseconds, as the schema's defaults write them.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Format is t as stored.
func Format(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Parse reads a stored time; an unreadable one is the zero time.
func Parse(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ParseNull reads a nullable stored time.
func ParseNull(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := Parse(s.String)
	return &t
}

// Null is a nullable stored time.
func Null(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: Format(*t), Valid: true}
}
