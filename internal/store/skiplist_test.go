package store

import (
	"math/rand/v2"
	"sort"
	"strconv"
	"testing"
)

// modelSorted returns the expected order for a map of scores.
func modelSorted(m map[string]float64) []ScoreMember {
	out := make([]ScoreMember, 0, len(m))
	for member, score := range m {
		out = append(out, ScoreMember{Score: score, Member: member})
	}
	sort.Slice(out, func(i, j int) bool {
		return less(out[i].Score, out[i].Member, out[j].Score, out[j].Member)
	})
	return out
}

func TestSkiplistAgainstModel(t *testing.T) {
	list := newSkiplist()
	model := make(map[string]float64)
	rng := rand.New(rand.NewPCG(1, 2))

	for step := 0; step < 4000; step++ {
		member := "m" + strconv.Itoa(rng.IntN(150))
		if rng.IntN(10) < 3 {
			if score, ok := model[member]; ok {
				if !list.delete(member, score) {
					t.Fatalf("step %d: delete(%q) returned false", step, member)
				}
				delete(model, member)
			}
		} else {
			score := float64(rng.IntN(40))
			if old, ok := model[member]; ok {
				list.delete(member, old)
			}
			list.insert(member, score)
			model[member] = score
		}

		if list.length != len(model) {
			t.Fatalf("step %d: length = %d, want %d", step, list.length, len(model))
		}

		got := list.all()
		want := modelSorted(model)
		if len(got) != len(want) {
			t.Fatalf("step %d: all() returned %d nodes, want %d", step, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("step %d: node %d = %+v, want %+v", step, i, got[i], want[i])
			}
		}

		for i, entry := range want {
			if rank := list.rank(entry.Member, entry.Score); rank != i {
				t.Fatalf("step %d: rank(%q) = %d, want %d", step, entry.Member, rank, i)
			}
			node := list.nodeByRank(i)
			if node == nil || node.member != entry.Member || node.score != entry.Score {
				t.Fatalf("step %d: nodeByRank(%d) = %+v, want %+v", step, i, node, entry)
			}
		}
	}
}

func TestSkiplistMissingRank(t *testing.T) {
	list := newSkiplist()
	list.insert("a", 1)
	list.insert("b", 2)

	if rank := list.rank("missing", 0); rank != -1 {
		t.Fatalf("rank of missing member = %d, want -1", rank)
	}
	if node := list.nodeByRank(5); node != nil {
		t.Fatalf("nodeByRank(5) = %+v, want nil", node)
	}
	if list.delete("missing", 0) {
		t.Fatal("delete of a missing member returned true")
	}
}

func TestSkiplistScoreRange(t *testing.T) {
	list := newSkiplist()
	for i, member := range []string{"a", "b", "c", "d"} {
		list.insert(member, float64(i))
	}

	first := list.firstInScoreRange(1, false)
	if first == nil || first.member != "b" {
		t.Fatalf("first >= 1 = %+v, want b", first)
	}
	first = list.firstInScoreRange(1, true)
	if first == nil || first.member != "c" {
		t.Fatalf("first > 1 = %+v, want c", first)
	}
	first = list.firstInScoreRange(10, false)
	if first != nil {
		t.Fatalf("first >= 10 = %+v, want nil", first)
	}
}
