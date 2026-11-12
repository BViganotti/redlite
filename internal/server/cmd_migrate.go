package server

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	// MIGRATE is registered with flagNoGate and takes the write gate itself,
	// so the whole dump/restore/delete sequence is atomic like Redis.
	registerCommand("MIGRATE", -6, flagWrite|flagNoGate, cmdMigrate)
}

// migrateOptions is the parsed form of a MIGRATE request.
type migrateOptions struct {
	host     string
	port     string
	key      string
	db       int64
	timeout  time.Duration
	copy     bool
	replace  bool
	username *string
	password *string
	keys     []string
	keysMode bool
}

func parseMigrateArgs(args [][]byte) (migrateOptions, error) {
	var opts migrateOptions
	opts.host = string(args[1])
	opts.port = string(args[2])
	opts.key = string(args[3])

	db, err := parseInt(args[4])
	if err != nil {
		return opts, err
	}
	opts.db = db

	timeoutMs, err := parseInt(args[5])
	if err != nil {
		return opts, err
	}
	if timeoutMs < 0 {
		return opts, errors.New("ERR timeout is negative")
	}
	// Redis treats a zero timeout as one second.
	if timeoutMs == 0 {
		timeoutMs = 1000
	}
	opts.timeout = time.Duration(timeoutMs) * time.Millisecond
	opts.keys = []string{opts.key}

	for i := 6; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "COPY":
			opts.copy = true
		case "REPLACE":
			opts.replace = true
		case "AUTH":
			if i+1 >= len(args) {
				return opts, errSyntax
			}
			i++
			password := string(args[i])
			opts.password = &password
		case "AUTH2":
			if i+2 >= len(args) {
				return opts, errSyntax
			}
			username := string(args[i+1])
			password := string(args[i+2])
			opts.username = &username
			opts.password = &password
			i += 2
		case "KEYS":
			if opts.key != "" {
				return opts, errors.New("ERR When using MIGRATE KEYS option, the key argument must be set to the empty string")
			}
			if i+1 >= len(args) {
				return opts, errors.New("ERR When using MIGRATE KEYS option, at least one key is required")
			}
			opts.keysMode = true
			opts.keys = make([]string, 0, len(args)-i-1)
			for _, raw := range args[i+1:] {
				opts.keys = append(opts.keys, string(raw))
			}
			i = len(args)
		default:
			return opts, errSyntax
		}
	}

	if opts.key == "" && !opts.keysMode {
		return opts, errors.New("ERR When using MIGRATE with an empty key, the KEYS option is required")
	}
	return opts, nil
}

// migrateIOError records which side of a round trip failed so the command can
// report a "writing" or "reading" IOERR like Redis does.
type migrateIOError struct {
	writing bool
	err     error
}

func (e *migrateIOError) Error() string { return e.err.Error() }
func (e *migrateIOError) Unwrap() error { return e.err }

// migrateTarget is one short-lived connection to the destination instance.
type migrateTarget struct {
	conn    net.Conn
	rd      *resp.ReplyReader
	timeout time.Duration
}

func newMigrateTarget(conn net.Conn, timeout time.Duration) *migrateTarget {
	return &migrateTarget{conn: conn, rd: resp.NewReplyReader(conn), timeout: timeout}
}

func (t *migrateTarget) send(args [][]byte) error {
	_ = t.conn.SetWriteDeadline(time.Now().Add(t.timeout))
	_, err := t.conn.Write(resp.AppendCommand(nil, args))
	_ = t.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return &migrateIOError{writing: true, err: err}
	}
	return nil
}

func (t *migrateTarget) receive() (*resp.Reply, error) {
	_ = t.conn.SetReadDeadline(time.Now().Add(t.timeout))
	reply, err := t.rd.ReadReply()
	_ = t.conn.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, &migrateIOError{err: err}
	}
	return reply, nil
}

func (t *migrateTarget) roundTrip(args [][]byte) (*resp.Reply, error) {
	if err := t.send(args); err != nil {
		return nil, err
	}
	return t.receive()
}

func migrateIOFailure(err error) error {
	var ioErr *migrateIOError
	if errors.As(err, &ioErr) && ioErr.writing {
		return errors.New("IOERR error or timeout writing to target instance")
	}
	return errors.New("IOERR error or timeout reading to target instance")
}

func migrateTargetError(reply *resp.Reply) error {
	return fmt.Errorf("ERR Target instance replied with error: %s", reply.Text)
}

