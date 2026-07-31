// Package sse implements the wire framing used by Server-Sent Events streams.
//
// It deliberately stops at framing: callers remain responsible for interpreting
// an event's data (JSON, a sentinel such as [DONE], or another protocol). The
// decoder accepts both LF and CRLF line endings and does not rely on the read
// boundaries of the supplied io.Reader.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// DefaultMaxEventBytes is the default ceiling used when NewDecoder receives a
// non-positive maximum. SSE events are normally small, while a 1 MiB ceiling
// still leaves room for a tool call whose arguments arrive in one event.
const DefaultMaxEventBytes = 1 << 20

// ErrEventTooLarge identifies an event (or physical line belonging to one)
// that exceeded the decoder's configured memory ceiling.
var ErrEventTooLarge = errors.New("sse: event exceeds maximum size")

// EventTooLargeError reports the configured limit and the size observed when
// it was crossed. It unwraps to ErrEventTooLarge.
type EventTooLargeError struct {
	Limit int
	Size  int
}

func (e *EventTooLargeError) Error() string {
	return fmt.Sprintf("%v: %d bytes exceeds limit of %d bytes", ErrEventTooLarge, e.Size, e.Limit)
}

func (e *EventTooLargeError) Unwrap() error { return ErrEventTooLarge }

// Event is one dispatched SSE event.
//
// Name is the value of the event field, or empty when that field was omitted.
// ID is the stream's current last-event ID, so it persists across events until
// another valid id field replaces or clears it. Retry is present only when the
// dispatched event contained a valid, representable retry value; SSE expresses
// that value as decimal milliseconds. Data contains all data field values in
// order, joined with a single '\n'.
//
// Data is owned by the returned Event and is not mutated by later calls to
// Decoder.Next.
type Event struct {
	Name  string
	ID    string
	Retry *time.Duration
	Data  []byte
}

// Decoder incrementally decodes an SSE byte stream.
//
// A Decoder is stateful and must not be used concurrently. Once Next returns a
// non-EOF error, that error is terminal and subsequent calls return it again.
type Decoder struct {
	reader *bufio.Reader
	max    int

	// A physical line is bounded as well as the assembled event. The allowance
	// covers the longest recognized field prefix, an optional space, a BOM and
	// a line ending. This prevents an ignored comment or unknown field from
	// growing memory without bound while allowing a field value of exactly max.
	lineMax int

	firstLine bool
	eof       bool
	terminal  error

	name          string
	lastID        string
	retry         *time.Duration
	retryWireSize int
	data          []byte
	hasData       bool
}

const lineSyntaxAllowance = 16

// NewDecoder returns a decoder reading from r. maxEventBytes caps the sum of
// the retained SSE field values for one assembled event: event name, current
// ID, valid retry token, data values, and the newlines inserted between data
// values. A value <= 0 selects DefaultMaxEventBytes.
//
// Independently, every physical line is capped at maxEventBytes plus a small
// SSE-syntax allowance. Comments and unknown fields are ignored semantically,
// but remain bounded by this physical-line limit.
func NewDecoder(r io.Reader, maxEventBytes int) *Decoder {
	if maxEventBytes <= 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	lineMax := maxEventBytes
	if lineMax <= math.MaxInt-lineSyntaxAllowance {
		lineMax += lineSyntaxAllowance
	} else {
		lineMax = math.MaxInt
	}
	return &Decoder{
		reader:    bufio.NewReader(r),
		max:       maxEventBytes,
		lineMax:   lineMax,
		firstLine: true,
	}
}

// Next returns the next dispatched event. A blank line dispatches an event
// only after at least one data field has been seen, matching the SSE dispatch
// algorithm. At clean EOF, pending data is dispatched once before a later call
// returns io.EOF.
func (d *Decoder) Next() (Event, error) {
	if d.terminal != nil {
		return Event{}, d.terminal
	}
	if d.eof {
		return Event{}, io.EOF
	}

	for {
		line, eofAfterLine, err := d.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				d.eof = true
				if d.hasData {
					return d.dispatch(), nil
				}
				d.resetEvent()
				return Event{}, io.EOF
			}
			return Event{}, d.fail(err)
		}

		if d.firstLine {
			d.firstLine = false
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
		}

		if len(line) == 0 {
			if d.hasData {
				event := d.dispatch()
				if eofAfterLine {
					d.eof = true
				}
				return event, nil
			}
			d.resetEvent()
		} else if line[0] != ':' { // SSE comments never affect event state.
			if err := d.processField(line); err != nil {
				return Event{}, d.fail(err)
			}
		}

		if eofAfterLine {
			d.eof = true
			if d.hasData {
				return d.dispatch(), nil
			}
			d.resetEvent()
			return Event{}, io.EOF
		}
	}
}

