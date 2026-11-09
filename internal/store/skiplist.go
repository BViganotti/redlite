package store

import "math/rand/v2"

// A skiplist ordered by (score, member). It mirrors the structure Redis uses:
// O(log n) insert, delete and rank, with spans so rank queries do not walk the
// whole list.
const (
	skiplistMaxLevel = 32
	skiplistP        = 0.25
)

type skipLevel struct {
	forward *skipNode
	span    int
}

type skipNode struct {
	member   string
	score    float64
	backward *skipNode
	levels   []skipLevel
}

type skiplist struct {
	head   *skipNode
	tail   *skipNode
	level  int
	length int
}

func newSkiplist() *skiplist {
	return &skiplist{
		head:  &skipNode{levels: make([]skipLevel, skiplistMaxLevel)},
		level: 1,
	}
}

func (z *skiplist) randomLevel() int {
	level := 1
	for level < skiplistMaxLevel && rand.Float64() < skiplistP {
		level++
	}
	return level
}

// less orders by score, then lexicographically by member.
func less(aScore float64, aMember string, bScore float64, bMember string) bool {
	return aScore < bScore || (aScore == bScore && aMember < bMember)
}

func lessOrEqual(aScore float64, aMember string, bScore float64, bMember string) bool {
	return aScore < bScore || (aScore == bScore && aMember <= bMember)
}

func (z *skiplist) insert(member string, score float64) {
	var (
		update [skiplistMaxLevel]*skipNode
		rank   [skiplistMaxLevel]int
	)

	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		if i == z.level-1 {
			rank[i] = 0
		} else {
			rank[i] = rank[i+1]
		}
		for x.levels[i].forward != nil &&
			less(x.levels[i].forward.score, x.levels[i].forward.member, score, member) {
			rank[i] += x.levels[i].span
			x = x.levels[i].forward
		}
		update[i] = x
	}

	level := z.randomLevel()
	if level > z.level {
		for i := z.level; i < level; i++ {
			rank[i] = 0
			update[i] = z.head
			update[i].levels[i].span = z.length
		}
		z.level = level
	}

	node := &skipNode{member: member, score: score, levels: make([]skipLevel, level)}
	for i := 0; i < level; i++ {
		node.levels[i].forward = update[i].levels[i].forward
		update[i].levels[i].forward = node
		node.levels[i].span = update[i].levels[i].span - (rank[0] - rank[i])
		update[i].levels[i].span = (rank[0] - rank[i]) + 1
	}
	for i := level; i < z.level; i++ {
		update[i].levels[i].span++
	}

	if update[0] != z.head {
		node.backward = update[0]
	}
	if node.levels[0].forward != nil {
		node.levels[0].forward.backward = node
	} else {
		z.tail = node
	}
	z.length++
}

// delete removes an exact (score, member) pair.
func (z *skiplist) delete(member string, score float64) bool {
	var update [skiplistMaxLevel]*skipNode

	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		for x.levels[i].forward != nil &&
			less(x.levels[i].forward.score, x.levels[i].forward.member, score, member) {
			x = x.levels[i].forward
		}
		update[i] = x
	}

	target := x.levels[0].forward
	if target == nil || target.score != score || target.member != member {
		return false
	}

	for i := 0; i < z.level; i++ {
		if update[i].levels[i].forward == target {
			update[i].levels[i].span += target.levels[i].span - 1
			update[i].levels[i].forward = target.levels[i].forward
		} else {
			update[i].levels[i].span--
		}
	}

	if target.levels[0].forward != nil {
		target.levels[0].forward.backward = target.backward
	} else {
		z.tail = target.backward
	}
	for z.level > 1 && z.head.levels[z.level-1].forward == nil {
		z.level--
	}
	z.length--
	return true
}

// rank returns the 0-based rank of an exact (score, member) pair, or -1.
func (z *skiplist) rank(member string, score float64) int {
	var traversed int
	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		for x.levels[i].forward != nil &&
			lessOrEqual(x.levels[i].forward.score, x.levels[i].forward.member, score, member) {
			traversed += x.levels[i].span
			x = x.levels[i].forward
		}
		if x != z.head && x.member == member && x.score == score {
			return traversed - 1
		}
	}
	return -1
}

// nodeByRank returns the node at a 0-based rank.
func (z *skiplist) nodeByRank(rank int) *skipNode {
	if rank < 0 || rank >= z.length {
		return nil
	}
	target := rank + 1
	traversed := 0
	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		for x.levels[i].forward != nil && traversed+x.levels[i].span <= target {
			traversed += x.levels[i].span
			x = x.levels[i].forward
		}
		if traversed == target {
			return x
		}
	}
	return nil
}

// firstInScoreRange returns the first node with score >= min.
func (z *skiplist) firstInScoreRange(min float64, exclusive bool) *skipNode {
	x := z.head
	for i := z.level - 1; i >= 0; i-- {
		for x.levels[i].forward != nil &&
			!scoreAtLeast(x.levels[i].forward.score, min, exclusive) {
			x = x.levels[i].forward
		}
	}
	node := x.levels[0].forward
	if node == nil || !scoreAtLeast(node.score, min, exclusive) {
		return nil
	}
	return node
}

func scoreAtLeast(score, min float64, exclusive bool) bool {
	if exclusive {
		return score > min
	}
	return score >= min
}

// all returns every node in order.
func (z *skiplist) all() []ScoreMember {
	out := make([]ScoreMember, 0, z.length)
	for node := z.head.levels[0].forward; node != nil; node = node.levels[0].forward {
		out = append(out, ScoreMember{Score: node.score, Member: node.member})
	}
	return out
}
