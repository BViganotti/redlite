package server

import (
	"fmt"
	"sort"
	"strings"
)

func init() {
	registerCommand("AUTH", -2, flagFast, cmdAuth)
	registerCommand("ACL", -2, flagAdmin, cmdAcl)
}

func cmdAuth(c *client, args [][]byte) (result, error) {
	user := "default"
	password := ""

	switch len(args) {
	case 2:
		password = string(args[1])
	case 3:
		user = string(args[1])
		password = string(args[2])
	default:
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'auth' command")
	}

	name, err := c.srv.authenticate(user, password)
	if err != nil {
		return result{}, err
	}
	c.userName = name
	c.authed = true
	return result{reply: simpleReply("OK")}, nil
}

func cmdAcl(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "WHOAMI":
		return result{reply: bulkReply(c.userName)}, nil

	case "USERS":
		names := c.srv.acl.names()
		out := make(arrayReply, 0, len(names))
		for _, name := range names {
			out = append(out, bulkReply(name))
		}
		return result{reply: out}, nil

	case "LIST":
		names := c.srv.acl.names()
		out := make(arrayReply, 0, len(names))
		for _, name := range names {
			out = append(out, bulkReply(c.srv.acl.get(name).describe()))
		}
		return result{reply: out}, nil

	case "SETUSER":
		return cmdAclSetUser(c, args)

	case "DELUSER":
		return cmdAclDelUser(c, args)

	case "GETUSER":
		return cmdAclGetUser(c, args)

	case "CAT":
		return result{reply: arrayReply{
			bulkReply("keyspace"),
			bulkReply("read"),
			bulkReply("write"),
			bulkReply("admin"),
			bulkReply("pubsub"),
			bulkReply("transaction"),
			bulkReply("connection"),
		}}, nil

	case "HELP":
		return result{reply: arrayReply{
			bulkReply("ACL <subcommand>"),
			bulkReply("WHOAMI, USERS, LIST, GETUSER, SETUSER, DELUSER, CAT"),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for ACL", printable(args[1]))
	}
}

func aclCommandsDescription(user *aclUser) string {
	if user.allCommands {
		return "+@all"
	}
	parts := make([]string, 0, len(user.allowed)+len(user.denied)+2)
	if user.allowRead {
		parts = append(parts, "+@read")
	}
	if user.allowWrite {
		parts = append(parts, "+@write")
	}
	allowed := make([]string, 0, len(user.allowed))
	for name := range user.allowed {
		allowed = append(allowed, name)
	}
	sort.Strings(allowed)
	for _, name := range allowed {
		parts = append(parts, "+"+strings.ToLower(name))
	}
	denied := make([]string, 0, len(user.denied))
	for name := range user.denied {
		denied = append(denied, name)
	}
	sort.Strings(denied)
	for _, name := range denied {
		parts = append(parts, "-"+strings.ToLower(name))
	}
	if len(parts) == 0 {
		return "-@all"
	}
	return strings.Join(parts, " ")
}
