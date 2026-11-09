package server

import (
	"fmt"
	"strings"
)

func init() {
	registerCommand("PING", -1, flagFast, cmdPing)
	registerCommand("ECHO", 2, flagFast, cmdEcho)
	registerCommand("QUIT", 1, flagFast, cmdQuit)
	registerCommand("SELECT", 2, flagFast, cmdSelect)
	registerCommand("HELLO", -1, flagFast, cmdHello)
	registerCommand("CLIENT", -2, flagAdmin, cmdClient)
	registerCommand("COMMAND", -1, flagAdmin, cmdCommand)
	registerCommand("CONFIG", -2, flagAdmin, cmdConfig)
	registerCommand("INFO", -1, flagAdmin, cmdInfo)
	registerCommand("TIME", 1, flagFast, cmdTime)
	registerCommand("SHUTDOWN", -1, flagAdmin, cmdShutdown)
	registerCommand("BGREWRITEAOF", 1, flagAdmin, cmdBGRewriteAOF)
	registerCommand("LASTSAVE", 1, flagFast, cmdLastSave)
}

func cmdPing(c *client, args [][]byte) (result, error) {
	switch len(args) {
	case 1:
		if c.subscribed() {
			return result{reply: pushReply{bulkReply("pong"), bulkReply("")}}, nil
		}
		return result{reply: simpleReply("PONG")}, nil
	case 2:
		if c.subscribed() {
			return result{reply: pushReply{bulkReply("pong"), bulkReply(args[1])}}, nil
		}
		return result{reply: bulkReply(args[1])}, nil
	default:
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'ping' command")
	}
}

func cmdEcho(c *client, args [][]byte) (result, error) {
	return result{reply: bulkReply(args[1])}, nil
}

func cmdQuit(c *client, args [][]byte) (result, error) {
	return result{reply: simpleReply("OK"), close: true}, nil
}

func cmdSelect(c *client, args [][]byte) (result, error) {
	index, err := parseInt(args[1])
	if err != nil {
		return result{}, err
	}
	if index != 0 {
		return result{}, fmt.Errorf("ERR DB index is out of range")
	}
	return result{reply: simpleReply("OK")}, nil
}

func cmdHello(c *client, args [][]byte) (result, error) {
	proto := c.protoNum()
	next := 1

	if next < len(args) {
		value, err := parseInt(args[next])
		if err != nil {
			return result{}, fmt.Errorf("ERR Protocol version is not an integer or out of range")
		}
		if value < 2 || value > 3 {
			return result{}, fmt.Errorf("NOPROTO unsupported protocol version")
		}
		proto = int(value)
		next++
	}

	for next < len(args) {
		option := strings.ToUpper(string(args[next]))
		switch option {
		case "AUTH":
			if next+2 >= len(args) {
				return result{}, errSyntax
			}
			return result{}, fmt.Errorf("WRONGPASS invalid username-password pair or user is disabled")
		case "SETNAME":
			if next+1 >= len(args) {
				return result{}, errSyntax
			}
			if err := c.setName(string(args[next+1])); err != nil {
				return result{}, err
			}
			next += 2
		default:
			return result{}, errSyntax
		}
	}

	if err := c.setProto(proto); err != nil {
		return result{}, err
	}

	return result{reply: mapReply{
		bulkReply("server"), bulkReply("redis"),
		bulkReply("version"), bulkReply("7.0.0"),
		bulkReply("proto"), intReply(proto),
		bulkReply("id"), intReply(c.id),
		bulkReply("mode"), bulkReply("standalone"),
		bulkReply("role"), bulkReply("master"),
		bulkReply("modules"), arrayReply{},
	}}, nil
}

