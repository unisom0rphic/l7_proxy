package routing

type trieNode struct {
	children [256]*trieNode
	size     int
	end      bool
}

func newNode() *trieNode {
	return &trieNode{}
}

type trie struct {
	root       *trieNode
	countWords int
}

func (tr *trie) Insert(word string) {
	cur := tr.root
	for c := 0; c < len(word); c++ {
		if cur.children[word[c]] == nil {
			cur.children[word[c]] = newNode()
			cur.size++
		}
		cur = cur.children[word[c]]
	}
	if !cur.end {
		cur.end = true
		tr.countWords++
	}
}

func (tr *trie) Search(word string) bool {
	if word == "" {
		return true
	}
	cur := tr.root
	for c := 0; c < len(word); c++ {
		if cur.children[word[c]] == nil {
			return false
		}
		cur = cur.children[word[c]]
	}
	return cur.end
}

func (tr *trie) StartsWith(prefix string) bool {
	cur := tr.root
	for c := 0; c < len(prefix); c++ {
		if cur.children[prefix[c]] == nil {
			return false
		}
		cur = cur.children[prefix[c]]
	}
	return true
}

type Triplet struct {
	node  *trieNode
	ch    byte
	child *trieNode
}

func (tr *trie) Delete(word string) bool {
	if word == "" {
		if tr.root.end {
			tr.root.end = false
			return true
		}
		return false
	}

	path := make([]Triplet, 0, len(word))
	node := tr.root
	for c := 0; c < len(word); c++ {
		child := node.children[word[c]]
		if child == nil {
			return false
		}
		path = append(path, Triplet{node, word[c], child})
		node = child
	}

	if !node.end {
		return false
	}

	node.end = false

	for i := len(path) - 1; i >= 0; i-- {
		t := path[i]
		if t.child.end || t.child.size > 0 {
			break
		}
		t.node.children[t.ch] = nil
		t.node.size--
	}

	return true
}

func (tr *trie) CountWords() int {
	return tr.countWords
}

func newTrie() trie {
	return trie{
		root: newNode(),
	}
}
