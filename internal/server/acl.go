package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/BViganotti/redlite/internal/store"
)

// aclUser is an immutable snapshot of one user. ACL SETUSER replaces the
// snapshot instead of mutating it, so command checks never race with an edit.
type aclUser struct {
	name     string
	enabled  bool
	password string

	allCommands bool
	allowRead   bool
	allowWrite  bool
	allowed     map[string]bool
	denied      map[string]bool

	allKeys     bool
	keyPatterns []string

	allChannels     bool
	channelPatterns []string
}

func newACLUser(name string) *aclUser {
	return &aclUser{
		name:    name,
		allowed: make(map[string]bool),
		denied:  make(map[string]bool),
	}
}

func (u *aclUser) clone() *aclUser {
	out := *u
	out.allowed = make(map[string]bool, len(u.allowed))
	for key, value := range u.allowed {
		out.allowed[key] = value
	}
	out.denied = make(map[string]bool, len(u.denied))
	for key, value := range u.denied {
		out.denied[key] = value
	}
	out.keyPatterns = append([]string(nil), u.keyPatterns...)
	out.channelPatterns = append([]string(nil), u.channelPatterns...)
	return &out
}

func (u *aclUser) canRun(cmd *command) bool {
	if u.denied[cmd.name] {
		return false
	}
	if u.allCommands || u.allowed[cmd.name] {
		return true
	}
	if u.allowRead && cmd.flags&flagReadOnly != 0 {
		return true
	}
	if u.allowWrite && cmd.flags&flagWrite != 0 {
		return true
	}
	return false
}

func (u *aclUser) canAccessKey(key string) bool {
	if u.allKeys {
		return true
	}
	for _, pattern := range u.keyPatterns {
		if store.Match(pattern, key) {
			return true
		}
	}
	return false
}

func (u *aclUser) canAccessChannel(channel string) bool {
	if u.allChannels {
		return true
	}
	for _, pattern := range u.channelPatterns {
		if store.Match(pattern, channel) {
			return true
		}
	}
	return false
}

// aclStore owns the user table. The default user always exists.
type aclStore struct {
	mu    sync.RWMutex
	users map[string]*aclUser
}

func newACLStore(defaultPassword string) *aclStore {
	admin := newACLUser("default")
	admin.enabled = true
	admin.password = defaultPassword
	admin.allCommands = true
	admin.allKeys = true
	admin.allChannels = true
	return &aclStore{users: map[string]*aclUser{"default": admin}}
}

func (a *aclStore) get(name string) *aclUser {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.users[name]
}

func (a *aclStore) set(user *aclUser) {
	a.mu.Lock()
	a.users[user.name] = user
	a.mu.Unlock()
}

func (a *aclStore) remove(name string) bool {
	if name == "default" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.users[name]; !ok {
		return false
	}
	delete(a.users, name)
	return true
}

