package server

import (
	"fmt"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("MULTI", 1, flagFast, cmdMulti)
	registerCommand("EXEC", 1, flagWrite, cmdExec)
	registerCommand("DISCARD", 1, flagFast, cmdDiscard)
	registerCommand("WATCH", -2, flagFast, cmdWatch)
	registerCommand("UNWATCH", 1, flagFast, cmdUnwatch)
}

func cmdMulti(c *client, args [][]byte) (result, error) {
	if c.multi {
		return result{}, fmt.Errorf("ERR MULTI calls can not be nested")
	}
	c.multi = true
	c.dirty = false
	c.queued = nil
	return result{reply: simpleReply("OK")}, nil
}

func cmdDiscard(c *client, args [][]byte) (result, error) {
	if !c.multi {
		return result{}, fmt.Errorf("ERR DISCARD without MULTI")
	}
	c.resetMulti()
	return result{reply: simpleReply("OK")}, nil
}

func cmdWatch(c *client, args [][]byte) (result, error) {
	if c.multi {
		return result{}, fmt.Errorf("ERR WATCH inside MULTI is not allowed")
	}
	if c.watched == nil {
		c.watched = make(map[string]store.WatchState, len(args)-1)
	}
	for _, key := range args[1:] {
		c.watched[string(key)] = c.srv.data.Watch(string(key))
	}
	return result{reply: simpleReply("OK")}, nil
}

func cmdUnwatch(c *client, args [][]byte) (result, error) {
	c.watched = nil
	return result{reply: simpleReply("OK")}, nil
}

func cmdExec(c *client, args [][]byte) (result, error) {
	if !c.multi {
		return result{}, fmt.Errorf("ERR EXEC without MULTI")
	}
	if c.dirty {
		c.resetMulti()
		return result{}, fmt.Errorf("EXECABORT Transaction discarded because of previous errors.")
	}

	for key, watched := range c.watched {
		if c.srv.data.Watch(key) != watched {
			c.resetMulti()
			return result{reply: nullArrayReply{}}, nil
		}
	}

	queued := c.queued
	c.multi = false
	c.dirty = false
	c.queued = nil
	c.watched = nil
	c.executing = true
	defer func() { c.executing = false }()

	aof := c.srv.aof
	writeAOF := aof != nil && !c.srv.replaying.Load()

	replies := make(arrayReply, 0, len(queued))
	wroteMulti := false
	closeAfter := false

	for _, command := range queued {
		res, err := c.srv.executeLocked(c, command, false)
		if err != nil {
			replies = append(replies, errorReply(err.Error()))
			continue
		}
		replies = append(replies, res.reply)
		if res.close {
			closeAfter = true
		}
		if !res.mutated || !writeAOF {
			continue
		}

		if !wroteMulti {
			if aerr := aof.Append([][]byte{[]byte("MULTI")}); aerr != nil {
				c.srv.aofErrors.Add(1)
				c.srv.logger.Error("append-only file write failed", "err", aerr)
			} else {
				wroteMulti = true
			}
		}
		payload := res.rewrite
		if payload == nil {
			payload = command
		}
		if aerr := aof.Append(payload); aerr != nil {
			c.srv.aofErrors.Add(1)
			c.srv.logger.Error("append-only file write failed", "err", aerr)
		}
	}

	if wroteMulti {
		if aerr := aof.Append([][]byte{[]byte("EXEC")}); aerr != nil {
			c.srv.aofErrors.Add(1)
			c.srv.logger.Error("append-only file write failed", "err", aerr)
		}
	}

	return result{reply: replies, close: closeAfter}, nil
}
