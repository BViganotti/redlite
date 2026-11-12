package server

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

type redisCommandInfo struct {
	Name     string   `json:"name"`
	Arity    int64    `json:"arity"`
	Flags    []string `json:"flags"`
	FirstKey int64    `json:"first_key"`
	LastKey  int64    `json:"last_key"`
	Step     int64    `json:"step"`
}

type redisInventory struct {
	RedisVersion string             `json:"redis_version"`
	Commands     []redisCommandInfo `json:"commands"`
}

// TestRedisCommandInventory compares the local command table with the fixture
// captured from a pinned Redis. Commands that are not implemented yet must be
// listed in testdata/command-gaps.txt; the test fails when the list drifts in
// either direction. Regenerate the fixture with cmd/redis-inventory and the gap
// list with UPDATE_GAPS=1.
func TestRedisCommandInventory(t *testing.T) {
	data, err := os.ReadFile("testdata/redis-commands.json")
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var inventory redisInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatalf("parse inventory: %v", err)
	}

	gaps := readGapList(t, "testdata/command-gaps.txt")
	known := make(map[string]redisCommandInfo, len(inventory.Commands))
	for _, command := range inventory.Commands {
		known[command.Name] = command
	}

	var (
		problems []string
		missing  []string
	)
	for name, remote := range known {
		local, ok := commandTable[strings.ToUpper(name)]
		if !ok {
			missing = append(missing, name)
			if !gaps[name] {
				problems = append(problems, "missing command not tracked in command-gaps.txt: "+name)
			}
			continue
		}
		if gaps[name] {
			problems = append(problems, "command is implemented but still listed as a gap: "+name)
		}
		if local.arity != int(remote.Arity) {
			problems = append(problems, fmt.Sprintf("%s arity %d, Redis %d", name, local.arity, remote.Arity))
		}
		redisWrite := containsFlag(remote.Flags, "write")
		redisRead := containsFlag(remote.Flags, "readonly")
		if redisWrite != (local.flags&flagWrite != 0) {
			problems = append(problems, fmt.Sprintf("%s write flag: local=%v redis=%v", name, local.flags&flagWrite != 0, redisWrite))
		}
		if redisRead != (local.flags&flagReadOnly != 0) {
			problems = append(problems, fmt.Sprintf("%s readonly flag: local=%v redis=%v", name, local.flags&flagReadOnly != 0, redisRead))
		}
	}

	for name := range commandTable {
		if _, ok := known[strings.ToLower(name)]; !ok {
			problems = append(problems, "command is not part of Redis: "+name)
		}
	}

	sort.Strings(missing)

	if os.Getenv("UPDATE_GAPS") == "1" {
		content := strings.Join(missing, "\n")
		if content != "" {
			content += "\n"
		}
		if err := os.WriteFile("testdata/command-gaps.txt", []byte(content), 0o644); err != nil {
			t.Fatalf("write gap list: %v", err)
		}
		t.Logf("redis %s: %d commands, %d implemented, %d tracked gaps",
			inventory.RedisVersion, len(known), len(known)-len(missing), len(missing))
		return
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("%d parity problems against Redis %s:\n%s",
			len(problems), inventory.RedisVersion, strings.Join(problems, "\n"))
	}

	t.Logf("redis %s: %d commands, %d implemented, %d tracked gaps",
		inventory.RedisVersion, len(known), len(known)-len(missing), len(missing))
}

func readGapList(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}
		}
		t.Fatalf("read gap list: %v", err)
	}
	out := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out[line] = true
		}
	}
	return out
}

func containsFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if flag == want {
			return true
		}
	}
	return false
}
