package server

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/BViganotti/redlite/internal/store"
)

// pubsubRegistry tracks which clients are listening on which channels and
// patterns. The registry lock is only held while collecting targets; message
// delivery happens outside it so one slow subscriber cannot block the rest.
type pubsubRegistry struct {
	mu       sync.RWMutex
	channels map[string]map[*client]struct{}
	patterns map[string]map[*client]struct{}
}

func newPubsubRegistry() *pubsubRegistry {
	return &pubsubRegistry{
		channels: make(map[string]map[*client]struct{}),
		patterns: make(map[string]map[*client]struct{}),
	}
}

func (p *pubsubRegistry) subscribe(channel string, c *client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.channels[channel] == nil {
		p.channels[channel] = make(map[*client]struct{})
	}
	p.channels[channel][c] = struct{}{}
}

func (p *pubsubRegistry) unsubscribe(channel string, c *client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if subs, ok := p.channels[channel]; ok {
		delete(subs, c)
		if len(subs) == 0 {
			delete(p.channels, channel)
		}
	}
}

func (p *pubsubRegistry) psubscribe(pattern string, c *client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.patterns[pattern] == nil {
		p.patterns[pattern] = make(map[*client]struct{})
	}
	p.patterns[pattern][c] = struct{}{}
}

func (p *pubsubRegistry) punsubscribe(pattern string, c *client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if subs, ok := p.patterns[pattern]; ok {
		delete(subs, c)
		if len(subs) == 0 {
			delete(p.patterns, pattern)
		}
	}
}

func (p *pubsubRegistry) removeAll(c *client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for channel, subs := range p.channels {
		delete(subs, c)
		if len(subs) == 0 {
			delete(p.channels, channel)
		}
	}
	for pattern, subs := range p.patterns {
		delete(subs, c)
		if len(subs) == 0 {
			delete(p.patterns, pattern)
		}
	}
}

// publish delivers a message and returns the number of deliveries. A client
// subscribed to both a channel and a matching pattern receives two messages,
// which is what Redis counts.
func (p *pubsubRegistry) publish(channel string, payload []byte) int {
	type patternMatch struct {
		client  *client
		pattern string
	}

	p.mu.RLock()
	direct := make([]*client, 0, len(p.channels[channel]))
	for c := range p.channels[channel] {
		direct = append(direct, c)
	}
	var matches []patternMatch
	for pattern, subs := range p.patterns {
		if !store.Match(pattern, channel) {
			continue
		}
		for c := range subs {
			matches = append(matches, patternMatch{client: c, pattern: pattern})
		}
	}
	p.mu.RUnlock()

	deliveries := 0
	for _, c := range direct {
		message := pushReply{bulkReply("message"), bulkReply(channel), bulkReply(payload)}
		if c.push(message) == nil {
			deliveries++
		}
	}
	for _, match := range matches {
		message := pushReply{bulkReply("pmessage"), bulkReply(match.pattern), bulkReply(channel), bulkReply(payload)}
		if match.client.push(message) == nil {
			deliveries++
		}
	}
	return deliveries
}

func (p *pubsubRegistry) channelNames(pattern string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.channels))
	for channel := range p.channels {
		if pattern == "" || store.Match(pattern, channel) {
			out = append(out, channel)
		}
	}
	sort.Strings(out)
	return out
}

func (p *pubsubRegistry) subscriberCount(channel string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.channels[channel])
}

func (p *pubsubRegistry) patternCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.patterns)
}

func init() {
	registerCommand("SUBSCRIBE", -2, flagFast, cmdSubscribe)
	registerCommand("UNSUBSCRIBE", -1, flagFast, cmdUnsubscribe)
	registerCommand("PSUBSCRIBE", -2, flagFast, cmdPSubscribe)
	registerCommand("PUNSUBSCRIBE", -1, flagFast, cmdPUnsubscribe)
	registerCommand("PUBLISH", 3, flagFast, cmdPublish)
	registerCommand("PUBSUB", -2, flagFast, cmdPubsub)
}

func cmdSubscribe(c *client, args [][]byte) (result, error) {
	out := make(multiReply, 0, len(args)-1)
	for _, raw := range args[1:] {
		channel := string(raw)
		c.srv.pubsub.subscribe(channel, c)
		count := c.addSubscription(false, channel)
		out = append(out, pushReply{bulkReply("subscribe"), bulkReply(channel), intReply(int64(count))})
	}
	return result{reply: out}, nil
}

func cmdUnsubscribe(c *client, args [][]byte) (result, error) {
	names, err := unsubscribeNames(c, args, false)
	if err != nil {
		return result{}, err
	}
	if len(names) == 0 {
		return result{reply: pushReply{bulkReply("unsubscribe"), null, intReply(0)}}, nil
	}
	out := make(multiReply, 0, len(names))
	for _, name := range names {
		c.srv.pubsub.unsubscribe(name, c)
		count := c.removeSubscription(false, name)
		out = append(out, pushReply{bulkReply("unsubscribe"), bulkReply(name), intReply(int64(count))})
	}
	return result{reply: out}, nil
}

func cmdPSubscribe(c *client, args [][]byte) (result, error) {
	out := make(multiReply, 0, len(args)-1)
	for _, raw := range args[1:] {
		pattern := string(raw)
		c.srv.pubsub.psubscribe(pattern, c)
		count := c.addSubscription(true, pattern)
		out = append(out, pushReply{bulkReply("psubscribe"), bulkReply(pattern), intReply(int64(count))})
	}
	return result{reply: out}, nil
}

func cmdPUnsubscribe(c *client, args [][]byte) (result, error) {
	names, err := unsubscribeNames(c, args, true)
	if err != nil {
		return result{}, err
	}
	if len(names) == 0 {
		return result{reply: pushReply{bulkReply("punsubscribe"), null, intReply(0)}}, nil
	}
	out := make(multiReply, 0, len(names))
	for _, name := range names {
		c.srv.pubsub.punsubscribe(name, c)
		count := c.removeSubscription(true, name)
		out = append(out, pushReply{bulkReply("punsubscribe"), bulkReply(name), intReply(int64(count))})
	}
	return result{reply: out}, nil
}

func unsubscribeNames(c *client, args [][]byte, pattern bool) ([]string, error) {
	if len(args) == 1 {
		return c.subscriptionNames(pattern), nil
	}
	names := make([]string, 0, len(args)-1)
	for _, raw := range args[1:] {
		names = append(names, string(raw))
	}
	return names, nil
}

func cmdPublish(c *client, args [][]byte) (result, error) {
	deliveries := c.srv.pubsub.publish(string(args[1]), args[2])
	return result{reply: intReply(int64(deliveries))}, nil
}

func cmdPubsub(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "CHANNELS":
		pattern := ""
		if len(args) == 3 {
			pattern = string(args[2])
		} else if len(args) > 3 {
			return result{}, errSyntax
		}
		return result{reply: bulkArrayString(c.srv.pubsub.channelNames(pattern))}, nil

	case "NUMSUB":
		out := make(arrayReply, 0, (len(args)-2)*2)
		for _, raw := range args[2:] {
			channel := string(raw)
			out = append(out, bulkReply(channel), intReply(int64(c.srv.pubsub.subscriberCount(channel))))
		}
		return result{reply: out}, nil

	case "NUMPAT":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		return result{reply: intReply(int64(c.srv.pubsub.patternCount()))}, nil

	case "HELP":
		return result{reply: arrayReply{
			bulkReply("PUBSUB <subcommand>"),
			bulkReply("CHANNELS [pattern], NUMSUB [channel ...], NUMPAT"),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for PUBSUB", printable(args[1]))
	}
}
