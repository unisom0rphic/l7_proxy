package routing

import "github.com/unisom0rphic/l7proxy/internal/config"

// trie maps path prefixes to the routes registered for them. Nodes are keyed by
// byte, so a lookup walks the request path once, independent of the prefix count.
type trie struct {
	root *trieNode
}

type trieNode struct {
	children [256]*trieNode
	routes   []*config.Route
}

func newTrie() *trie {
	return &trie{root: &trieNode{}}
}

// insert appends route to the node for prefix, preserving insertion order.
func (tr *trie) insert(prefix string, route *config.Route) {
	node := tr.root
	for i := 0; i < len(prefix); i++ {
		child := node.children[prefix[i]]
		if child == nil {
			child = &trieNode{}
			node.children[prefix[i]] = child
		}
		node = child
	}
	node.routes = append(node.routes, route)
}

// match calls yield for every node on the way down path, so the longest registered
// prefix is offered first, and stops as soon as yield returns true. depth is the
// length of the prefix ending at that node.
func (tr *trie) match(path string, yield func(routes []*config.Route, depth int) bool) bool {
	return tr.root.match(path, 0, yield)
}

func (n *trieNode) match(path string, depth int, yield func([]*config.Route, int) bool) bool {
	if depth < len(path) {
		if child := n.children[path[depth]]; child != nil {
			if child.match(path, depth+1, yield) {
				return true
			}
		}
	}
	return len(n.routes) > 0 && yield(n.routes, depth)
}
