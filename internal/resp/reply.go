package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// ReplyKind identifies the type of a decoded RESP reply.
type ReplyKind uint8

// Reply kinds. The names follow the RESP3 type names; RESP2 replies use the
// matching kinds for simple strings, errors, integers, bulk strings and arrays.
const (
	SimpleStringReply ReplyKind = iota
	ErrorReply
	IntegerReply
	BulkStringReply
	ArrayReply
	NullReply
	BooleanReply
	DoubleReply
	BigNumberReply
	VerbatimReply
	MapReply
	SetReply
	PushReply
)

// ReplyPair is one key/value entry of a RESP3 map or attribute.
type ReplyPair struct {
	Key   Reply
	Value Reply
}

// Reply is a decoded protocol reply. Text holds simple strings, errors, bulk
// strings, verbatim strings and big numbers. Pairs is used by maps and
// attributes, Values by arrays, sets and push frames.
type Reply struct {
	Kind       ReplyKind
	Text       string
	Int        int64
	Bool       bool
	Float      float64
	Values     []Reply
	Pairs      []ReplyPair
	Attributes []ReplyPair
}

// IsError reports whether the reply is a RESP error.
func (r *Reply) IsError() bool { return r != nil && r.Kind == ErrorReply }

// IsNull reports whether the reply is a RESP null.
func (r *Reply) IsNull() bool { return r == nil || r.Kind == NullReply }

// maxReplyDepth bounds nesting so a hostile peer cannot exhaust the stack.
const maxReplyDepth = 128

// maxReplyLine bounds protocol header lines returned by a peer.
const maxReplyLine = 64 * 1024

// ReplyReader decodes RESP2 and RESP3 replies from a stream.
type ReplyReader struct {
	br       *bufio.Reader
	MaxBulk  int64
	MaxArray int
}

// NewReplyReader wraps r in a buffered reply decoder with default limits.
func NewReplyReader(r io.Reader) *ReplyReader {
	return &ReplyReader{
		br:       bufio.NewReaderSize(r, 16*1024),
		MaxBulk:  DefaultMaxBulk,
		MaxArray: DefaultMaxArray,
	}
}

// ReadReply reads one reply. Attribute frames are attached to the reply they
// precede instead of being returned on their own.
func (r *ReplyReader) ReadReply() (*Reply, error) {
	return r.read(0)
}

func (r *ReplyReader) read(depth int) (*Reply, error) {
	if depth > maxReplyDepth {
		return nil, errors.New("resp: reply nesting too deep")
	}

	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, errors.New("resp: empty reply line")
	}

	prefix, rest := line[0], line[1:]
	switch prefix {
	case '+':
		return &Reply{Kind: SimpleStringReply, Text: string(rest)}, nil
	case '-':
		return &Reply{Kind: ErrorReply, Text: string(rest)}, nil
	case ':':
		n, err := strconv.ParseInt(string(rest), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("resp: invalid integer reply %q", rest)
		}
		return &Reply{Kind: IntegerReply, Int: n}, nil
	case ',':
		text := string(rest)
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("resp: invalid double reply %q", rest)
		}
		return &Reply{Kind: DoubleReply, Float: f, Text: text}, nil
	case '(':
		if len(rest) == 0 {
			return nil, errors.New("resp: empty big number reply")
		}
		for i, ch := range rest {
			if ch == '-' && i == 0 {
				continue
			}
			if ch < '0' || ch > '9' {
				return nil, fmt.Errorf("resp: invalid big number reply %q", rest)
			}
		}
		return &Reply{Kind: BigNumberReply, Text: string(rest)}, nil
	case '#':
		switch string(rest) {
		case "t":
			return &Reply{Kind: BooleanReply, Bool: true}, nil
		case "f":
			return &Reply{Kind: BooleanReply, Bool: false}, nil
		default:
			return nil, fmt.Errorf("resp: invalid boolean reply %q", rest)
		}
	case '_':
		if len(rest) != 0 {
			return nil, fmt.Errorf("resp: invalid null reply %q", rest)
		}
		return &Reply{Kind: NullReply}, nil
	case '$':
		return r.readBulk(rest)
	case '=':
		return r.readVerbatim(rest)
	case '*', '~', '>':
		return r.readAggregate(rest, prefix, depth)
	case '%':
		return r.readMap(rest, depth)
	case '|':
		attrs, err := r.readPairs(rest, depth)
		if err != nil {
			return nil, err
		}
		actual, err := r.read(depth + 1)
		if err != nil {
			return nil, err
		}
		actual.Attributes = append(attrs, actual.Attributes...)
		return actual, nil
	default:
		return nil, fmt.Errorf("resp: unknown reply type %q", prefix)
	}
}

