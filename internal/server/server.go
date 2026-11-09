// Package server ties the keyspace, the protocol and the command table into a
// running Redis-compatible server.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BViganotti/redlite/internal/config"
	"github.com/BViganotti/redlite/internal/resp"
	"github.com/BViganotti/redlite/internal/store"
)

// Server is a running redlite instance.
type Server struct {
	cfg    config.Config
	logger *slog.Logger
	level  *slog.LevelVar

	data *store.Store
	aof  *store.AOF

	pubsub *pubsubRegistry

	lnMu sync.Mutex
	lns  []net.Listener

	clientsMu sync.RWMutex
	clients   map[int64]*client
	nextID    atomic.Int64

	maxClients      atomic.Int64
	started         time.Time
	totalConns      atomic.Uint64
	totalCmds       atomic.Uint64
	aofErrors       atomic.Uint64
	rejected        atomic.Uint64
	lastSave        atomic.Int64
	bgsaveActive    atomic.Bool
	bgrewriteActive atomic.Bool
	rdbOK           atomic.Bool
	replaying       atomic.Bool
	shuttingDown    atomic.Bool

	wg           sync.WaitGroup
	shutdownOnce sync.Once

	// txMu makes EXEC atomic with respect to other clients. Normal commands
	// take a read lock; EXEC takes the write lock for the whole batch.
	txMu sync.RWMutex

	replicasMu  sync.RWMutex
	replicas    map[*client]*replicaLink
	replID      string
	backlog     *replBacklog
	replOffset  atomic.Int64
	fullResyncs atomic.Uint64

	replMu       sync.Mutex
	replCancel   chan struct{}
	replHost     string
	replPort     string
	replUp       atomic.Bool
	replicaMode  atomic.Bool
	masterOffset atomic.Int64
	masterReplID atomic.Value // string

	acl *aclStore

	notifyConfig atomic.Value // notifyFlags
	notifyRaw    atomic.Value // string
	notifyCh     chan string
	notifyStop   chan struct{}
}

// New builds a server, opens the append-only file if enabled, and replays it.
func New(cfg config.Config, logger *slog.Logger, level *slog.LevelVar) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if level == nil {
		level = new(slog.LevelVar)
	}
	level.Set(cfg.Level())

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	srv := &Server{
		cfg:      cfg,
		logger:   logger,
		level:    level,
		data:     store.New(),
		pubsub:   newPubsubRegistry(),
		clients:  make(map[int64]*client),
		replicas: make(map[*client]*replicaLink),
		replID:   newReplicationID(),
		backlog:  newReplBacklog(cfg.ReplBacklogSize),
		started:  time.Now(),
	}
	srv.maxClients.Store(int64(cfg.MaxClients))
	srv.lastSave.Store(srv.started.Unix())
	srv.rdbOK.Store(true)
	srv.acl = newACLStore(cfg.RequirePass)
	srv.masterReplID.Store("")

	if err := validateNotifyFlags(cfg.NotifyKeyspaceEvents); err != nil {
		srv.data.Close()
		return nil, err
	}
	srv.notifyRaw.Store(cfg.NotifyKeyspaceEvents)
	srv.notifyConfig.Store(parseNotifyFlags(cfg.NotifyKeyspaceEvents))
	srv.notifyCh = make(chan string, 1024)
	srv.notifyStop = make(chan struct{})
	srv.data.SetExpireHook(func(key string) {
		select {
		case srv.notifyCh <- key:
		default:
		}
	})
	go srv.expiryEventLoop()

	srv.data.StartExpiry(100*time.Millisecond, 20)

	if !cfg.AppendOnly {
		if err := srv.loadSnapshot(); err != nil {
			srv.data.Close()
			return nil, err
		}
		return srv, nil
	}

	aof, err := store.OpenAOF(cfg.AOFPath(), store.SyncPolicy(cfg.AppendFsync))
	if err != nil {
		srv.data.Close()
		return nil, fmt.Errorf("open append-only file: %w", err)
	}
	srv.aof = aof

	replayClient := newReplayClient(srv)
	replayed := 0
	srv.replaying.Store(true)
	truncated, loadErr := aof.Load(func(args [][]byte) error {
		if len(args) == 0 {
			return nil
		}
		if _, err := srv.execute(replayClient, args); err != nil {
			return fmt.Errorf("command %q: %w", args[0], err)
		}
		replayed++
		return nil
	})
	srv.replaying.Store(false)

	if loadErr != nil {
		_ = aof.Close()
		srv.data.Close()
		return nil, fmt.Errorf("load append-only file: %w", loadErr)
	}
	if truncated > 0 {
		srv.logger.Warn("truncated partial command at end of append-only file",
			"path", aof.Path(), "offset", truncated)
	}
	srv.logger.Info("append-only file loaded",
		"path", aof.Path(), "commands", replayed, "policy", cfg.AppendFsync)

	return srv, nil
}

// Listen binds the configured address without accepting connections.
func (s *Server) Listen() error {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if len(s.lns) > 0 {
		return errors.New("server already listening")
	}

	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		return err
	}
	opened := []net.Listener{ln}

	if s.cfg.TLSPort > 0 {
		tlsConfig, err := s.buildTLSConfig()
		if err != nil {
			_ = ln.Close()
			return err
		}
		tlsListener, err := tls.Listen("tcp", s.cfg.TLSAddr(), tlsConfig)
		if err != nil {
			_ = ln.Close()
			return err
		}
		opened = append(opened, tlsListener)
	}

	s.lns = opened
	return nil
}

