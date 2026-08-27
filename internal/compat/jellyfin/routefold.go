package jellyfin

import (
	"fmt"
	"net/http"
	"strings"
)

// Case-insensitive route matching.
//
// A real Jellyfin server matches paths without regard to case — /system/info/public
// and /SYSTEM/INFO/PUBLIC both answer 200 on the reference server — because
// ASP.NET's routing is case-insensitive. Go's net/http mux is not. VidHub sends
// lowercase /emby/videos/{id}/stream.mkv and got a 404 for it.
//
// Lowercasing the whole path is not an option: it would corrupt every value a
// path carries. Item ids are hex and a client may echo back the casing it read
// elsewhere; container extensions ride in the same path. Only the LITERAL
// segments of a route may be folded, and the PARAMETER segments have to pass
// through byte for byte.
//
// Knowing which is which means matching against the route shapes, so the fold
// is a trie built from the registered patterns. A flat map of lowercased path
// to canonical path cannot work: parameters make the set of paths infinite.

// routeTable registers handlers and remembers the patterns.
//
// The trie is built from what was actually registered rather than from a
// second list maintained by hand, which would drift from the mux the first
// time somebody added a route and forgot.
type routeTable struct {
	mux      *http.ServeMux
	patterns []string
}

func newRouteTable() *routeTable {
	return &routeTable{mux: http.NewServeMux()}
}

// handle registers one "METHOD /Path/{param}" pattern.
func (t *routeTable) handle(pattern string, handler http.HandlerFunc) {
	t.patterns = append(t.patterns, pattern)
	t.mux.HandleFunc(pattern, handler)
}

// foldNode is one path segment in the routing trie.
type foldNode struct {
	// literals maps a lowercased segment to its child. Each child knows the
	// canonical spelling to rewrite to.
	literals map[string]*foldNode
	// canonical is the spelling this segment was registered with.
	canonical string
	// wildcard is the child for a {param} segment, if any. Parameters are
	// never folded, so the node carries no canonical form.
	wildcard *foldNode
	// terminal marks a node that completes at least one registered route.
	terminal bool
}

func newFoldNode() *foldNode {
	return &foldNode{literals: map[string]*foldNode{}}
}

// buildFoldTrie assembles the trie from registered patterns.
//
// It panics on a casing conflict — two patterns registering literals in the
// same position that differ only by case. That is a programming error with no
// correct resolution: the fold would have to pick one spelling and silently
// route the other somewhere unintended. Routes() runs at startup, so this
// fails loudly at boot rather than quietly in production.
func buildFoldTrie(patterns []string) *foldNode {
	root := newFoldNode()

	for _, pattern := range patterns {
		// "GET /Items/{id}" — the method plays no part in path folding.
		path := pattern
		if _, rest, found := strings.Cut(pattern, " "); found {
			path = rest
		}

		node := root
		for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
			if segment == "" {
				continue
			}

			if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
				if node.wildcard == nil {
					node.wildcard = newFoldNode()
				}
				node = node.wildcard
				continue
			}

			key := strings.ToLower(segment)
			child, seen := node.literals[key]
			if !seen {
				child = newFoldNode()
				child.canonical = segment
				node.literals[key] = child
			} else if child.canonical != segment {
				panic(fmt.Sprintf(
					"compat route casing conflict: %q and %q differ only by case; "+
						"the fold cannot choose between them", child.canonical, segment))
			}
			node = child
		}
		node.terminal = true
	}
	return root
}

// fold rewrites a path's literal segments into their registered spelling.
//
// Returns the path unchanged when no registered route matches. Folding must
// never invent a match: an unknown path has to keep reaching the mux and
// getting an honest 404.
func (root *foldNode) fold(path string) string {
	if path == "" || path == "/" {
		return path
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")

	canonical, ok := root.walk(segments)
	if !ok {
		return path
	}

	folded := "/" + strings.Join(canonical, "/")
	if folded == path {
		return path
	}
	return folded
}

// walk resolves segments against the trie, returning their canonical spelling.
//
// The literal branch is tried first, matching net/http's own precedence, so
// /Users/Me continues to win over /Users/{userId}. It BACKTRACKS to the
// wildcard branch when the literal branch fails to complete: a path whose
// leading segments match a literal route but whose tail only exists under a
// parameter would otherwise be abandoned. /Items/Latest matches a literal
// route; /Items/Latest/Intros only exists as /Items/{id}/Intros.
func (n *foldNode) walk(segments []string) ([]string, bool) {
	if len(segments) == 0 {
		if n.terminal {
			return nil, true
		}
		return nil, false
	}

	head, tail := segments[0], segments[1:]

	if child, found := n.literals[strings.ToLower(head)]; found {
		if rest, ok := child.walk(tail); ok {
			return append([]string{child.canonical}, rest...), true
		}
	}

	if n.wildcard != nil {
		if rest, ok := n.wildcard.walk(tail); ok {
			// Verbatim. This segment is a value, and rewriting it would
			// corrupt an id or an extension.
			return append([]string{head}, rest...), true
		}
	}
	return nil, false
}