func (a *aclStore) names() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.users))
	for name := range a.users {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (a *aclStore) defaultPassword() string {
	user := a.get("default")
	if user == nil {
		return ""
	}
	return user.password
}

// authRequired reports whether the default user has a password.
func (s *Server) authRequired() bool {
	return s.acl.defaultPassword() != ""
}

func (a *aclStore) setPassword(name, password string) {
	user := a.get(name)
	if user == nil {
		return
	}
	updated := user.clone()
	updated.password = password
	a.set(updated)
}

// authenticate validates a username/password pair and returns the user name.
func (s *Server) authenticate(user, password string) (string, error) {
	account := s.acl.get(user)
	if account == nil || !account.enabled {
		return "", errors.New("WRONGPASS invalid username-password pair or user is disabled.")
	}
	if account.password == "" {
		if account.name == "default" {
			return "", errors.New("ERR Client sent AUTH, but no password is set. Did you mean AUTH <username> <password>?")
		}
		return account.name, nil
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(account.password)) != 1 {
		return "", errors.New("WRONGPASS invalid username-password pair or user is disabled.")
	}
	return account.name, nil
}

// checkACL enforces the connected user's permissions.
func (s *Server) checkACL(c *client, name string, args [][]byte) error {
	user := s.acl.get(c.userName)
	if user == nil {
		return errors.New("NOPERM this user has no permissions to run the '" + strings.ToLower(name) + "' command")
	}

	cmd, ok := lookupCommand(name)
	if !ok {
		return nil // unknown commands get the normal error
	}
	if !user.canRun(cmd) {
		return fmt.Errorf("NOPERM this user has no permissions to run the '%s' command", strings.ToLower(name))
	}

	for _, key := range commandKeys(name, args) {
		if !user.canAccessKey(string(key)) {
			return errors.New("NOPERM this user has no permissions to access one of the keys used as arguments")
		}
	}

	switch name {
	case "SUBSCRIBE", "PSUBSCRIBE", "PUBLISH":
		for _, channel := range args[1:] {
			if !user.canAccessChannel(string(channel)) {
				return errors.New("NOPERM this user has no permissions to access one of the channels used as arguments")
			}
		}
	}
	return nil
}

func aclPasswordField(password string) string {
	if password == "" {
		return "nopass"
	}
	sum := sha256.Sum256([]byte(password))
	return "#" + hex.EncodeToString(sum[:])
}

func (u *aclUser) describe() string {
	var parts []string
	parts = append(parts, "user", u.name)
	if u.enabled {
		parts = append(parts, "on")
	} else {
		parts = append(parts, "off")
	}
	parts = append(parts, aclPasswordField(u.password))

	if u.allKeys {
		parts = append(parts, "~*")
	} else if len(u.keyPatterns) == 0 {
		parts = append(parts, "resetkeys")
	} else {
		for _, pattern := range u.keyPatterns {
			parts = append(parts, "~"+pattern)
		}
	}

	if u.allChannels {
		parts = append(parts, "&*")
	} else if len(u.channelPatterns) == 0 {
		parts = append(parts, "resetchannels")
	} else {
		for _, pattern := range u.channelPatterns {
			parts = append(parts, "&"+pattern)
		}
	}

	if u.allCommands {
		parts = append(parts, "+@all")
	} else {
		if u.allowRead {
			parts = append(parts, "+@read")
		}
		if u.allowWrite {
			parts = append(parts, "+@write")
		}
		allowed := make([]string, 0, len(u.allowed))
		for name := range u.allowed {
			allowed = append(allowed, name)
		}
		sort.Strings(allowed)
		for _, name := range allowed {
			parts = append(parts, "+"+strings.ToLower(name))
		}
		denied := make([]string, 0, len(u.denied))
		for name := range u.denied {
			denied = append(denied, name)
		}
		sort.Strings(denied)
		for _, name := range denied {
			parts = append(parts, "-"+strings.ToLower(name))
		}
	}
	return strings.Join(parts, " ")
}

func applyACLRule(user *aclUser, rule string) error {
	switch {
	case rule == "on":
		user.enabled = true
	case rule == "off":
		user.enabled = false
	case rule == "nopass" || rule == "resetpass":
		user.password = ""
	case strings.HasPrefix(rule, ">"):
		user.password = rule[1:]
	case rule == "allkeys" || rule == "~*":
		user.allKeys = true
		user.keyPatterns = nil
	case rule == "resetkeys":
		user.allKeys = false
		user.keyPatterns = nil
	case strings.HasPrefix(rule, "~"):
		user.allKeys = false
		user.keyPatterns = append(user.keyPatterns, rule[1:])
	case rule == "allchannels" || rule == "&*":
		user.allChannels = true
		user.channelPatterns = nil
	case rule == "resetchannels":
		user.allChannels = false
		user.channelPatterns = nil
	case strings.HasPrefix(rule, "&"):
		user.allChannels = false
		user.channelPatterns = append(user.channelPatterns, rule[1:])
	case rule == "allcommands" || rule == "+@all":
		user.allCommands = true
		user.allowRead = false
		user.allowWrite = false
		user.allowed = make(map[string]bool)
		user.denied = make(map[string]bool)
	case rule == "nocommands" || rule == "-@all":
		user.allCommands = false
		user.allowRead = false
		user.allowWrite = false
		user.allowed = make(map[string]bool)
		user.denied = make(map[string]bool)
	case rule == "+@read":
		user.allowRead = true
	case rule == "-@read":
		user.allowRead = false
	case rule == "+@write":
		user.allowWrite = true
	case rule == "-@write":
		user.allowWrite = false
	case rule == "reset":
		*user = *newACLUser(user.name)
	case strings.HasPrefix(rule, "+"):
		name := strings.ToUpper(rule[1:])
		if _, ok := lookupCommand(name); !ok {
			return fmt.Errorf("ERR Unknown command '%s' in ACL SETUSER", strings.ToLower(name))
		}
		user.allowed[name] = true
		delete(user.denied, name)
	case strings.HasPrefix(rule, "-"):
		name := strings.ToUpper(rule[1:])
		user.denied[name] = true
		delete(user.allowed, name)
	default:
		return fmt.Errorf("ERR Error in ACL SETUSER modifier '%s': Syntax error", rule)
	}
	return nil
}

func cmdAclSetUser(c *client, args [][]byte) (result, error) {
	if len(args) < 3 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'acl|setuser' command")
	}
	name := string(args[2])

	base := c.srv.acl.get(name)
	user := newACLUser(name)
	if base != nil {
		user = base.clone()
	}
	for _, raw := range args[3:] {
		if err := applyACLRule(user, string(raw)); err != nil {
			return result{}, err
		}
	}
	if user.name == "default" && !user.enabled {
		return result{}, errors.New("ERR The 'default' user cannot be disabled")
	}
	c.srv.acl.set(user)
	return result{reply: simpleReply("OK")}, nil
}

func cmdAclDelUser(c *client, args [][]byte) (result, error) {
	if len(args) < 3 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'acl|deluser' command")
	}
	var removed int64
	for _, raw := range args[2:] {
		if c.srv.acl.remove(string(raw)) {
			removed++
		}
	}
	return result{reply: intReply(removed)}, nil
}

func cmdAclGetUser(c *client, args [][]byte) (result, error) {
	if len(args) != 3 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'acl|getuser' command")
	}
	user := c.srv.acl.get(string(args[2]))
	if user == nil {
		return result{reply: null}, nil
	}

	flags := make(arrayReply, 0, 3)
	if user.enabled {
		flags = append(flags, bulkReply("on"))
	} else {
		flags = append(flags, bulkReply("off"))
	}
	if user.allCommands {
		flags = append(flags, bulkReply("allcommands"))
	}
	if user.allKeys {
		flags = append(flags, bulkReply("allkeys"))
	}

	commands := aclCommandsDescription(user)
	if commands == "" {
		commands = "-@all"
	}

	return result{reply: mapReply{
		bulkReply("flags"), flags,
		bulkReply("passwords"), arrayReply{bulkReply(aclPasswordField(user.password))},
		bulkReply("commands"), bulkReply(commands),
		bulkReply("keys"), bulkReply(strings.Join(user.keyPatterns, " ")),
		bulkReply("channels"), bulkReply(strings.Join(user.channelPatterns, " ")),
	}}, nil
}
