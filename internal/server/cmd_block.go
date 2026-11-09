package server

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerCommand("BLPOP", -3, flagWrite|flagBlocking, cmdBLPop)
	registerCommand("BRPOP", -3, flagWrite|flagBlocking, cmdBRPop)
	registerCommand("BLMOVE", 6, flagWrite|flagBlocking, cmdBLMove)
	registerCommand("BRPOPLPUSH", 4, flagWrite|flagBlocking, cmdBRPopLPush)
	registerCommand("LMOVE", 5, flagWrite, cmdLMove)
	registerCommand("RPOPLPUSH", 3, flagWrite, cmdRPopLPush)
}

var (
	errTimeoutNotFloat = errors.New("ERR timeout is not a float or out of range")
	errTimeoutNegative = errors.New("ERR timeout is negative")
)

func parseBlockTimeout(arg []byte) (time.Duration, error) {
	value, err := strconv.ParseFloat(string(arg), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errTimeoutNotFloat
	}
	if value < 0 {
		return 0, errTimeoutNegative
	}
	if value == 0 {
		return 0, nil
	}
	// A century is enough; anything larger is a client bug.
	if value > 100*365*24*3600 {
		return 0, errTimeoutNotFloat
	}
	return time.Duration(value * float64(time.Second)), nil
}

func cmdBLPop(c *client, args [][]byte) (result, error) {
	return blockingPop(c, args, true)
}

func cmdBRPop(c *client, args [][]byte) (result, error) {
	return blockingPop(c, args, false)
}

func blockingPop(c *client, args [][]byte, left bool) (result, error) {
	timeout, err := parseBlockTimeout(args[len(args)-1])
	if err != nil {
		return result{}, err
	}
	keys := args[1 : len(args)-1]
	names := make([]string, len(keys))
	for i, key := range keys {
		names[i] = string(key)
	}

	try := func() (string, []byte, error) {
		release := c.enterReadGate()
		defer release()
		for _, key := range names {
			var (
				values [][]byte
				err    error
			)
			if left {
				values, err = c.srv.data.LPop(key, 1)
			} else {
				values, err = c.srv.data.RPop(key, 1)
			}
			if err != nil {
				return "", nil, err
			}
			if len(values) > 0 {
				return key, values[0], nil
			}
		}
		return "", nil, nil
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	popCommand := "LPOP"
	if !left {
		popCommand = "RPOP"
	}

	for {
		key, value, err := try()
		if err != nil {
			return result{}, err
		}
		if value != nil {
			return poppedResult(key, value, popCommand), nil
		}
		if c.executing || c.srv.shuttingDown.Load() {
			return result{reply: nullArrayReply{}}, nil
		}

		waiter := c.srv.data.Wait(names)
		key, value, err = try()
		if err != nil {
			c.srv.data.CancelWait(waiter)
			return result{}, err
		}
		if value != nil {
			c.srv.data.CancelWait(waiter)
			return poppedResult(key, value, popCommand), nil
		}

		select {
		case <-waiter.C:
			c.srv.data.CancelWait(waiter)
		case <-timeoutCh:
			c.srv.data.CancelWait(waiter)
			return result{reply: nullArrayReply{}}, nil
		}
	}
}

func poppedResult(key string, value []byte, popCommand string) result {
	return result{
		reply:   arrayReply{bulkReply(key), bulkReply(value)},
		mutated: true,
		rewrite: [][]byte{[]byte(popCommand), []byte(key)},
	}
}

func cmdLMove(c *client, args [][]byte) (result, error) {
	fromLeft, err := parseMoveDirection(args[3])
	if err != nil {
		return result{}, err
	}
	toLeft, err := parseMoveDirection(args[4])
	if err != nil {
		return result{}, err
	}
	return moveOnce(c, string(args[1]), string(args[2]), fromLeft, toLeft)
}

func cmdRPopLPush(c *client, args [][]byte) (result, error) {
	return moveOnce(c, string(args[1]), string(args[2]), false, true)
}

func moveOnce(c *client, source, destination string, fromLeft, toLeft bool) (result, error) {
	// The caller holds the transaction read gate; LMOVE is not blocking.
	value, ok, err := c.srv.data.Move(source, destination, fromLeft, toLeft)
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}
	return result{
		reply:   bulkReply(value),
		mutated: true,
		rewrite: moveRewrite(source, destination, fromLeft, toLeft),
	}, nil
}

func moveRewrite(source, destination string, fromLeft, toLeft bool) [][]byte {
	return [][]byte{
		[]byte("LMOVE"),
		[]byte(source),
		[]byte(destination),
		[]byte(moveDirectionName(fromLeft)),
		[]byte(moveDirectionName(toLeft)),
	}
}

func cmdBLMove(c *client, args [][]byte) (result, error) {
	fromLeft, err := parseMoveDirection(args[3])
	if err != nil {
		return result{}, err
	}
	toLeft, err := parseMoveDirection(args[4])
	if err != nil {
		return result{}, err
	}
	timeout, err := parseBlockTimeout(args[5])
	if err != nil {
		return result{}, err
	}
	return blockingMove(c, string(args[1]), string(args[2]), fromLeft, toLeft, timeout)
}

func cmdBRPopLPush(c *client, args [][]byte) (result, error) {
	timeout, err := parseBlockTimeout(args[3])
	if err != nil {
		return result{}, err
	}
	return blockingMove(c, string(args[1]), string(args[2]), false, true, timeout)
}

func blockingMove(c *client, source, destination string, fromLeft, toLeft bool, timeout time.Duration) (result, error) {
	try := func() ([]byte, bool, error) {
		release := c.enterReadGate()
		defer release()
		return c.srv.data.Move(source, destination, fromLeft, toLeft)
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}
	rewrite := moveRewrite(source, destination, fromLeft, toLeft)

	for {
		value, ok, err := try()
		if err != nil {
			return result{}, err
		}
		if ok {
			return result{reply: bulkReply(value), mutated: true, rewrite: rewrite}, nil
		}
		if c.executing || c.srv.shuttingDown.Load() {
			return result{reply: null}, nil
		}

		waiter := c.srv.data.Wait([]string{source})
		value, ok, err = try()
		if err != nil {
			c.srv.data.CancelWait(waiter)
			return result{}, err
		}
		if ok {
			c.srv.data.CancelWait(waiter)
			return result{reply: bulkReply(value), mutated: true, rewrite: rewrite}, nil
		}

		select {
		case <-waiter.C:
			c.srv.data.CancelWait(waiter)
		case <-timeoutCh:
			c.srv.data.CancelWait(waiter)
			return result{reply: null}, nil
		}
	}
}

func parseMoveDirection(arg []byte) (bool, error) {
	switch strings.ToUpper(string(arg)) {
	case "LEFT":
		return true, nil
	case "RIGHT":
		return false, nil
	default:
		return false, fmt.Errorf("ERR syntax error")
	}
}

func moveDirectionName(left bool) string {
	if left {
		return "LEFT"
	}
	return "RIGHT"
}
