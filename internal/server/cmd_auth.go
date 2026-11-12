package server

import (
	"crypto/rand"
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
		c.srv.recordACLLog(c, user, "auth", "AUTH", "toplevel")
		return result{}, err
	}
	c.setUser(name)
	c.authed = true
	return result{reply: simpleReply("OK")}, nil
}

func cmdAcl(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "WHOAMI":
		return result{reply: bulkReply(c.user())}, nil

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

	case "DRYRUN":
		return c.aclDryRun(args)

	case "LOG":
		return c.aclLog(args)

	case "GENPASS":
		return cmdAclGenPass(args)

	case "CAT":
		return cmdAclCat(args)

	case "HELP":
		return result{reply: arrayReply{
			bulkReply("ACL <subcommand>"),
			bulkReply("WHOAMI, USERS, LIST, GETUSER, SETUSER, DELUSER, DRYRUN, LOG, CAT, GENPASS"),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for ACL", printable(args[1]))
	}
}

func cmdAclCat(args [][]byte) (result, error) {
	if len(args) == 2 {
		return result{reply: arrayReply{
			bulkReply("keyspace"),
			bulkReply("read"),
			bulkReply("write"),
			bulkReply("admin"),
			bulkReply("pubsub"),
			bulkReply("transaction"),
			bulkReply("connection"),
			bulkReply("string"),
			bulkReply("list"),
			bulkReply("set"),
			bulkReply("sortedset"),
			bulkReply("hash"),
			bulkReply("bitmap"),
			bulkReply("hyperloglog"),
			bulkReply("geo"),
			bulkReply("stream"),
			bulkReply("scripting"),
			bulkReply("fast"),
			bulkReply("slow"),
			bulkReply("blocking"),
		}}, nil
	}
	if len(args) != 3 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'acl|cat' command")
	}
	category := strings.ToLower(string(args[2]))
	known := map[string]bool{
		"keyspace": true, "read": true, "write": true, "admin": true, "pubsub": true,
		"transaction": true, "connection": true, "string": true, "list": true,
		"set": true, "sortedset": true, "hash": true, "bitmap": true,
		"hyperloglog": true, "geo": true, "stream": true, "scripting": true,
		"fast": true, "slow": true, "blocking": true,
	}
	if !known[category] {
		return result{}, fmt.Errorf("ERR Unknown category '%s'", category)
	}
	names := commandNames()
	out := make([]string, 0, len(names))
	for _, name := range names {
		if commandACLCategories(name)[category] {
			out = append(out, strings.ToLower(name))
		}
	}
	sort.Strings(out)
	return result{reply: bulkArrayString(out)}, nil
}

func cmdAclGenPass(args [][]byte) (result, error) {
	bits := 256
	if len(args) == 3 {
		n, err := parseInt(args[2])
		if err != nil || n < 1 || n > 4096 {
			return result{}, fmt.Errorf("ERR ACL GENPASS argument must be the number of bits for the output password, a positive number up to 4096")
		}
		bits = int(n)
	} else if len(args) > 3 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'acl|genpass' command")
	}
	chars := (bits + 3) / 4
	raw := make([]byte, chars)
	if _, err := rand.Read(raw); err != nil {
		return result{}, fmt.Errorf("ERR failed to generate password: %v", err)
	}
	password := make([]byte, chars)
	const hexDigits = "0123456789abcdef"
	for i, b := range raw {
		password[i] = hexDigits[b&0x0f]
	}
	return result{reply: bulkReply(password)}, nil
}

// aclCommandsDescription is kept for callers that describe the root selector.
func aclCommandsDescription(user *aclUser) string {
	return user.root.commandsDescription()
}
