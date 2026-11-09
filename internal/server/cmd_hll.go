package server

import "fmt"

func init() {
	registerCommand("PFADD", -2, flagWrite|flagFast, cmdPFAdd)
	registerCommand("PFCOUNT", -2, flagReadOnly, cmdPFCount)
	registerCommand("PFMERGE", -2, flagWrite, cmdPFMerge)
}

func cmdPFAdd(c *client, args [][]byte) (result, error) {
	changed, err := c.srv.data.PFAdd(string(args[1]), args[2:])
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(changed), mutated: changed == 1}, nil
}

func cmdPFCount(c *client, args [][]byte) (result, error) {
	keys := make([]string, 0, len(args)-1)
	for _, key := range args[1:] {
		keys = append(keys, string(key))
	}
	n, err := c.srv.data.PFCount(keys)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdPFMerge(c *client, args [][]byte) (result, error) {
	if len(args) < 2 {
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'pfmerge' command")
	}
	sources := make([]string, 0, len(args)-2)
	for _, key := range args[2:] {
		sources = append(sources, string(key))
	}
	if err := c.srv.data.PFMerge(string(args[1]), sources); err != nil {
		return result{}, err
	}
	return result{reply: simpleReply("OK"), mutated: true}, nil
}