func cmdMigrate(c *client, args [][]byte) (result, error) {
	opts, err := parseMigrateArgs(args)
	if err != nil {
		return result{}, err
	}

	c.srv.txMu.Lock()
	defer c.srv.txMu.Unlock()

	// Redis resolves the key set before connecting. Missing keys are not an
	// error: the caller may simply have raced with an expiry.
	present := make([]string, 0, len(opts.keys))
	for _, key := range opts.keys {
		if c.srv.data.Exists([]string{key}) > 0 {
			present = append(present, key)
		}
	}
	if len(present) == 0 {
		if opts.keysMode {
			return result{reply: intReply(0)}, nil
		}
		return result{reply: simpleReply("NOKEY")}, nil
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(opts.host, opts.port), opts.timeout)
	if err != nil {
		return result{}, errors.New("IOERR error or timeout connecting to the client")
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}

	target := newMigrateTarget(conn, opts.timeout)

	if opts.password != nil {
		authArgs := [][]byte{[]byte("AUTH")}
		if opts.username != nil {
			authArgs = append(authArgs, []byte(*opts.username))
		}
		authArgs = append(authArgs, []byte(*opts.password))
		reply, err := target.roundTrip(authArgs)
		if err != nil {
			return result{}, migrateIOFailure(err)
		}
		if reply.IsError() {
			return result{}, migrateTargetError(reply)
		}
	}

	// Redis always sends SELECT here, even for database zero. A destination
	// that only supports DB 0 may answer SELECT 0 with an error; that reply is
	// harmless and is ignored.
	selectArgs := [][]byte{[]byte("SELECT"), []byte(strconv.FormatInt(opts.db, 10))}
	selectReply, err := target.roundTrip(selectArgs)
	if err != nil {
		return result{}, migrateIOFailure(err)
	}
	if selectReply.IsError() && opts.db != 0 {
		return result{}, migrateTargetError(selectReply)
	}

	var (
		migrated int64
		deleted  [][]byte
		firstErr error
	)
	for _, key := range present {
		ttl := c.srv.data.TTLMillis(key)
		if ttl == store.ExpiryMissing {
			continue
		}

		started := time.Now()
		payload, ok, err := c.srv.data.Dump(key)
		if err != nil {
			return result{}, err
		}
		if !ok {
			continue
		}

		var ttlArg int64
		switch {
		case ttl == store.ExpiryNoExpiry:
			ttlArg = 0
		case ttl < 0:
			// The key expired between the TTL probe and the dump.
			continue
		default:
			// Use a relative TTL, like Redis MIGRATE, but subtract the time
			// spent serialising the value so the destination does not extend
			// the key's life.
			ttlArg = ttl - time.Since(started).Milliseconds()
			if ttlArg < 1 {
				ttlArg = 1
			}
		}

		restore := [][]byte{
			[]byte("RESTORE"),
			[]byte(key),
			[]byte(strconv.FormatInt(ttlArg, 10)),
			payload,
		}
		if opts.replace {
			restore = append(restore, []byte("REPLACE"))
		}

		reply, err := target.roundTrip(restore)
		if err != nil {
			if firstErr == nil {
				firstErr = migrateIOFailure(err)
			}
			break
		}
		if reply.IsError() {
			if firstErr == nil {
				firstErr = migrateTargetError(reply)
			}
			continue
		}

		migrated++
		if !opts.copy {
			c.srv.data.Del([]string{key})
			deleted = append(deleted, []byte(key))
		}
	}

	res := result{}
	if len(deleted) > 0 {
		res.mutated = true
		res.rewrite = make([][]byte, 0, len(deleted)+1)
		res.rewrite = append(res.rewrite, []byte("DEL"))
		res.rewrite = append(res.rewrite, deleted...)
	}

	if firstErr != nil {
		// If some keys were deleted before the failure, return the error as a
		// reply value so the dispatcher still logs the DEL rewrite. Returning a
		// Go error would drop the mutation and leave AOF/replication behind.
		if res.mutated {
			res.reply = errorReply(firstErr.Error())
			return res, nil
		}
		return result{}, firstErr
	}

	if opts.keysMode {
		res.reply = intReply(migrated)
		return res, nil
	}
	if migrated == 0 {
		res.reply = simpleReply("NOKEY")
		return res, nil
	}
	res.reply = simpleReply("OK")
	return res, nil
}
