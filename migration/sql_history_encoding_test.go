package migration

import (
	"encoding/json"
	"testing"
)

// Golden values are calculated by an independent Python struct.pack/sha256 oracle.
func TestSQLHistoryBoundaryEncodingVectors(t *testing.T) {
	for _, sample := range []struct {
		up, down []string
		want     string
	}{
		{[]string{"SELECT 1"}, nil, "42ec7b13199a6c537a5e1f7a80c291e312d34c14bafbd79698fc7df2c72c780b"},
		{[]string{"SELECT 1"}, []string{"down"}, "0f99275ade741f5f72182dd60ba6a14e6381aa98994888a7e0617a4a3d630cda"},
		{[]string{"SELECT 1", "down"}, nil, "8a74a63ac9bd0cd1744783391bc2cf093b92505213d87755eea13ad23c6fbfa5"},
		{[]string{"SELECT 1"}, []string{"SELECT 2", "down", "SELECT 3"}, "2c4e7a8a809532f25cdc309d6939561aa300271fc00509e0167b2c83f6fedbbf"},
		{[]string{"SELECT 1", "down", "SELECT 2"}, []string{"SELECT 3"}, "34b2fc1b1c805cc15ea75acfe8564d91b702841e63221991594b2ba0f6635754"},
		{[]string{"down"}, []string{"down", "down"}, "e0b12b70cda6941b807a06043025f7c4eb7b2e7512ec335b6da7ba2bbd5bc6b4"},
		{[]string{"SELECT \xff", "down"}, []string{"SELECT \xfe"}, "e97cdd887090144304f57cf346a4f4e1c2c054610e16ef85ac0f30b8cba770c7"},
		{[]string{"SELECT '中文'"}, []string{"down"}, "550f3ff90be2bf4de5ffa60ba9f456b64b699e67ca39f524038c3481b3a40b06"},
	} {
		if got := mustHistorySQL(t, sample.up, sample.down).Checksum(); got != sample.want {
			t.Fatalf("history checksum=%s want=%s for %q / %q", got, sample.want, sample.up, sample.down)
		}
	}
}

// Exhaust a small structural domain, including repeated direction tokens in both groups.
// The oracle is the pair of arrays, not the hash implementation's own framing.
func TestSQLHistoryBoundarySmallDomainInjectivity(t *testing.T) {
	alphabet := []string{"SELECT 1", "down", "up", "SELECT 'down'"}
	groups := [][]string{nil}
	frontier := [][]string{nil}
	for depth := 0; depth < 3; depth++ {
		var next [][]string
		for _, prefix := range frontier {
			for _, statement := range alphabet {
				sequence := append(append([]string(nil), prefix...), statement)
				next = append(next, sequence)
				groups = append(groups, sequence)
			}
		}
		frontier = next
	}
	seen := make(map[string]string)
	for _, up := range groups[1:] {
		for _, down := range groups {
			logical, err := json.Marshal([2][]string{up, down})
			if err != nil {
				t.Fatal(err)
			}
			digest := mustHistorySQL(t, up, down).Checksum()
			if previous, found := seen[digest]; found && previous != string(logical) {
				t.Fatalf("distinct histories alias: %s / %s", previous, logical)
			}
			seen[digest] = string(logical)
		}
	}
	t.Logf("checked %d distinct normalized definitions", len(seen))
}
