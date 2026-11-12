// Package resp implements the Redis Serialization Protocol, versions 2 and 3.
//
// Request parsing accepts both multibulk arrays (what every Redis client
// sends) and inline commands (what you type into telnet), with hard limits on
// every length prefix so a hostile peer cannot make the server allocate
// unbounded memory.
package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

const (
	// DefaultMaxBulk matches the Redis default for proto-max-bulk-len (512 MiB).
	DefaultMaxBulk int64 = 512 * 1024 * 1024
	// DefaultMaxArray caps the number of elements in a single multibulk request.
	DefaultMaxArray = 1024 * 1024
	// DefaultMaxInline caps the length of one protocol line. Redis uses 64 KiB.
	DefaultMaxInline = 64 * 1024
)

// ProtocolError reports a malformed request. The server answers with an error
// and closes the connection, which is what Redis does for protocol violations.
type ProtocolError struct {
	Message string
}

func (e *ProtocolError) Error() string { return "Protocol error: " + e.Message }

func protoErrorf(format string, args ...any) error {
	return &ProtocolError{Message: fmt.Sprintf(format, args...)}
}

// Reader parses client requests and append-only-file entries.
type Reader struct {
	br        *bufio.Reader
	MaxBulk   int64
	MaxArray  int
	MaxInline int
}

// NewReader wraps r in a buffered protocol reader with default limits.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		br:        bufio.NewReaderSize(r, 16*1024),
		MaxBulk:   DefaultMaxBulk,
		MaxArray:  DefaultMaxArray,
		MaxInline: DefaultMaxInline,
	}
}

// Buffered reports how many bytes have been read from the underlying reader but
// not consumed yet. Log replay uses it to locate the end of the last complete
// command after a crash.
func (r *Reader) Buffered() int { return r.br.Buffered() }

// ReadCommand reads one request. It returns io.EOF only when the stream ends
// cleanly between commands; a stream that ends mid-command yields
// io.ErrUnexpectedEOF so callers can distinguish the two cases.
func (r *Reader) ReadCommand() ([][]byte, error) {
	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, protoErrorf("empty request")
	}
	if line[0] == '*' {
		return r.readMultibulk(line[1:])
	}
	return splitInline(line)
}

func (r *Reader) readMultibulk(header []byte) ([][]byte, error) {
	n, err := parseLen(header, "multibulk")
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > int64(r.MaxArray) {
		return nil, protoErrorf("invalid multibulk length")
	}

	args := make([][]byte, 0, n)
	for i := int64(0); i < n; i++ {
		hdr, err := r.readLine()
		if err != nil {
			return nil, midCommand(err)
		}
		if len(hdr) == 0 {
			return nil, protoErrorf("expected '$', got ' '")
		}
		if hdr[0] != '$' {
			return nil, protoErrorf("expected '$', got '%c'", hdr[0])
		}
		size, err := parseLen(hdr[1:], "bulk")
		if err != nil {
			return nil, err
		}
		if size < 0 || size > r.MaxBulk {
			return nil, protoErrorf("invalid bulk length")
		}

		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r.br, buf); err != nil {
			return nil, midCommand(err)
		}
		if buf[size] != '\r' || buf[size+1] != '\n' {
			return nil, protoErrorf("invalid bulk string terminator")
		}
		args = append(args, buf[:size:size])
	}
	return args, nil
}

// midCommand converts an EOF that happens after a request has started into
// io.ErrUnexpectedEOF.
func midCommand(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func parseLen(b []byte, kind string) (int64, error) {
	if len(b) == 0 || b[0] == '+' {
		return 0, protoErrorf("invalid %s length", kind)
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return 0, protoErrorf("invalid %s length", kind)
	}
	return n, nil
}

// readLine reads one CRLF- or LF-terminated line. Redis tolerates a bare LF,
// and so do we.
func (r *Reader) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.br.ReadSlice('\n')
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(line) > r.MaxInline {
				return nil, protoErrorf("too big inline request")
			}
			continue
		}
		if err != nil {
			if len(line) == 0 {
				return nil, err
			}
			return nil, midCommand(err)
		}
		break
	}

	if len(line) > r.MaxInline {
		return nil, protoErrorf("too big inline request")
	}
	line = line[:len(line)-1] // strip '\n'
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}

