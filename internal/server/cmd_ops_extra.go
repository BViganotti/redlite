package server

import (
	"fmt"
	"strconv"
)

func init() {
	registerCommand("ROLE", 1, flagFast, cmdRole)
	registerCommand("READONLY", 1, flagFast, func(c *client, args [][]byte) (result, error) {
		return result{}, fmt.Errorf("ERR This instance has cluster support disabled")
	})
	registerCommand("READWRITE", 1, flagFast, func(c *client, args [][]byte) (result, error) {
		return result{}, fmt.Errorf("ERR This instance has cluster support disabled")
	})
	registerCommand("ASKING", 1, flagFast, func(c *client, args [][]byte) (result, error) {
		return result{}, fmt.Errorf("ERR This instance has cluster support disabled")
	})
	registerCommand("PFSELFTEST", 1, 0, func(c *client, args [][]byte) (result, error) {
		return result{reply: simpleReply("OK")}, nil
	})
}

func cmdRole(c *client, args [][]byte) (result, error) {
	if host, port, ok := c.srv.replicationTarget(); ok {
		state := "connect"
		if c.srv.replUp.Load() {
			state = "connected"
		}
		portNumber, _ := strconv.ParseInt(port, 10, 64)
		return result{reply: arrayReply{
			bulkReply("slave"),
			bulkReply(host),
			intReply(portNumber),
			bulkReply(state),
			intReply(c.srv.masterOffset.Load()),
		}}, nil
	}
	return result{reply: arrayReply{
		bulkReply("master"),
		intReply(c.srv.replOffset.Load()),
		arrayReply{},
	}}, nil
}