func cmdClient(c *client, args [][]byte) (result, error) {
	sub := strings.ToUpper(string(args[1]))
	switch sub {
	case "ID":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		return result{reply: intReply(c.id)}, nil
	case "GETNAME":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		return result{reply: bulkReply(c.name())}, nil
	case "SETNAME":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		if err := c.setName(string(args[2])); err != nil {
			return result{}, err
		}
		return result{reply: simpleReply("OK")}, nil
	case "SETINFO":
		if len(args) != 4 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "LIST":
		return result{reply: bulkReply(c.srv.clientList())}, nil
	case "INFO":
		return result{reply: bulkReply(c.infoLine())}, nil
	case "GETREDIR":
		return result{reply: intReply(-1)}, nil
	case "NO-EVICT", "NO-TOUCH":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "MAINT_NOTIFICATIONS":
		// Redis 8 clients opt in to maintenance push messages. Push is not
		// implemented, so acknowledge without enabling anything.
		if len(args) < 3 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "UNPAUSE", "PAUSE":
		return result{reply: simpleReply("OK")}, nil
	case "HELP":
		return result{reply: arrayReply{
			bulkReply("CLIENT <subcommand>"),
			bulkReply("ID, GETNAME, SETNAME, SETINFO, LIST, INFO, GETREDIR"),
		}}, nil
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for CLIENT", printable(args[1]))
	}
}

func cmdCommand(c *client, args [][]byte) (result, error) {
	if len(args) == 1 {
		return result{reply: commandListReply()}, nil
	}

	switch strings.ToUpper(string(args[1])) {
	case "COUNT":
		return result{reply: intReply(int64(len(commandTable)))}, nil
	case "INFO":
		if len(args) == 2 {
			return result{reply: commandListReply()}, nil
		}
		out := make(arrayReply, 0, len(args)-2)
		for _, name := range args[2:] {
			cmd, ok := lookupCommand(strings.ToUpper(string(name)))
			if !ok {
				out = append(out, null)
				continue
			}
			out = append(out, commandInfoReply(cmd))
		}
		return result{reply: out}, nil
	case "DOCS":
		return result{reply: arrayReply{}}, nil
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for COMMAND", printable(args[1]))
	}
}

func commandListReply() arrayReply {
	names := commandNames()
	out := make(arrayReply, 0, len(names))
	for _, name := range names {
		out = append(out, commandInfoReply(commandTable[name]))
	}
	return out
}

func commandInfoReply(cmd *command) arrayReply {
	firstKey, lastKey, step := int64(0), int64(0), int64(0)
	if cmd.flags&(flagWrite|flagReadOnly) != 0 && cmd.arity >= 2 {
		firstKey, lastKey, step = 1, 1, 1
	}
	return arrayReply{
		bulkReply(strings.ToLower(cmd.name)),
		intReply(int64(cmd.arity)),
		arrayReply(cmd.flags.names()),
		intReply(firstKey),
		intReply(lastKey),
		intReply(step),
		arrayReply{},
		arrayReply{},
		arrayReply{},
		arrayReply{},
	}
}

func cmdInfo(c *client, args [][]byte) (result, error) {
	sections := make([]string, 0, len(args)-1)
	for _, raw := range args[1:] {
		sections = append(sections, strings.ToLower(string(raw)))
	}
	return result{reply: bulkReply(c.srv.info(sections...))}, nil
}

func cmdTime(c *client, args [][]byte) (result, error) {
	return result{reply: c.srv.timeReply()}, nil
}

func cmdShutdown(c *client, args [][]byte) (result, error) {
	if len(args) > 2 {
		return result{}, errSyntax
	}
	if len(args) == 2 {
		switch strings.ToUpper(string(args[1])) {
		case "NOSAVE", "SAVE":
		default:
			return result{}, errSyntax
		}
	}
	c.srv.requestShutdown()
	return result{close: true}, nil
}

func cmdBGRewriteAOF(c *client, args [][]byte) (result, error) {
	if err := c.srv.rewriteAOF(); err != nil {
		return result{}, fmt.Errorf("ERR %v", err)
	}
	return result{reply: simpleReply("Background append only file rewriting started")}, nil
}

func cmdLastSave(c *client, args [][]byte) (result, error) {
	return result{reply: intReply(c.srv.lastSave.Load())}, nil
}
