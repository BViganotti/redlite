package server

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func init() {
	registerCommand("SAVE", 1, flagAdmin, cmdSave)
	registerCommand("BGSAVE", -1, flagAdmin, cmdBGSave)
}

func cmdSave(c *client, args [][]byte) (result, error) {
	if err := c.srv.saveSnapshot(); err != nil {
		return result{}, fmt.Errorf("ERR %v", err)
	}
	return result{reply: simpleReply("OK")}, nil
}

func cmdBGSave(c *client, args [][]byte) (result, error) {
	if len(args) > 2 {
		return result{}, errSyntax
	}
	if len(args) == 2 && !strings.EqualFold(string(args[1]), "SCHEDULE") {
		return result{}, errSyntax
	}
	if !c.srv.startBGSave() {
		return result{}, fmt.Errorf("ERR Background save already in progress")
	}
	return result{reply: simpleReply("Background saving started")}, nil
}

// loadSnapshot restores the snapshot file if one exists. The caller decides
// whether a snapshot is authoritative (it is not when the AOF is enabled).
func (s *Server) loadSnapshot() error {
	path := s.cfg.SnapshotPath()
	file, err := os.Open(path)
	switch {
	case err == nil:
		loadErr := s.data.Load(file)
		closeErr := file.Close()
		if loadErr != nil {
			return fmt.Errorf("load snapshot %s: %w", path, loadErr)
		}
		if closeErr != nil {
			return closeErr
		}
		s.logger.Info("snapshot loaded", "path", path)
		return nil
	case os.IsNotExist(err):
		return nil
	default:
		return fmt.Errorf("open snapshot %s: %w", path, err)
	}
}

// saveSnapshot writes the keyspace atomically: a temporary file is fsynced and
// then renamed over the snapshot path.
func (s *Server) saveSnapshot() error {
	temp, err := os.CreateTemp(s.cfg.Dir, "redlite-dump-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()

	if err := s.data.Save(temp); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.cfg.SnapshotPath()); err != nil {
		return err
	}
	s.lastSave.Store(time.Now().Unix())
	return nil
}

// startBGSave runs a save in the background. It returns false when one is
// already running.
func (s *Server) startBGSave() bool {
	if !s.bgsaveActive.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer s.bgsaveActive.Store(false)
		if err := s.saveSnapshot(); err != nil {
			s.rdbOK.Store(false)
			s.logger.Error("background save failed", "err", err)
			return
		}
		s.rdbOK.Store(true)
	}()
	return true
}
