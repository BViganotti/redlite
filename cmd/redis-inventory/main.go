// Command redis-inventory captures the command table of a running Redis into
// the JSON fixture used by the parity test.
//
//	go run ./cmd/redis-inventory -addr 127.0.0.1:6379 \
//	    -out internal/server/testdata/redis-commands.json
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

type command struct {
	Name     string   `json:"name"`
	Arity    int64    `json:"arity"`
	Flags    []string `json:"flags"`
	FirstKey int64    `json:"first_key"`
	LastKey  int64    `json:"last_key"`
	Step     int64    `json:"step"`
}

type inventory struct {
	RedisVersion string    `json:"redis_version"`
	GeneratedAt  string    `json:"generated_at"`
	Commands     []command `json:"commands"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:6379", "Redis address")
	out := flag.String("out", "internal/server/testdata/redis-commands.json", "output file")
	flag.Parse()

	conn, err := net.DialTimeout("tcp", *addr, 5*time.Second)
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer conn.Close()

	parser := &parser{reader: bufio.NewReaderSize(conn, 64*1024)}

	version := redisVersion(conn, parser)

	raw, err := sendCommand(conn, parser, "COMMAND")
	if err != nil {
		log.Fatalf("COMMAND: %v", err)
	}
	items, ok := raw.([]any)
	if !ok {
		log.Fatalf("COMMAND returned %T", raw)
	}

	result := inventory{
		RedisVersion: version,
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Commands:     make([]command, 0, len(items)),
	}
	for _, item := range items {
		fields, ok := item.([]any)
		if !ok || len(fields) < 6 {
			continue
		}
		name, _ := fields[0].(string)
		if name == "" {
			continue
		}
		cmd := command{
			Name:     strings.ToLower(name),
			Arity:    asInt(fields[1]),
			Flags:    asStrings(fields[2]),
			FirstKey: asInt(fields[3]),
			LastKey:  asInt(fields[4]),
			Step:     asInt(fields[5]),
		}
		result.Commands = append(result.Commands, cmd)
	}
	sort.Slice(result.Commands, func(i, j int) bool { return result.Commands[i].Name < result.Commands[j].Name })

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, encoded, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %d commands from Redis %s to %s", len(result.Commands), version, *out)
}

func redisVersion(conn net.Conn, parser *parser) string {
	raw, err := sendCommand(conn, parser, "INFO", "server")
	if err != nil {
		return "unknown"
	}
	text, _ := raw.(string)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "redis_version:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "redis_version:"))
		}
	}
	return "unknown"
}

func sendCommand(conn net.Conn, parser *parser, parts ...string) (any, error) {
	raw := make([][]byte, len(parts))
	for i, part := range parts {
		raw[i] = []byte(part)
	}
	if _, err := conn.Write(resp.AppendCommand(nil, raw)); err != nil {
		return nil, err
	}
	return parser.read()
}

func asInt(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case string:
		parsed, _ := strconv.ParseInt(v, 10, 64)
		return parsed
	}
	return 0
}

func asStrings(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// parser is a minimal RESP reply reader. It only covers the types COMMAND and
// INFO use, plus the containers around them.
type parser struct {
	reader *bufio.Reader
}

func (p *parser) read() (any, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("empty reply")
	}

	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, errors.New(line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		size, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, nil
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(p.reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	case '*', '~', '>':
		count, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if count < 0 {
			return nil, nil
		}
		out := make([]any, count)
		for i := range out {
			if out[i], err = p.read(); err != nil {
				return nil, err
			}
		}
		return out, nil
	case '%':
		count, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, count)
		for i := 0; i < count; i++ {
			key, err := p.read()
			if err != nil {
				return nil, err
			}
			value, err := p.read()
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(key)] = value
		}
		return out, nil
	case '_':
		return nil, nil
	case '#':
		return line[1:] == "t", nil
	case ',':
		return strconv.ParseFloat(line[1:], 64)
	case '(':
		return line[1:], nil
	case '=':
		return line[1:], nil
	default:
		return nil, fmt.Errorf("unsupported reply type %q", line[0])
	}
}
