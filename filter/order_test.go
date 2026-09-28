package filter

import (
	"testing"
)

// producer is a broad filter (low specificity) that moves INBOX mail to dest.
func producer(name, dest string) Filter {
	return Filter{
		Name:       name,
		Conditions: []Condition{{Field: "from", Operator: "contains", Value: "@example.com"}},
		Actions:    []Action{{Type: "move", Value: dest}},
	}
}

// consumer is a narrow filter (high specificity) reading from the given mailboxes.
func consumer(name string, mailboxes ...string) Filter {
	return Filter{
		Name:           name,
		Mailboxes:      mailboxes,
		ConditionsType: "and",
		Conditions: []Condition{
			{Field: "from", Operator: "equals", Value: "a@example.com"},
			{Field: "subject", Operator: "equals", Value: "hello"},
			{Field: "to", Operator: "equals", Value: "me@example.com"},
		},
		Actions: []Action{{Type: "mark_as_read"}},
	}
}

func sortedNames(t *testing.T, filters []Filter) []string {
	t.Helper()
	sorted, ok := TopologicalSort(BuildDependencyGraph(filters))
	if !ok {
		t.Fatal("unexpected cycle")
	}
	names := make([]string, len(sorted))
	for i, f := range sorted {
		names[i] = f.Name
	}
	return names
}

func indexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

func assertBefore(t *testing.T, names []string, first, second string) {
	t.Helper()
	fi, si := indexOf(names, first), indexOf(names, second)
	if fi < 0 || si < 0 {
		t.Fatalf("missing filter in %v", names)
	}
	if fi > si {
		t.Errorf("want %q before %q, got %v", first, second, names)
	}
}

func TestTopologicalSort_DependencyBeatsSpecificity(t *testing.T) {
	tests := []struct {
		name    string
		filters []Filter
		order   [][2]string // pairs that must be ordered first-before-second
	}{
		{
			name:    "producer before more specific consumer",
			filters: []Filter{consumer("B", "X"), producer("A", "X")},
			order:   [][2]string{{"A", "B"}},
		},
		{
			name: "chain propagates depth",
			filters: []Filter{
				consumer("C", "Y"),
				{Name: "B", Mailboxes: []string{"X"},
					Conditions: []Condition{{Field: "from", Operator: "contains", Value: "@example.com"}},
					Actions:    []Action{{Type: "move", Value: "Y"}}},
				producer("A", "X"),
			},
			order: [][2]string{{"A", "B"}, {"B", "C"}},
		},
		{
			name:    "consumer with no mailboxes reads INBOX",
			filters: []Filter{consumer("B"), producer("A", "INBOX")},
			order:   [][2]string{{"A", "B"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names := sortedNames(t, tt.filters)
			for _, p := range tt.order {
				assertBefore(t, names, p[0], p[1])
			}
		})
	}
}

func TestTopologicalSort_SpecificityWithinSameLevel(t *testing.T) {
	broad := producer("broad", "X")
	narrow := consumer("narrow") // reads INBOX, independent of broad
	names := sortedNames(t, []Filter{broad, narrow})
	assertBefore(t, names, "narrow", "broad")
}

func TestBuildDependencyGraph_DuplicateNamesKeepAllFilters(t *testing.T) {
	filters := []Filter{producer("same", "X"), producer("same", "Y"), producer("", "Z"), producer("", "W")}
	names := sortedNames(t, filters)
	if len(names) != len(filters) {
		t.Fatalf("want %d filters, got %d: %v", len(filters), len(names), names)
	}
}

func edgeExists(nodes map[string]*Node, from, to string) bool {
	for _, n := range nodes {
		if n.Filter.Name != from {
			continue
		}
		for _, e := range n.Edges {
			if e.Filter.Name == to {
				return true
			}
		}
	}
	return false
}

func TestBuildDependencyGraph_Edges(t *testing.T) {
	tests := []struct {
		name     string
		producer Filter
		consumer Filter
		want     bool
	}{
		{
			name:     "exact mailbox",
			producer: producer("A", "Archive"),
			consumer: consumer("B", "Archive"),
			want:     true,
		},
		{
			name:     "move type is case-insensitive",
			producer: Filter{Name: "A", Actions: []Action{{Type: "Move", Value: "Archive"}}},
			consumer: consumer("B", "Archive"),
			want:     true,
		},
		{
			name:     "consumer glob matches move target",
			producer: producer("A", "Archive/2026"),
			consumer: consumer("B", "Archive/*"),
			want:     true,
		},
		{
			name:     "unrelated mailbox",
			producer: producer("A", "Archive"),
			consumer: consumer("B", "Spam"),
			want:     false,
		},
		{
			name:     "no self edge",
			producer: Filter{Name: "A", Mailboxes: []string{"X"}, Actions: []Action{{Type: "move", Value: "X"}}},
			consumer: consumer("B", "Other"),
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := BuildDependencyGraph([]Filter{tt.producer, tt.consumer})
			if got := edgeExists(nodes, "A", "B"); got != tt.want {
				t.Errorf("edge A->B = %v, want %v", got, tt.want)
			}
			if edgeExists(nodes, "A", "A") {
				t.Error("unexpected self edge")
			}
		})
	}
}

func TestTopologicalSort_DetectsCycle(t *testing.T) {
	a := Filter{Name: "A", Mailboxes: []string{"X"}, Actions: []Action{{Type: "move", Value: "Y"}}}
	b := Filter{Name: "B", Mailboxes: []string{"Y"}, Actions: []Action{{Type: "move", Value: "X"}}}
	if _, ok := TopologicalSort(BuildDependencyGraph([]Filter{a, b})); ok {
		t.Fatal("expected cycle to be reported")
	}
}

func TestBuildDependencyGraph_OneMoveFeedsManyConsumers(t *testing.T) {
	names := sortedNames(t, []Filter{consumer("B", "X"), consumer("C", "X"), producer("A", "X")})
	assertBefore(t, names, "A", "B")
	assertBefore(t, names, "A", "C")
}