// splitInline tokenizes an inline command using Redis' quoting rules.
func splitInline(line []byte) ([][]byte, error) {
	var (
		args   [][]byte
		cur    []byte
		quote  byte
		escape bool
		inWord bool
	)
	endWord := func() {
		if inWord {
			args = append(args, cur)
			cur = nil
			inWord = false
		}
	}

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escape {
			switch ch {
			case 'n':
				cur = append(cur, '\n')
			case 'r':
				cur = append(cur, '\r')
			case 't':
				cur = append(cur, '\t')
			case 'b':
				cur = append(cur, '\b')
			case 'a':
				cur = append(cur, '\a')
			case 'x':
				if i+2 >= len(line) {
					return nil, protoErrorf("invalid hex escape in request")
				}
				v, err := strconv.ParseUint(string(line[i+1:i+3]), 16, 8)
				if err != nil {
					return nil, protoErrorf("invalid hex escape in request")
				}
				cur = append(cur, byte(v))
				i += 2
			default:
				cur = append(cur, ch)
			}
			escape = false
			inWord = true
			continue
		}

		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
				continue
			}
			if ch == '\\' && quote == '"' {
				escape = true
				continue
			}
			cur = append(cur, ch)
			inWord = true
		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			endWord()
		case ch == '"' || ch == '\'':
			quote = ch
			inWord = true
		default:
			cur = append(cur, ch)
			inWord = true
		}
	}

	if escape || quote != 0 {
		return nil, protoErrorf("unbalanced quotes in request")
	}
	endWord()
	return args, nil
}

// Writer serializes replies. Its protocol version decides how nulls, booleans
// and maps are encoded.
type Writer struct {
	bw    *bufio.Writer
	proto int
}

// NewWriter returns a RESP2 writer. Call SetProto after a successful HELLO.
func NewWriter(w io.Writer) *Writer {
	return &Writer{bw: bufio.NewWriterSize(w, 16*1024), proto: 2}
}

// Proto returns the protocol version currently in use (2 or 3).
func (w *Writer) Proto() int { return w.proto }

// SetProto switches the reply encoding. Only versions 2 and 3 exist.
func (w *Writer) SetProto(version int) error {
	if version != 2 && version != 3 {
		return fmt.Errorf("unsupported protocol version %d", version)
	}
	w.proto = version
	return nil
}

// Flush writes any buffered bytes to the underlying writer.
func (w *Writer) Flush() error { return w.bw.Flush() }

func (w *Writer) writeString(s string) error {
	_, err := w.bw.WriteString(s)
	return err
}

// WriteSimple writes a RESP simple string, rejecting embedded newlines so a
// reply can never desynchronize the stream.
func (w *Writer) WriteSimple(s string) error {
	if hasNewline(s) {
		return errors.New("resp: simple string contains newline")
	}
	return w.writeString("+" + s + "\r\n")
}

// WriteError writes a RESP error, folding any newlines into spaces.
func (w *Writer) WriteError(msg string) error {
	msg = sanitizeLine(msg)
	return w.writeString("-" + msg + "\r\n")
}

// WriteInt writes a RESP integer.
func (w *Writer) WriteInt(n int64) error {
	return w.writeString(":" + strconv.FormatInt(n, 10) + "\r\n")
}

// WriteBulk writes a RESP bulk string.
func (w *Writer) WriteBulk(b []byte) error {
	if err := w.writeString("$" + strconv.Itoa(len(b)) + "\r\n"); err != nil {
		return err
	}
	if _, err := w.bw.Write(b); err != nil {
		return err
	}
	return w.writeString("\r\n")
}

// WriteBulkString writes s as a bulk string.
func (w *Writer) WriteBulkString(s string) error { return w.WriteBulk([]byte(s)) }

// WriteNull writes a null bulk string in RESP2 and a null in RESP3.
func (w *Writer) WriteNull() error {
	if w.proto >= 3 {
		return w.writeString("_\r\n")
	}
	return w.writeString("$-1\r\n")
}

// WriteNullArray writes a null array in RESP2 and a null in RESP3.
func (w *Writer) WriteNullArray() error {
	if w.proto >= 3 {
		return w.writeString("_\r\n")
	}
	return w.writeString("*-1\r\n")
}