func (r *ReplyReader) readBulk(header []byte) (*Reply, error) {
	n, err := parseReplyLen(header)
	if err != nil {
		return nil, fmt.Errorf("resp: invalid bulk reply length %q", header)
	}
	if n < 0 {
		return &Reply{Kind: NullReply}, nil
	}
	if n > r.MaxBulk {
		return nil, errors.New("resp: bulk reply too large")
	}
	buf := make([]byte, n+2)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		return nil, err
	}
	if buf[n] != '\r' || buf[n+1] != '\n' {
		return nil, errors.New("resp: invalid bulk reply terminator")
	}
	return &Reply{Kind: BulkStringReply, Text: string(buf[:n])}, nil
}

func (r *ReplyReader) readVerbatim(header []byte) (*Reply, error) {
	n, err := parseReplyLen(header)
	if err != nil || n < 4 {
		return nil, fmt.Errorf("resp: invalid verbatim reply length %q", header)
	}
	if n > r.MaxBulk {
		return nil, errors.New("resp: verbatim reply too large")
	}
	buf := make([]byte, n+2)
	if _, err := io.ReadFull(r.br, buf); err != nil {
		return nil, err
	}
	if buf[n] != '\r' || buf[n+1] != '\n' {
		return nil, errors.New("resp: invalid verbatim reply terminator")
	}
	if buf[3] != ':' {
		return nil, errors.New("resp: invalid verbatim format separator")
	}
	return &Reply{Kind: VerbatimReply, Text: string(buf[4:n])}, nil
}

func (r *ReplyReader) readAggregate(header []byte, prefix byte, depth int) (*Reply, error) {
	n, err := parseReplyLen(header)
	if err != nil {
		return nil, fmt.Errorf("resp: invalid aggregate reply length %q", header)
	}
	if n < 0 {
		return &Reply{Kind: NullReply}, nil
	}
	if n > int64(r.MaxArray) {
		return nil, errors.New("resp: aggregate reply too large")
	}

	kind := ArrayReply
	switch prefix {
	case '~':
		kind = SetReply
	case '>':
		kind = PushReply
	}

	values, err := r.readValues(int(n), depth)
	if err != nil {
		return nil, err
	}
	return &Reply{Kind: kind, Values: values}, nil
}

func (r *ReplyReader) readMap(header []byte, depth int) (*Reply, error) {
	pairs, err := r.readPairs(header, depth)
	if err != nil {
		return nil, err
	}
	return &Reply{Kind: MapReply, Pairs: pairs}, nil
}

func (r *ReplyReader) readPairs(header []byte, depth int) ([]ReplyPair, error) {
	n, err := parseReplyLen(header)
	if err != nil {
		return nil, fmt.Errorf("resp: invalid map reply length %q", header)
	}
	if n < 0 {
		return nil, fmt.Errorf("resp: invalid map reply length %q", header)
	}
	if n > int64(r.MaxArray)/2 {
		return nil, errors.New("resp: map reply too large")
	}

	pairs := make([]ReplyPair, 0, n)
	for i := int64(0); i < n; i++ {
		key, err := r.read(depth + 1)
		if err != nil {
			return nil, err
		}
		value, err := r.read(depth + 1)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, ReplyPair{Key: *key, Value: *value})
	}
	return pairs, nil
}

func (r *ReplyReader) readValues(n int, depth int) ([]Reply, error) {
	values := make([]Reply, 0, n)
	for i := 0; i < n; i++ {
		value, err := r.read(depth + 1)
		if err != nil {
			return nil, err
		}
		values = append(values, *value)
	}
	return values, nil
}

func parseReplyLen(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, errors.New("empty length")
	}
	return strconv.ParseInt(string(b), 10, 64)
}

// readLine reads one CRLF- or LF-terminated protocol line.
func (r *ReplyReader) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.br.ReadSlice('\n')
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(line) > maxReplyLine {
				return nil, errors.New("resp: reply line too long")
			}
			continue
		}
		if err != nil {
			if len(line) == 0 {
				return nil, err
			}
			return nil, err
		}
		break
	}
	if len(line) > maxReplyLine {
		return nil, errors.New("resp: reply line too long")
	}
	line = line[:len(line)-1] // strip '\n'
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}