// Addr returns the plain TCP address, or nil before Listen.
func (s *Server) Addr() net.Addr {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if len(s.lns) == 0 {
		return nil
	}
	return s.lns[0].Addr()
}

// TLSAddr returns the TLS address, or nil when TLS is disabled.
func (s *Server) TLSAddr() net.Addr {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if len(s.lns) < 2 {
		return nil
	}
	return s.lns[1].Addr()
}

// ListenAndServe binds and then accepts connections.
func (s *Server) ListenAndServe() error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve()
}

// Serve accepts connections on every listener until Shutdown closes them.
func (s *Server) Serve() error {
	s.lnMu.Lock()
	listeners := append([]net.Listener(nil), s.lns...)
	s.lnMu.Unlock()
	if len(listeners) == 0 {
		return errors.New("server is not listening")
	}

	for i, ln := range listeners {
		s.logger.Info("redlite listening",
			"addr", ln.Addr().String(), "tls", i > 0, "aof", s.aof != nil)
	}

	errCh := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func(listener net.Listener) {
			errCh <- s.acceptLoop(listener)
		}(ln)
	}

	var firstErr error
	for range listeners {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) acceptLoop(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.shuttingDown.Load() || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	if s.shuttingDown.Load() {
		_ = conn.Close()
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(5 * time.Minute)
	}

	s.clientsMu.Lock()
	if int64(len(s.clients)) >= s.maxClients.Load() {
		s.clientsMu.Unlock()
		s.rejected.Add(1)
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		writer := resp.NewWriter(conn)
		_ = writer.WriteError("ERR max number of clients reached")
		_ = writer.Flush()
		_ = conn.Close()
		return
	}
	id := s.nextID.Add(1)
	c := newClient(s, conn, id)
	s.clients[id] = c
	s.clientsMu.Unlock()

	s.totalConns.Add(1)
	s.wg.Add(1)
	s.logger.Debug("client connected", "id", id, "addr", c.addr)
	go s.serveClient(c)
}

func (s *Server) serveClient(c *client) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic serving client", "id", c.id, "addr", c.addr, "panic", r)
		}
		c.close()
		s.clientsMu.Lock()
		delete(s.clients, c.id)
		s.clientsMu.Unlock()
		s.removeReplica(c)
		s.pubsub.removeAll(c)
		s.wg.Done()
		s.logger.Debug("client disconnected", "id", c.id, "addr", c.addr)
	}()

	for {
		if s.cfg.Timeout > 0 {
			_ = c.conn.SetReadDeadline(time.Now().Add(s.cfg.Timeout))
		}

		args, err := c.rd.ReadCommand()
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return
			}
			var protoErr *resp.ProtocolError
			if errors.As(err, &protoErr) {
				_ = c.wr.WriteError("ERR " + protoErr.Error())
				_ = c.wr.Flush()
				s.logger.Debug("protocol error", "id", c.id, "err", protoErr.Message)
				return
			}
			// io.EOF and anything else: the peer or the socket is gone.
			return
		}
		if len(args) == 0 {
			continue
		}

		s.totalCmds.Add(1)
		c.cmds.Add(1)
		c.setLastCommand(commandLabel(args))

		res, err := s.execute(c, args)
		if err != nil {
			if werr := c.sendReply(errorReply(err.Error())); werr != nil {
				return
			}
		} else if res.reply != nil {
			if werr := c.sendReply(res.reply); werr != nil {
				return
			}
		}
		if res.close {
			return
		}
	}
}

// Shutdown stops accepting, closes client connections, and flushes the
// append-only file. It is safe to call more than once.
func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.shutdownOnce.Do(func() {
		s.shuttingDown.Store(true)

		s.lnMu.Lock()
		for _, ln := range s.lns {
			_ = ln.Close()
		}
		s.lnMu.Unlock()

		s.clientsMu.RLock()
		for _, c := range s.clients {
			c.close()
		}
		s.clientsMu.RUnlock()

		// Wake clients blocked in BLPOP/BRPOP/BLMOVE so shutdown cannot hang.
		s.data.WakeAll()
		s.stopReplication()
		close(s.notifyStop)

		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			err = ctx.Err()
		}

		s.data.Close()
		if s.aof != nil {
			if aerr := s.aof.Close(); aerr != nil && err == nil {
				err = fmt.Errorf("close append-only file: %w", aerr)
			}
		}
		s.logger.Info("redlite stopped")
	})
	return err
}

func (s *Server) nowMs() int64 { return time.Now().UnixMilli() }

// buildTLSConfig loads the listener certificate and, when a CA is configured,
// requires client certificates.
func (s *Server) buildTLSConfig() (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	}
	if s.cfg.TLSCACertFile != "" {
		pool, err := loadCertPool(s.cfg.TLSCACertFile)
		if err != nil {
			return nil, err
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return config, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", path)
	}
	return pool, nil
}

// requestShutdown initiates an orderly shutdown from inside a command.
func (s *Server) requestShutdown() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			s.logger.Error("shutdown failed", "err", err)
		}
	}()
}