// WriteBool writes a RESP3 boolean, or an integer in RESP2.
func (w *Writer) WriteBool(v bool) error {
	if w.proto >= 3 {
		if v {
			return w.writeString("#t\r\n")
		}
		return w.writeString("#f\r\n")
	}
	if v {
		return w.WriteInt(1)
	}
	return w.WriteInt(0)
}

// WriteArrayHeader writes the length prefix of an array.
func (w *Writer) WriteArrayHeader(n int) error {
	return w.writeString("*" + strconv.Itoa(n) + "\r\n")
}

// WriteSetHeader writes a RESP3 set header, or an array header in RESP2.
func (w *Writer) WriteSetHeader(n int) error {
	if w.proto >= 3 {
		return w.writeString("~" + strconv.Itoa(n) + "\r\n")
	}
	return w.WriteArrayHeader(n)
}

// WriteDouble writes a RESP3 double, or a bulk string in RESP2. Redis uses the
// same textual form for both protocols.
func (w *Writer) WriteDouble(v float64) error {
	text := FormatDouble(v)
	if w.proto >= 3 {
		return w.writeString("," + text + "\r\n")
	}
	return w.WriteBulkString(text)
}

// WriteBigNumber writes a RESP3 big number, or a bulk string in RESP2.
func (w *Writer) WriteBigNumber(digits string) error {
	if w.proto >= 3 {
		return w.writeString("(" + digits + "\r\n")
	}
	return w.WriteBulkString(digits)
}

// WriteVerbatim writes a RESP3 verbatim string, or a plain bulk string in
// RESP2. The format is a three-character type such as "txt" or "mkd".
func (w *Writer) WriteVerbatim(format, text string) error {
	if w.proto >= 3 {
		if len(format) != 3 {
			return errors.New("resp: verbatim format must be three characters")
		}
		if err := w.writeString("=" + strconv.Itoa(len(text)+4) + "\r\n" + format + ":"); err != nil {
			return err
		}
		if _, err := w.bw.WriteString(text); err != nil {
			return err
		}
		return w.writeString("\r\n")
	}
	return w.WriteBulkString(text)
}

// WriteAttributeHeader writes a RESP3 attribute header. RESP2 has no
// attributes, so the header and the values that follow are dropped by the
// caller when Proto reports 2.
func (w *Writer) WriteAttributeHeader(n int) error {
	if w.proto < 3 {
		return nil
	}
	return w.writeString("|" + strconv.Itoa(n) + "\r\n")
}

// FormatDouble renders a float the way Redis does: no trailing ".0" for
// integral values, "inf"/"-inf" for infinities and lowercase exponent form.
func FormatDouble(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	case math.IsNaN(v):
		return "nan"
	case v == 0:
		return "0"
	case v == math.Trunc(v) && math.Abs(v) < 1e17:
		return strconv.FormatInt(int64(v), 10)
	default:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}

// WritePushHeader writes a RESP3 push header, or an array header in RESP2.
func (w *Writer) WritePushHeader(n int) error {
	if w.proto >= 3 {
		return w.writeString(">" + strconv.Itoa(n) + "\r\n")
	}
	return w.WriteArrayHeader(n)
}

// WriteMapHeader writes a map header in RESP3 or a flat array header in RESP2.
func (w *Writer) WriteMapHeader(pairs int) error {
	if w.proto >= 3 {
		return w.writeString("%" + strconv.Itoa(pairs) + "\r\n")
	}
	return w.WriteArrayHeader(2 * pairs)
}

// AppendCommand appends the RESP encoding of a command to dst. It is the
// inverse of Reader.ReadCommand and is what the append-only file stores.
func AppendCommand(dst []byte, args [][]byte) []byte {
	dst = append(dst, '*')
	dst = strconv.AppendInt(dst, int64(len(args)), 10)
	dst = append(dst, '\r', '\n')
	for _, arg := range args {
		dst = append(dst, '$')
		dst = strconv.AppendInt(dst, int64(len(arg)), 10)
		dst = append(dst, '\r', '\n')
		dst = append(dst, arg...)
		dst = append(dst, '\r', '\n')
	}
	return dst
}

func hasNewline(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' || s[i] == '\n' {
			return true
		}
	}
	return false
}

func sanitizeLine(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r', '\n':
			out[i] = ' '
		default:
			out[i] = s[i]
		}
	}
	return string(out)
}
