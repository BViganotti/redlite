package server

import (
	"testing"
	"time"
)

func (c *testConn) readFrame() any {
	c.t.Helper()
	value, err := readValue(c.rd)
	if err != nil {
		c.t.Fatalf("read push: %v", err)
	}
	return value
}

func TestPubSubDirect(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	ack := expectArray(t, sub.do("SUBSCRIBE", "news"))
	expectBulk(t, ack[0], "subscribe")
	expectBulk(t, ack[1], "news")
	expectInt(t, ack[2], 1)

	expectInt(t, pub.do("PUBLISH", "news", "hello"), 1)
	message := expectArray(t, sub.readFrame())
	expectBulk(t, message[0], "message")
	expectBulk(t, message[1], "news")
	expectBulk(t, message[2], "hello")

	expectInt(t, pub.do("PUBLISH", "other", "nobody"), 0)
}

func TestPubSubMultipleChannels(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	sub.send("SUBSCRIBE", "a", "b")
	first := expectArray(t, sub.readFrame())
	second := expectArray(t, sub.readFrame())
	expectInt(t, first[2], 1)
	expectInt(t, second[2], 2)

	expectInt(t, pub.do("PUBLISH", "b", "to-b"), 1)
	message := expectArray(t, sub.readFrame())
	expectBulk(t, message[1], "b")
	expectBulk(t, message[2], "to-b")
}

func TestPubSubPattern(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	ack := expectArray(t, sub.do("PSUBSCRIBE", "news.*"))
	expectBulk(t, ack[0], "psubscribe")
	expectBulk(t, ack[1], "news.*")

	expectInt(t, pub.do("PUBLISH", "news.tech", "hello"), 1)
	message := expectArray(t, sub.readFrame())
	expectBulk(t, message[0], "pmessage")
	expectBulk(t, message[1], "news.*")
	expectBulk(t, message[2], "news.tech")
	expectBulk(t, message[3], "hello")
}

func TestPubSubCountsPatternAndChannel(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	sub.do("SUBSCRIBE", "news.tech")
	sub.do("PSUBSCRIBE", "news.*")

	if n := pub.do("PUBLISH", "news.tech", "both"); n != int64(2) {
		t.Fatalf("PUBLISH counted %v deliveries, want 2", n)
	}
	sub.readFrame()
	sub.readFrame()
}

func TestUnsubscribe(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	sub.send("SUBSCRIBE", "a", "b")
	sub.readFrame()
	sub.readFrame()

	ack := expectArray(t, sub.do("UNSUBSCRIBE", "a"))
	expectBulk(t, ack[0], "unsubscribe")
	expectInt(t, ack[2], 1)

	expectInt(t, pub.do("PUBLISH", "a", "gone"), 0)
	expectInt(t, pub.do("PUBLISH", "b", "still"), 1)
	sub.readFrame()

	all := expectArray(t, sub.do("UNSUBSCRIBE"))
	expectBulk(t, all[0], "unsubscribe")
	expectInt(t, all[2], 0)
}

func TestPubSubIntrospection(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	other := dial(t, srv)

	sub.do("SUBSCRIBE", "news")
	sub.do("PSUBSCRIBE", "news.*")

	channels := expectArray(t, other.do("PUBSUB", "CHANNELS"))
	if len(channels) != 1 || string(channels[0].([]byte)) != "news" {
		t.Fatalf("PUBSUB CHANNELS = %v", channels)
	}

	counts := expectArray(t, other.do("PUBSUB", "NUMSUB", "news", "other"))
	expectBulk(t, counts[0], "news")
	expectInt(t, counts[1], 1)
	expectBulk(t, counts[2], "other")
	expectInt(t, counts[3], 0)

	expectInt(t, other.do("PUBSUB", "NUMPAT"), 1)
}

func TestSubscriberModeRestrictions(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)

	sub.do("SUBSCRIBE", "news")
	expectError(t, sub.do("GET", "k"), "only (P|S)SUBSCRIBE")

	pong := expectArray(t, sub.do("PING", "hi"))
	expectBulk(t, pong[0], "pong")
	expectBulk(t, pong[1], "hi")
}

func TestPubSubRESP3(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	sub.do("HELLO", "3")
	ack := expectArray(t, sub.do("SUBSCRIBE", "news"))
	expectBulk(t, ack[1], "news")

	pub.do("PUBLISH", "news", "frame")
	message := expectArray(t, sub.readFrame())
	expectBulk(t, message[0], "message")
	expectBulk(t, message[2], "frame")

	// RESP3 clients may still run ordinary commands while subscribed.
	expectSimple(t, sub.do("SET", "k", "v"), "OK")
}

func TestPubSubCleansUpOnDisconnect(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	sub.do("SUBSCRIBE", "news")
	_ = sub.conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub.do("PUBLISH", "news", "x") == int64(0) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscriber was not removed after disconnect")
}

func TestPubSubDeliversToEverySubscriber(t *testing.T) {
	srv := newTestServer(t, nil)
	one := dial(t, srv)
	two := dial(t, srv)
	pub := dial(t, srv)

	one.do("SUBSCRIBE", "news")
	two.do("SUBSCRIBE", "news")

	if deliveries := pub.do("PUBLISH", "news", "hello"); deliveries != int64(2) {
		t.Fatalf("deliveries = %v, want 2", deliveries)
	}

	first := expectArray(t, one.readFrame())
	second := expectArray(t, two.readFrame())
	expectBulk(t, first[2], "hello")
	expectBulk(t, second[2], "hello")
}
