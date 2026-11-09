package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

func init() {
	registerCommand("AUTH", -2, flagFast, cmdAuth)
	registerCommand("ACL", -2, flagAdmin, cmdAcl)
}

// authRequired reports whether a password is configured.
func (s *Server) authRequired() bool {
	password, _ := s.requirePass.Load().(string)
	return password != ""
}

func (s *Server) currentPassword() string {
	password, _ := s.requirePass.Load().(string)
	return password
}

// authenticate validates credentials for the single "default" user.
func (s *Server) authenticate(user, password string) error {
	expected := s.currentPassword()
	if expected == "" {
		return errors.New("ERR Client sent AUTH, but no password is set. Did you mean AUTH <username> <password>?")
	}
	if user != "default" || subtle.ConstantTimeCompare([]byte(password), []byte(expected)) != 1 {
		return errors.New("WRONGPASS invalid username-password pair or user is disabled.")
	}
	return nil
}

func cmdAuth(c *client, args [][]byte) (result, error) {
	switch len(args) {
	case 2:
		if err := c.srv.authenticate("default", string(args[1])); err != nil {
			return result{}, err
		}
	case 3:
		if err := c.srv.authenticate(string(args[1]), string(args[2])); err != nil {
			return result{}, err
		}
	default:
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'auth' command")
	}
	c.authed = true
	return result{reply: simpleReply("OK")}, nil
}

func cmdAcl(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "WHOAMI":
		return result{reply: bulkReply("default")}, nil

	case "LIST":
		line := "user default on"
		if password := c.srv.currentPassword(); password != "" {
			sum := sha256.Sum256([]byte(password))
			line += " #" + hex.EncodeToString(sum[:])
		} else {
			line += " nopass"
		}
		line += " ~* &* +@all"
		return result{reply: arrayReply{bulkReply(line)}}, nil

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
			bulkReply("WHOAMI, LIST, CAT. Per-user ACLs are not implemented; there is one default user."),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for ACL", printable(args[1]))
	}
}
