package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/trick77/flow/internal/event"
)

// readLines calls line for every complete line of r (without its newline) up to max bytes and long for every longer
// one, which is skipped to its end: one oversized draft must not stop the stream, as it would stop a bufio.Scanner
// for good. An unterminated tail at EOF is a torn line and dropped. It returns r's error, nil at EOF.
func readLines(r io.Reader, maxLen int, line func([]byte), long func(n int)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	skipped := -1 // bytes of the line being skipped, -1 when not skipping
	for {
		chunk, err := br.ReadSlice('\n')
		if skipped >= 0 {
			skipped += len(chunk)
		} else {
			buf = append(buf, chunk...)
			if len(buf) > maxLen+1 { // +1: the newline
				skipped, buf = len(buf), buf[:0]
			}
		}
		switch {
		case err == nil:
			if skipped >= 0 {
				long(skipped)
				skipped = -1
			} else {
				line(buf[:len(buf)-1])
			}
			buf = buf[:0]
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return nil
		default:
			return err
		}
	}
}

// parseDraft reads one stdout line as an EventDraft of a contract kind whose data decodes into that kind's payload.
// The reason it gives for a refusal names no content.
func parseDraft(line []byte) (event.EventDraft, string) {
	var d event.EventDraft
	if err := json.Unmarshal(line, &d); err != nil {
		return d, "not_json"
	}
	if _, ok := d.Kind.Info(); !ok {
		return d, "unknown_kind"
	}
	if len(bytes.TrimSpace(d.Data)) == 0 || d.Data[0] != '{' {
		return d, "data"
	}
	if _, err := (event.Event{V: event.Version, Kind: d.Kind, Data: d.Data}).Payload(); err != nil {
		return d, "payload"
	}
	return d, ""
}

// ParseDraft is parseDraft for tests of the real sidecar's output.
func ParseDraft(line []byte) (event.EventDraft, error) {
	d, why := parseDraft(line)
	if why != "" {
		return d, errors.New("draft refused: " + why)
	}
	return d, nil
}

// logStderr copies the sidecar's stderr (its own diagnostics and Claude Code's) into the journal line by line, at
// most max bytes per process; beyond that it keeps reading, so the child never blocks on a full pipe, and drops.
// Lines are cut at 4 KiB.
func logStderr(r io.Reader, maxBytes int64, log *slog.Logger) {
	br := bufio.NewReaderSize(r, 4<<10)
	var total, dropped int64
	for {
		chunk, err := br.ReadSlice('\n')
		if n := int64(len(chunk)); n > 0 {
			total += n
			switch {
			case total <= maxBytes:
				log.Info("sidecar stderr", "line", string(bytes.TrimRight(chunk, "\n")))
			case dropped == 0:
				log.Warn("sidecar stderr over its cap, the rest is dropped", "capBytes", maxBytes)
				dropped += n
			default:
				dropped += n
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			if dropped > 0 {
				log.Warn("sidecar stderr dropped", "bytes", dropped)
			}
			return
		}
	}
}
