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
	"time"

	"github.com/BViganotti/redlite/internal/store"
)

// aclSelector is one permission set. A user has a root selector plus any
// number of parenthesised selectors; a command is allowed when at least one
// selector permits the command together with all of its keys and channels.
type aclSelector struct {
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

func newACLSelector() *aclSelector {
	return &aclSelector{
		allowed: make(map[string]bool),
		denied:  make(map[string]bool),
	}
}

func (s *aclSelector) clone() *aclSelector {
	out := *s
	out.allowed = make(map[string]bool, len(s.allowed))
	for key, value := range s.allowed {
		out.allowed[key] = value
	}
	out.denied = make(map[string]bool, len(s.denied))
	for key, value := range s.denied {
		out.denied[key] = value
	}
	out.keyPatterns = append([]string(nil), s.keyPatterns...)
	out.channelPatterns = append([]string(nil), s.channelPatterns...)
	return &out
}

func (s *aclSelector) canRun(cmd *command) bool {
	if s.denied[cmd.name] {
		return false
	}
	if s.allCommands || s.allowed[cmd.name] {
		return true
	}
	if s.allowRead && cmd.flags&flagReadOnly != 0 {
		return true
	}
	if s.allowWrite && cmd.flags&flagWrite != 0 {
		return true
	}
	return false
}

func (s *aclSelector) canAccessKey(key string) bool {
	if s.allKeys {
		return true
	}
	for _, pattern := range s.keyPatterns {
		if store.Match(pattern, key) {
			return true
		}
	}
	return false
}

func (s *aclSelector) canAccessChannel(channel string) bool {
	if s.allChannels {
		return true
	}
	for _, pattern := range s.channelPatterns {
		if store.Match(pattern, channel) {
			return true
		}
	}
	return false
}

func (s *aclSelector) permits(cmd *command, keys, channels []string) bool {
	if !s.canRun(cmd) {
		return false
	}
	for _, key := range keys {
		if !s.canAccessKey(key) {
			return false
		}
	}
	for _, channel := range channels {
		if !s.canAccessChannel(channel) {
			return false
		}
	}
	return true
}

func (s *aclSelector) commandsDescription() string {
	if s.allCommands {
		return "+@all"
	}
	parts := make([]string, 0, len(s.allowed)+len(s.denied)+3)
	parts = append(parts, "-@all")
	if s.allowRead {
		parts = append(parts, "+@read")
	}
	if s.allowWrite {
		parts = append(parts, "+@write")
	}
	allowed := make([]string, 0, len(s.allowed))
	for name := range s.allowed {
		allowed = append(allowed, name)
	}
	sort.Strings(allowed)
	for _, name := range allowed {
		parts = append(parts, "+"+strings.ToLower(name))
	}
	denied := make([]string, 0, len(s.denied))
	for name := range s.denied {
		denied = append(denied, name)
	}
	sort.Strings(denied)
	for _, name := range denied {
		parts = append(parts, "-"+strings.ToLower(name))
	}
	return strings.Join(parts, " ")
}

func (s *aclSelector) keysDescription() string {
	if s.allKeys {
		return "~*"
	}
	if len(s.keyPatterns) == 0 {
		return "resetkeys"
	}
	parts := make([]string, 0, len(s.keyPatterns))
	for _, pattern := range s.keyPatterns {
		parts = append(parts, "~"+pattern)
	}
	return strings.Join(parts, " ")
}

func (s *aclSelector) channelsDescription() string {
	if s.allChannels {
		return "&*"
	}
	if len(s.channelPatterns) == 0 {
		return "resetchannels"
	}
	parts := make([]string, 0, len(s.channelPatterns))
	for _, pattern := range s.channelPatterns {
		parts = append(parts, "&"+pattern)
	}
	return strings.Join(parts, " ")
}

func (s *aclSelector) describe() string {
	parts := make([]string, 0, 4)
	if keys := s.keysDescription(); keys != "" {
		parts = append(parts, keys)
	}
	if channels := s.channelsDescription(); channels != "" {
		parts = append(parts, channels)
	}
	parts = append(parts, s.commandsDescription())
	return strings.Join(parts, " ")
}

// applySelectorRule applies one command/key/channel rule.
func applySelectorRule(sel *aclSelector, rule string) error {
	switch {
	case rule == "allkeys" || rule == "~*":
		sel.allKeys = true
		sel.keyPatterns = nil
	case rule == "resetkeys":
		sel.allKeys = false
		sel.keyPatterns = nil
	case strings.HasPrefix(rule, "~"):
		sel.allKeys = false
		sel.keyPatterns = append(sel.keyPatterns, rule[1:])
	case rule == "allchannels" || rule == "&*":
		sel.allChannels = true
		sel.channelPatterns = nil
	case rule == "resetchannels":
		sel.allChannels = false
		sel.channelPatterns = nil
	case strings.HasPrefix(rule, "&"):
		sel.allChannels = false
		sel.channelPatterns = append(sel.channelPatterns, rule[1:])
	case rule == "allcommands" || rule == "+@all":
		sel.allCommands = true
		sel.allowRead = false
		sel.allowWrite = false
		sel.allowed = make(map[string]bool)
		sel.denied = make(map[string]bool)
	case rule == "nocommands" || rule == "-@all":
		sel.allCommands = false
		sel.allowRead = false
		sel.allowWrite = false
		sel.allowed = make(map[string]bool)
		sel.denied = make(map[string]bool)
	case rule == "+@read":
		sel.allowRead = true
	case rule == "-@read":
		sel.allowRead = false
	case rule == "+@write":
		sel.allowWrite = true
	case rule == "-@write":
		sel.allowWrite = false
	case rule == "reset":
		*sel = *newACLSelector()
	case strings.HasPrefix(rule, "+") && len(rule) > 1:
		name := strings.ToUpper(rule[1:])
		if _, ok := lookupCommand(name); !ok {
			return fmt.Errorf("ERR Unknown command '%s' in ACL SETUSER", strings.ToLower(name))
		}
		sel.allowed[name] = true
		delete(sel.denied, name)
	case strings.HasPrefix(rule, "-") && len(rule) > 1:
		name := strings.ToUpper(rule[1:])
		if _, ok := lookupCommand(name); !ok {
			return fmt.Errorf("ERR Unknown command '%s' in ACL SETUSER", strings.ToLower(name))
		}
		sel.denied[name] = true
		delete(sel.allowed, name)
	default:
		return fmt.Errorf("ERR Error in ACL SETUSER modifier '%s': Syntax error", rule)
	}
	return nil
}

// aclUser is an immutable snapshot of one user.
type aclUser struct {
	name     string
	enabled  bool
	password string
	root     *aclSelector
	extra    []*aclSelector
}

func newACLUser(name string) *aclUser {
	return &aclUser{name: name, root: newACLSelector()}
}

func (u *aclUser) clone() *aclUser {
	out := *u
	if u.root != nil {
		out.root = u.root.clone()
	}
	out.extra = make([]*aclSelector, len(u.extra))
	for i, sel := range u.extra {
		out.extra[i] = sel.clone()
	}
	return &out
}

func (u *aclUser) selectors() []*aclSelector {
	out := make([]*aclSelector, 0, 1+len(u.extra))
	if u.root != nil {
		out = append(out, u.root)
	}
	return append(out, u.extra...)
}

func (u *aclUser) canRun(cmd *command) bool {
	for _, sel := range u.selectors() {
		if sel.canRun(cmd) {
			return true
		}
	}
	return false
}

// permits reports whether any selector allows the command with every key and
// channel. It also returns which aspect failed, for Redis-compatible errors.
func (u *aclUser) permits(cmd *command, keys, channels []string) (ok, commandDenied, keyDenied, channelDenied bool) {
	for _, sel := range u.selectors() {
		if !sel.canRun(cmd) {
			continue
		}
		keysOK := true
		for _, key := range keys {
			if !sel.canAccessKey(key) {
				keyDenied = true
				keysOK = false
				break
			}
		}
		if !keysOK {
			continue
		}
		channelsOK := true
		for _, channel := range channels {
			if !sel.canAccessChannel(channel) {
				channelDenied = true
				channelsOK = false
				break
			}
		}
		if channelsOK {
			return true, false, false, false
		}
	}
	return false, !u.canRun(cmd), keyDenied, channelDenied
}

func (u *aclUser) describe() string {
	parts := []string{"user", u.name}
	if u.enabled {
		parts = append(parts, "on")
	} else {
		parts = append(parts, "off")
	}
	if u.password == "" {
		parts = append(parts, "nopass", "sanitize-payload")
	} else {
		parts = append(parts, "sanitize-payload", aclPasswordField(u.password))
	}
	parts = append(parts, u.root.describe())
	for _, sel := range u.extra {
		parts = append(parts, "("+sel.describe()+")")
	}
	return strings.Join(parts, " ")
}

// aclLogEntry is one ACL failure kept for ACL LOG.
type aclLogEntry struct {
	Count       int64
	Reason      string
	Context     string
	Object      string
	Username    string
	ClientInfo  string
	ID          int64
	Created     int64
	LastUpdated int64
}

// aclStore owns the user table and the ACL failure log.
type aclStore struct {
	mu    sync.RWMutex
	users map[string]*aclUser
	logMu sync.Mutex
	log   []*aclLogEntry
	next  int64
	max   int
}

func newACLStore(defaultPassword string) *aclStore {
	admin := newACLUser("default")
	admin.enabled = true
	admin.password = defaultPassword
	admin.root.allCommands = true
	admin.root.allKeys = true
	admin.root.allChannels = true
	return &aclStore{users: map[string]*aclUser{"default": admin}, max: 128}
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

func (a *aclStore) setLogMax(n int) {
	if n < 0 {
		n = 0
	}
	a.logMu.Lock()
	a.max = n
	if len(a.log) > n {
		a.log = a.log[len(a.log)-n:]
	}
	a.logMu.Unlock()
}

func (a *aclStore) logMax() int {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	return a.max
}

func (a *aclStore) addLog(entry *aclLogEntry) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	now := time.Now().UnixMilli()
	for _, existing := range a.log {
		if existing.Username == entry.Username && existing.Reason == entry.Reason &&
			existing.Object == entry.Object && existing.Context == entry.Context {
			existing.Count++
			existing.LastUpdated = now
			return
		}
	}
	entry.ID = a.next
	a.next++
	entry.Created = now
	entry.LastUpdated = now
	entry.Count = 1
	a.log = append(a.log, entry)
	if a.max >= 0 && len(a.log) > a.max {
		a.log = a.log[len(a.log)-a.max:]
	}
}

func (a *aclStore) logEntries(limit int) []*aclLogEntry {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	out := make([]*aclLogEntry, 0, len(a.log))
	for i := len(a.log) - 1; i >= 0; i-- {
		if limit >= 0 && len(out) >= limit {
			break
		}
		copyEntry := *a.log[i]
		out = append(out, &copyEntry)
	}
	return out
}

func (a *aclStore) resetLog() {
	a.logMu.Lock()
	a.log = nil
	a.logMu.Unlock()
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

// recordACLLog stores one ACL failure, merging repeats of the same user,
// reason, object and context.
func (s *Server) recordACLLog(c *client, username, reason, object, context string) {
	info := ""
	if c != nil {
		info = c.infoLine()
		if username == "" {
			username = c.user()
		}
	}
	s.acl.addLog(&aclLogEntry{
		Reason:     reason,
		Context:    context,
		Object:     object,
		Username:   username,
		ClientInfo: info,
	})
}

// checkACL enforces the connected user's permissions.
func (s *Server) checkACL(c *client, name string, args [][]byte) error {
	user := s.acl.get(c.user())
	if user == nil {
		s.recordACLLog(c, c.user(), "command", name, "toplevel")
		return errors.New("NOPERM this user has no permissions to run the '" + strings.ToLower(name) + "' command")
	}

	cmd, ok := lookupCommand(name)
	if !ok {
		return nil // unknown commands get the normal error
	}
	keys := commandKeys(name, args)
	keyStrings := make([]string, len(keys))
	for i, key := range keys {
		keyStrings[i] = string(key)
	}
	var channels []string
	switch name {
	case "SUBSCRIBE", "PSUBSCRIBE", "PUBLISH", "SSUBSCRIBE", "SUNSUBSCRIBE", "SPUBLISH":
		for _, channel := range args[1:] {
			channels = append(channels, string(channel))
		}
	}

	ok, commandDenied, keyDenied, channelDenied := user.permits(cmd, keyStrings, channels)
	if ok {
		return nil
	}
	switch {
	case commandDenied:
		s.recordACLLog(c, user.name, "command", strings.ToLower(name), "toplevel")
		return fmt.Errorf("NOPERM User %s has no permissions to run the '%s' command", user.name, strings.ToLower(name))
	case keyDenied:
		object := ""
		if len(keyStrings) > 0 {
			object = keyStrings[0]
		}
		s.recordACLLog(c, user.name, "key", object, "toplevel")
		if object != "" {
			return fmt.Errorf("NOPERM User %s has no permissions to access the '%s' key", user.name, object)
		}
		return errors.New("NOPERM this user has no permissions to access one of the keys used as arguments")
	case channelDenied:
		object := ""
		if len(channels) > 0 {
			object = channels[0]
		}
		s.recordACLLog(c, user.name, "channel", object, "toplevel")
		return fmt.Errorf("NOPERM User %s has no permissions to access the '%s' channel", user.name, object)
	default:
		return fmt.Errorf("NOPERM User %s has no permissions to run the '%s' command", user.name, strings.ToLower(name))
	}
}

func aclPasswordField(password string) string {
	if password == "" {
		return "nopass"
	}
	sum := sha256.Sum256([]byte(password))
	return "#" + hex.EncodeToString(sum[:])
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
	case rule == "reset":
		user.enabled = false
		user.password = ""
		user.root = newACLSelector()
		user.extra = nil
	default:
		return applySelectorRule(user.root, rule)
	}
	return nil
}

// parseSelector splits the contents of a parenthesised selector.
func parseSelector(parts []string) (*aclSelector, error) {
	sel := newACLSelector()
	for _, part := range parts {
		for _, rule := range strings.Fields(part) {
			if rule == "" {
				continue
			}
			switch {
			case rule == "on", rule == "off", rule == "nopass", rule == "resetpass",
				strings.HasPrefix(rule, ">"):
				return nil, fmt.Errorf("ERR Error in ACL SETUSER modifier '%s': Syntax error", rule)
			}
			if err := applySelectorRule(sel, rule); err != nil {
				return nil, err
			}
		}
	}
	return sel, nil
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

	for i := 3; i < len(args); i++ {
		raw := string(args[i])
		if strings.HasPrefix(raw, "(") {
			var parts []string
			token := raw
			for {
				body := strings.TrimPrefix(token, "(")
				if strings.HasSuffix(body, ")") {
					parts = append(parts, strings.TrimSuffix(body, ")"))
					break
				}
				parts = append(parts, body)
				i++
				if i >= len(args) {
					return result{}, fmt.Errorf("ERR Unmatched parenthesis in selector")
				}
				token = string(args[i])
			}
			sel, err := parseSelector(parts)
			if err != nil {
				return result{}, err
			}
			user.extra = append(user.extra, sel)
			continue
		}
		if err := applyACLRule(user, raw); err != nil {
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

	flags := make(arrayReply, 0, 4)
	if user.enabled {
		flags = append(flags, bulkReply("on"))
	} else {
		flags = append(flags, bulkReply("off"))
	}
	if user.password == "" {
		flags = append(flags, bulkReply("nopass"))
	}
	flags = append(flags, bulkReply("sanitize-payload"))
	if user.root.allCommands {
		flags = append(flags, bulkReply("allcommands"))
	}
	if user.root.allKeys {
		flags = append(flags, bulkReply("allkeys"))
	}

	passwords := arrayReply{}
	if user.password != "" {
		passwords = append(passwords, bulkReply(strings.TrimPrefix(aclPasswordField(user.password), "#")))
	}

	selectors := arrayReply{}
	for _, sel := range user.extra {
		selectors = append(selectors, arrayReply{
			bulkReply("commands"), bulkReply(sel.commandsDescription()),
			bulkReply("keys"), bulkReply(getUserKeys(sel)),
			bulkReply("channels"), bulkReply(getUserChannels(sel)),
		})
	}

	return result{reply: mapReply{
		bulkReply("flags"), flags,
		bulkReply("passwords"), passwords,
		bulkReply("commands"), bulkReply(user.root.commandsDescription()),
		bulkReply("keys"), bulkReply(getUserKeys(user.root)),
		bulkReply("channels"), bulkReply(getUserChannels(user.root)),
		bulkReply("selectors"), selectors,
	}}, nil
}

func (c *client) aclDryRun(args [][]byte) (result, error) {
	if len(args) < 4 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'acl|dryrun' command")
	}
	username := string(args[2])
	user := c.srv.acl.get(username)
	if user == nil {
		return result{}, fmt.Errorf("ERR User '%s' not found", username)
	}
	name := strings.ToUpper(string(args[3]))
	cmd, ok := lookupCommand(name)
	if !ok {
		return result{}, fmt.Errorf("ERR Command '%s' not found", strings.ToLower(name))
	}
	commandArgs := args[3:]
	if !arityOK(cmd.arity, len(commandArgs)) {
		return result{}, fmt.Errorf("ERR wrong number of arguments for '%s' command", strings.ToLower(name))
	}
	keys := commandKeys(name, commandArgs)
	keyStrings := make([]string, len(keys))
	for i, key := range keys {
		keyStrings[i] = string(key)
	}
	var channels []string
	switch name {
	case "SUBSCRIBE", "PSUBSCRIBE", "PUBLISH", "SSUBSCRIBE", "SUNSUBSCRIBE", "SPUBLISH":
		for _, channel := range commandArgs[1:] {
			channels = append(channels, string(channel))
		}
	}
	ok, commandDenied, keyDenied, _ := user.permits(cmd, keyStrings, channels)
	if ok {
		return result{reply: simpleReply("OK")}, nil
	}
	if commandDenied {
		return result{reply: bulkReply(fmt.Sprintf("User %s has no permissions to run the '%s' command", username, strings.ToLower(name)))}, nil
	}
	if keyDenied && len(keyStrings) > 0 {
		return result{reply: bulkReply(fmt.Sprintf("User %s has no permissions to access the '%s' key", username, keyStrings[0]))}, nil
	}
	if len(channels) > 0 {
		return result{reply: bulkReply(fmt.Sprintf("User %s has no permissions to access the '%s' channel", username, channels[0]))}, nil
	}
	return result{reply: bulkReply(fmt.Sprintf("User %s has no permissions to run the '%s' command", username, strings.ToLower(name)))}, nil
}

func (c *client) aclLog(args [][]byte) (result, error) {
	if len(args) == 3 && strings.EqualFold(string(args[2]), "RESET") {
		c.srv.acl.resetLog()
		return result{reply: simpleReply("OK")}, nil
	}
	limit := 10
	if len(args) == 3 {
		n, err := parseInt(args[2])
		if err != nil {
			return result{}, err
		}
		if n < 0 {
			n = 0
		}
		limit = int(n)
	} else if len(args) > 3 {
		return result{}, errSyntax
	}

	entries := c.srv.acl.logEntries(limit)
	out := make(arrayReply, 0, len(entries))
	now := time.Now().UnixMilli()
	for _, entry := range entries {
		out = append(out, mapReply{
			bulkReply("count"), intReply(entry.Count),
			bulkReply("reason"), bulkReply(entry.Reason),
			bulkReply("context"), bulkReply(entry.Context),
			bulkReply("object"), bulkReply(entry.Object),
			bulkReply("username"), bulkReply(entry.Username),
			bulkReply("age-seconds"), bulkReply(fmt.Sprintf("%.3f", float64(now-entry.Created)/1000)),
			bulkReply("client-info"), bulkReply(entry.ClientInfo),
			bulkReply("entry-id"), intReply(entry.ID),
			bulkReply("timestamp-created"), intReply(entry.Created),
			bulkReply("timestamp-last-updated"), intReply(entry.LastUpdated),
		})
	}
	return result{reply: out}, nil
}

func getUserKeys(sel *aclSelector) string {
	if sel.allKeys {
		return "~*"
	}
	parts := make([]string, 0, len(sel.keyPatterns))
	for _, pattern := range sel.keyPatterns {
		parts = append(parts, "~"+pattern)
	}
	return strings.Join(parts, " ")
}

func getUserChannels(sel *aclSelector) string {
	if sel.allChannels {
		return "&*"
	}
	parts := make([]string, 0, len(sel.channelPatterns))
	for _, pattern := range sel.channelPatterns {
		parts = append(parts, "&"+pattern)
	}
	return strings.Join(parts, " ")
}
