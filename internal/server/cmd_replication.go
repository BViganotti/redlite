package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

func init() {
	registerCommand("REPLCONF", -1, flagAdmin, cmdReplConf)
	registerCommand("PSYNC", -1, flagAdmin|flagNoGate, cmdPSync)
	registerCommand("REPLICAOF", 3, flagAdmin, cmdReplicaOf)
	registerCommand("SLAVEOF", 3, flagAdmin, cmdReplicaOf)
}

type replicaLink struct {
	conn net.Conn
	addr string

	mu sync.Mutex
}

func (l *replicaLink) send(data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.conn.Write(data)
	return err
}

func newReplicationID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(raw[:])
}

func cmdReplConf(c *client, args [][]byte) (result, error) {
	// Replicas ACK offsets; that is a no-reply command on the wire.
	if len(args) >= 2 && strings.EqualFold(string(args[1]), "ACK") {
		return result{}, nil
	}
	return result{reply: simpleReply("OK")}, nil
}

// cmdPSync performs a full resynchronisation: one snapshot, then a command
// stream. The write side of the transaction gate is held while the snapshot is
// serialised and the link is registered, so no write can slip between them.
func cmdPSync(c *client, args [][]byte) (result, error) {
	srv := c.srv
	srv.txMu.Lock()
	defer srv.txMu.Unlock()

	var snapshot bytes.Buffer
	if err := srv.data.Save(&snapshot); err != nil {
		return result{}, fmt.Errorf("ERR %v", err)
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()

	header := fmt.Sprintf("+FULLRESYNC %s 0\r\n$%d\r\n", srv.replID, snapshot.Len())
	if _, err := c.conn.Write([]byte(header)); err != nil {
		return result{}, err
	}
	if _, err := c.conn.Write(snapshot.Bytes()); err != nil {
		return result{}, err
	}

	srv.addReplica(c, c.conn)
	srv.logger.Info("replica attached", "addr", c.addr)
	return result{}, nil
}

func cmdReplicaOf(c *client, args [][]byte) (result, error) {
	if strings.EqualFold(string(args[1]), "NO") && strings.EqualFold(string(args[2]), "ONE") {
		c.srv.stopReplication()
		return result{reply: simpleReply("OK")}, nil
	}

	port := string(args[2])
	if _, err := strconv.Atoi(port); err != nil {
		return result{}, fmt.Errorf("ERR Invalid port")
	}
	c.srv.startReplication(string(args[1]), port)
	return result{reply: simpleReply("OK")}, nil
}

func (s *Server) addReplica(c *client, conn net.Conn) {
	link := &replicaLink{conn: conn, addr: c.addr}
	s.replicasMu.Lock()
	s.replicas[c] = link
	s.replicasMu.Unlock()
}

func (s *Server) removeReplica(c *client) {
	s.replicasMu.Lock()
	delete(s.replicas, c)
	s.replicasMu.Unlock()
}

func (s *Server) replicaCount() int {
	s.replicasMu.RLock()
	defer s.replicasMu.RUnlock()
	return len(s.replicas)
}

// propagate sends a command to every attached replica.
func (s *Server) propagate(args [][]byte) {
	s.replicasMu.RLock()
	if len(s.replicas) == 0 {
		s.replicasMu.RUnlock()
		return
	}
	links := make([]*replicaLink, 0, len(s.replicas))
	for _, link := range s.replicas {
		links = append(links, link)
	}
	s.replicasMu.RUnlock()

	data := resp.AppendCommand(nil, args)
	for _, link := range links {
		if err := link.send(data); err != nil {
			// The replica's connection goroutine will notice and clean up.
			_ = link.conn.Close()
		}
	}
}

func (s *Server) stopReplication() {
	s.replMu.Lock()
	if s.replCancel != nil {
		close(s.replCancel)
		s.replCancel = nil
	}
	s.replHost = ""
	s.replPort = ""
	s.replMu.Unlock()

	s.replicaMode.Store(false)
	s.replUp.Store(false)
}

func (s *Server) startReplication(host, port string) {
	s.replMu.Lock()
	if s.replCancel != nil {
		close(s.replCancel)
	}
	cancel := make(chan struct{})
	s.replCancel = cancel
	s.replHost = host
	s.replPort = port
	s.replMu.Unlock()

	s.replicaMode.Store(true)
	s.replUp.Store(false)
	go s.replicaLoop(net.JoinHostPort(host, port), cancel)
}

func (s *Server) replicationTarget() (host, port string, ok bool) {
	s.replMu.Lock()
	defer s.replMu.Unlock()
	return s.replHost, s.replPort, s.replHost != ""
}

func (s *Server) replicaLoop(addr string, cancel chan struct{}) {
	for {
		select {
		case <-cancel:
			return
		default:
		}

		err := s.syncWithMaster(addr, cancel)
		s.replUp.Store(false)
		if err != nil && !errors.Is(err, errReplicationStopped) {
			s.logger.Warn("replication link lost", "master", addr, "err", err)
		}

		select {
		case <-cancel:
			return
		case <-time.After(time.Second):
		}
	}
}

var errReplicationStopped = errors.New("replication stopped")

func (s *Server) syncWithMaster(addr string, cancel chan struct{}) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	buffered := bufio.NewReaderSize(conn, 64*1024)

	write := func(args ...string) error {
		raw := make([][]byte, len(args))
		for i, arg := range args {
			raw[i] = []byte(arg)
		}
		_, err := conn.Write(resp.AppendCommand(nil, raw))
		return err
	}

	if s.cfg.MasterAuth != "" {
		if err := write("AUTH", s.cfg.MasterAuth); err != nil {
			return err
		}
		line, err := buffered.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "+OK") {
			return fmt.Errorf("master authentication failed: %q", strings.TrimSpace(line))
		}
	}

	if err := write("PING"); err != nil {
		return err
	}
	if line, err := buffered.ReadString('\n'); err != nil || !strings.HasPrefix(line, "+PONG") {
		return fmt.Errorf("unexpected PING reply %q, %v", line, err)
	}

	if err := write("REPLCONF", "listening-port", strconv.Itoa(s.port())); err != nil {
		return err
	}
	if line, err := buffered.ReadString('\n'); err != nil || !strings.HasPrefix(line, "+OK") {
		return fmt.Errorf("unexpected REPLCONF reply %q, %v", line, err)
	}

	if err := write("PSYNC", "?", "-1"); err != nil {
		return err
	}
	line, err := buffered.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "+FULLRESYNC") {
		return fmt.Errorf("unexpected PSYNC reply %q", line)
	}

	line, err = buffered.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "$") {
		return fmt.Errorf("unexpected snapshot header %q", line)
	}
	size, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil || size < 0 || size > maxSnapshotSize {
		return fmt.Errorf("invalid snapshot size %q", line)
	}

	snapshot := make([]byte, size)
	if _, err := io.ReadFull(buffered, snapshot); err != nil {
		return err
	}
	if err := s.data.Load(bytes.NewReader(snapshot)); err != nil {
		return fmt.Errorf("load master snapshot: %w", err)
	}
	s.replUp.Store(true)
	s.logger.Info("replication snapshot loaded", "master", addr, "bytes", size)

	reader := resp.NewReader(buffered)
	applyClient := newReplayClient(s)
	applyClient.fromMaster = true

	for {
		select {
		case <-cancel:
			return errReplicationStopped
		default:
		}

		args, err := reader.ReadCommand()
		if err != nil {
			return err
		}
		if len(args) == 0 {
			continue
		}
		if _, err := s.execute(applyClient, args); err != nil {
			return fmt.Errorf("apply %q: %w", args[0], err)
		}
	}
}

// maxSnapshotSize mirrors the store's snapshot limit.
const maxSnapshotSize = 2 << 30
