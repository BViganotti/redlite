package server

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BViganotti/redlite/internal/resp"
	"github.com/BViganotti/redlite/internal/store"
)

// Reply values. Handlers return one of these and the connection loop encodes
// it, which keeps protocol details out of command logic and makes ordering
// (mutate, append to the log, then reply) explicit.
type (
	simpleReply string
	bulkReply   []byte
	intReply    int64
	arrayReply  []any
	mapReply    []any // alternating key, value
	nullReply   struct{}
)

var null = nullReply{}

func writeReply(w *resp.Writer, v any) error {
	switch reply := v.(type) {
	case nil, nullReply:
		return w.WriteNull()
	case simpleReply:
		return w.WriteSimple(string(reply))
	case bulkReply:
		return w.WriteBulk(reply)
	case intReply:
		return w.WriteInt(int64(reply))
	case arrayReply:
		if err := w.WriteArrayHeader(len(reply)); err != nil {
			return err
		}
		for _, item := range reply {
			if err := writeReply(w, item); err != nil {
				return err
			}
		}
		return nil
	case mapReply:
		if len(reply)%2 != 0 {
			return errors.New("resp: map reply has an odd number of elements")
		}
		if err := w.WriteMapHeader(len(reply) / 2); err != nil {
			return err
		}
		for _, item := range reply {
			if err := writeReply(w, item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("resp: unsupported reply type %T", v)
	}
}

// result is what a command hands back to the connection loop.
type result struct {
	reply   any
	rewrite [][]byte // canonical form to persist; nil means "use the request"
	mutated bool
	close   bool
}

// cmdFlag describes a command for COMMAND and for internal bookkeeping.
type cmdFlag uint8

const (
	flagWrite cmdFlag = 1 << iota
	flagReadOnly
	flagAdmin
	flagFast
)

func (f cmdFlag) names() []any {
	var out []any
	if f&flagWrite != 0 {
		out = append(out, bulkReply("write"))
	}
	if f&flagReadOnly != 0 {
		out = append(out, bulkReply("readonly"))
	}
	if f&flagAdmin != 0 {
		out = append(out, bulkReply("admin"))
	}
	if f&flagFast != 0 {
		out = append(out, bulkReply("fast"))
	}
	return out
}

type handlerFunc func(c *client, args [][]byte) (result, error)

type command struct {
	name    string
	arity   int // positive: exact; negative: at least |arity|
	flags   cmdFlag
	handler handlerFunc
}

var commandTable = make(map[string]*command)

func registerCommand(name string, arity int, flags cmdFlag, handler handlerFunc) {
	commandTable[name] = &command{name: name, arity: arity, flags: flags, handler: handler}
}

func lookupCommand(name string) (*command, bool) {
	cmd, ok := commandTable[name]
	return cmd, ok
}

func commandNames() []string {
	names := make([]string, 0, len(commandTable))
	for name := range commandTable {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// arityOK implements Redis arity semantics.
func arityOK(arity, argc int) bool {
	if arity >= 0 {
		return argc == arity
	}
	return argc >= -arity
}

// execute dispatches one command: validate, run the handler, persist if the
// handler reports a mutation, and only then let the caller reply.
func (s *Server) execute(c *client, args [][]byte) (result, error) {
	name := strings.ToUpper(string(args[0]))
	cmd, ok := lookupCommand(name)
	if !ok {
		message := fmt.Sprintf("ERR unknown command '%s'", printable(args[0]))
		if len(args) > 1 {
			message += fmt.Sprintf(", with args beginning with: %s", previewArgs(args[1:]))
		}
		return result{}, errors.New(message)
	}
	if !arityOK(cmd.arity, len(args)) {
		return result{}, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(name))
	}

	res, err := cmd.handler(c, args)
	if err != nil {
		return result{}, err
	}

	if res.mutated && !s.replaying.Load() && s.aof != nil {
		payload := res.rewrite
		if payload == nil {
			payload = args
		}
		if aerr := s.aof.Append(payload); aerr != nil {
			s.aofErrors.Add(1)
			s.logger.Error("append-only file write failed", "err", aerr)
			return result{}, fmt.Errorf("MISCONF Errors writing to the append-only file: %v", aerr)
		}
	}
	return res, nil
}

var (
	errSyntax  = errors.New("ERR syntax error")
	errInteger = errors.New("ERR value is not an integer or out of range")
)

func isWrongType(err error) bool  { return errors.Is(err, store.ErrWrongType) }
func isNotInteger(err error) bool { return errors.Is(err, store.ErrNotInteger) }
func isOverflow(err error) bool   { return errors.Is(err, store.ErrOverflow) }

func overflowError() error {
	return errors.New("ERR increment or decrement would overflow")
}

// parseInt parses a 64-bit integer argument.
func parseInt(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, errInteger
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return 0, errInteger
	}
	return n, nil
}

// printable replaces non-printable bytes so an error reply cannot contain a
// newline or other control character.
func printable(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, ch := range b {
		if ch < 0x20 || ch > 0x7e {
			out = append(out, '?')
			continue
		}
		out = append(out, ch)
	}
	if len(out) > 128 {
		out = append(out[:128], '.', '.', '.')
	}
	return string(out)
}

func previewArgs(args [][]byte) string {
	const maxArgs = 3
	var b strings.Builder
	for i, arg := range args {
		if i == maxArgs {
			break
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(printable(arg))
		b.WriteByte('\'')
	}
	return b.String()
}

// commandLabel renders "client|list"-style names for CLIENT LIST.
func commandLabel(args [][]byte) string {
	if len(args) == 0 {
		return ""
	}
	name := strings.ToLower(string(args[0]))
	if len(args) > 1 && (name == "client" || name == "config" || name == "command") {
		return name + "|" + strings.ToLower(string(args[1]))
	}
	return name
}

func bulkOrNull(b []byte, ok bool) any {
	if !ok {
		return null
	}
	return bulkReply(b)
}