// readLine returns a line without its LF or CRLF terminator. eofAfterLine is
// true only for a final, unterminated line. ReadSlice is used in a loop so the
// result is independent of bufio's buffer size and the source's Read chunks.
func (d *Decoder) readLine() (line []byte, eofAfterLine bool, err error) {
	for {
		fragment, readErr := d.reader.ReadSlice('\n')
		if len(fragment) > d.lineMax-len(line) {
			size := d.lineMax
			if size < math.MaxInt {
				size++
			}
			return nil, false, &EventTooLargeError{Limit: d.max, Size: size}
		}
		line = append(line, fragment...)

		switch readErr {
		case nil:
			// ReadSlice includes the delimiter. CR is a terminator only as the
			// first half of CRLF here; lone CR support is intentionally outside
			// this package's wire contract.
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, false, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, false, io.EOF
			}
			return line, true, nil
		default:
			return nil, false, readErr
		}
	}
}

func (d *Decoder) processField(line []byte) error {
	field, value, found := bytes.Cut(line, []byte{':'})
	if !found {
		value = nil
	} else if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}

	switch {
	case bytes.Equal(field, []byte("event")):
		if err := d.checkSize(len(value), len(d.lastID), len(d.data), d.retryWireSize); err != nil {
			return err
		}
		d.name = string(value)

	case bytes.Equal(field, []byte("data")):
		newDataSize := len(d.data) + len(value)
		if d.hasData {
			newDataSize++ // separator inserted between adjacent data fields
		}
		if err := d.checkSize(len(d.name), len(d.lastID), newDataSize, d.retryWireSize); err != nil {
			return err
		}
		if !d.hasData {
			// Preserve the distinction between a dispatched empty data field and
			// no data field at all, including for callers comparing nil slices.
			d.data = make([]byte, 0, len(value))
		} else {
			d.data = append(d.data, '\n')
		}
		d.data = append(d.data, value...)
		d.hasData = true

	case bytes.Equal(field, []byte("id")):
		// The SSE algorithm ignores the whole field when its value contains a
		// NUL, leaving the previous last-event ID intact.
		if bytes.IndexByte(value, 0) >= 0 {
			return nil
		}
		if err := d.checkSize(len(d.name), len(value), len(d.data), d.retryWireSize); err != nil {
			return err
		}
		d.lastID = string(value)

	case bytes.Equal(field, []byte("retry")):
		retry, ok := parseRetry(value)
		if !ok {
			return nil
		}
		if err := d.checkSize(len(d.name), len(d.lastID), len(d.data), len(value)); err != nil {
			return err
		}
		d.retry = &retry
		d.retryWireSize = len(value)
	}
	return nil
}

func (d *Decoder) checkSize(parts ...int) error {
	total := 0
	for _, part := range parts {
		if part > math.MaxInt-total {
			return &EventTooLargeError{Limit: d.max, Size: math.MaxInt}
		}
		total += part
		if total > d.max {
			return &EventTooLargeError{Limit: d.max, Size: total}
		}
	}
	return nil
}

func parseRetry(value []byte) (time.Duration, bool) {
	if len(value) == 0 {
		return 0, false
	}
	const maxMilliseconds = uint64(math.MaxInt64 / int64(time.Millisecond))
	var milliseconds uint64
	for _, b := range value {
		if b < '0' || b > '9' {
			return 0, false
		}
		digit := uint64(b - '0')
		if milliseconds > (maxMilliseconds-digit)/10 {
			return 0, false
		}
		milliseconds = milliseconds*10 + digit
	}
	return time.Duration(milliseconds) * time.Millisecond, true
}

func (d *Decoder) dispatch() Event {
	event := Event{
		Name:  d.name,
		ID:    d.lastID,
		Retry: d.retry,
		Data:  d.data,
	}
	d.resetEvent()
	return event
}

func (d *Decoder) resetEvent() {
	d.name = ""
	d.retry = nil
	d.retryWireSize = 0
	d.data = nil
	d.hasData = false
}

func (d *Decoder) fail(err error) error {
	d.terminal = err
	return err
}
