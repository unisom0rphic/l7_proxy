package routing

import "testing"

func assertSearch(t *testing.T, tr *Trie, in string, want bool) {
	t.Helper()
	if got := tr.Search(in); got != want {
		t.Errorf("Search(%q)=%v want %v", in, got, want)
	}
}

func assertStartsWith(t *testing.T, tr *Trie, prefix string, want bool) {
	t.Helper()
	if got := tr.StartsWith(prefix); got != want {
		t.Errorf("StartsWith(%q)=%v want %v", prefix, got, want)
	}
}

func assertDelete(t *testing.T, tr *Trie, word string, wantErr bool) {
	t.Helper()
	ok := tr.Delete(word)
	if wantErr && ok {
		t.Errorf("Delete(%q)=ok, want not ok", word)
	}
	if !wantErr && !ok {
		t.Errorf("Delete(%q)=%v, want ok", word, ok)
	}
}

func TestTrie_Basic(t *testing.T) {
	Trie := NewTrie()
	Trie.Insert("a")
	for _, word := range []string{"word1", "somethingelse", "x", "12345", "99test", "test99"} {
		Trie.Insert(word)
	}

	cases := []struct {
		in   string
		want bool
	}{
		{"a", true},
		{"b", false},
		{"", true},
		{"word1", true},
		{"somethingelse", true},
		{"x", true},
		{"12345", true},
		{"99test", true},
		{"test99", true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assertSearch(t, &Trie, tc.in, tc.want)
		})
	}
}

func TestTrie_StartsWith(t *testing.T) {
	tr := NewTrie()
	tr.Insert("cart")
	tr.Insert("cat")

	cases := []struct {
		in   string
		want bool
	}{
		{"ca", true},
		{"ca", true},
		{"car", true},
		{"cart", true},
		{"cat", true},
		{"c", true},
		{"", true},
		{"b", false},
		{"cartx", false},
		{"carts", false},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assertStartsWith(t, &tr, tc.in, tc.want)
		})
	}

	empty := NewTrie()
	assertStartsWith(t, &empty, "", true)
	assertStartsWith(t, &empty, "x", false)
}

func TestTrie_Duplicates(t *testing.T) {
	t.Run("single duplicate", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("hello")
		want := tr.Size()
		tr.Insert("hello")
		if got := tr.Size(); got != want {
			t.Errorf("Size after duplicate = %d, want %d", got, want)
		}
		assertSearch(t, &tr, "hello", true)
	})

	t.Run("many duplicates", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("hello")
		want := tr.Size()
		for i := 0; i < 1000; i++ {
			tr.Insert("hello")
		}
		if got := tr.Size(); got != want {
			t.Errorf("Size after 1000 duplicates = %d, want %d", got, want)
		}
		assertSearch(t, &tr, "hello", true)
	})

	t.Run("duplicate of prefix word", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("car")
		tr.Insert("cart")
		want := tr.Size()
		tr.Insert("car")
		tr.Insert("cart")
		if got := tr.Size(); got != want {
			t.Errorf("Size after duplicates = %d, want %d", got, want)
		}
		assertSearch(t, &tr, "car", true)
		assertSearch(t, &tr, "cart", true)
	})
}

func TestTrie_Deletion(t *testing.T) {
	t.Run("delete existing", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("hello")
		assertDelete(t, &tr, "hello", false)
		assertSearch(t, &tr, "hello", false)
	})

	t.Run("delete non existing", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("hello")
		assertDelete(t, &tr, "world", true)
		assertSearch(t, &tr, "hello", true)
	})

	t.Run("delete from empty Trie", func(t *testing.T) {
		tr := NewTrie()
		assertDelete(t, &tr, "whatever", true)
		assertSearch(t, &tr, "whatever", false)
	})

	t.Run("delete word with prefix", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("car")
		tr.Insert("cart")
		assertDelete(t, &tr, "cart", false)
		assertSearch(t, &tr, "car", true)
		assertSearch(t, &tr, "cart", false)
	})

	t.Run("delete from group with same prefix", func(t *testing.T) {
		tr := NewTrie()
		for _, w := range []string{"cat", "car", "cart"} {
			tr.Insert(w)
		}
		assertDelete(t, &tr, "car", false)
		assertSearch(t, &tr, "cat", true)
		assertSearch(t, &tr, "car", false)
		assertSearch(t, &tr, "cart", true)
	})

	t.Run("delete same word twice", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("hello")
		assertDelete(t, &tr, "hello", false)
		assertDelete(t, &tr, "hello", true) // second time must return error
		assertSearch(t, &tr, "hello", false)
	})

	t.Run("delete then insert", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("hello")
		assertDelete(t, &tr, "hello", false)
		assertSearch(t, &tr, "hello", false)
		tr.Insert("hello")
		assertSearch(t, &tr, "hello", true)
	})

	t.Run("delete all words with same prefix cleans prefix", func(t *testing.T) {
		tr := NewTrie()
		words := []string{"cat", "car", "cart"}
		for _, w := range words {
			tr.Insert(w)
		}
		assertStartsWith(t, &tr, "ca", true)

		for _, w := range words {
			assertDelete(t, &tr, w, false)
		}
		for _, w := range words {
			assertSearch(t, &tr, w, false)
		}
		// prefix node should have been pruned
		assertStartsWith(t, &tr, "ca", false)
		assertStartsWith(t, &tr, "c", false)
	})

	t.Run("delete one of two branches keeps the other", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("cat")
		tr.Insert("car")
		assertDelete(t, &tr, "cat", false)
		assertSearch(t, &tr, "cat", false)
		assertSearch(t, &tr, "car", true)
		assertStartsWith(t, &tr, "ca", true)
	})

	t.Run("delete prefix word does not delete longer word", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("car")
		tr.Insert("cart")
		assertDelete(t, &tr, "car", false)
		assertSearch(t, &tr, "car", false)
		assertSearch(t, &tr, "cart", true)
	})
}

func TestTrie_EdgeCases(t *testing.T) {
	t.Run("differ in first character", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("cat")
		tr.Insert("bat")
		assertSearch(t, &tr, "cat", true)
		assertSearch(t, &tr, "bat", true)
		assertSearch(t, &tr, "aat", false)
	})

	t.Run("differ in last character", func(t *testing.T) {
		tr := NewTrie()
		tr.Insert("cat")
		tr.Insert("car")
		assertSearch(t, &tr, "cat", true)
		assertSearch(t, &tr, "car", true)
		assertSearch(t, &tr, "caw", false)
	})

	t.Run("single character words", func(t *testing.T) {
		tr := NewTrie()
		for _, w := range []string{"a", "b", "z"} {
			tr.Insert(w)
		}
		assertSearch(t, &tr, "a", true)
		assertSearch(t, &tr, "b", true)
		assertSearch(t, &tr, "z", true)
		assertSearch(t, &tr, "c", false)
		assertDelete(t, &tr, "b", false)
		assertSearch(t, &tr, "b", false)
		assertSearch(t, &tr, "a", true)
		assertSearch(t, &tr, "z", true)
	})

	t.Run("empty strings", func(t *testing.T) {
		tr := NewTrie()
		// Search("") = true is an agreement
		assertSearch(t, &tr, "", true)
		// inserting empty string must not break the Trie
		tr.Insert("")
		assertSearch(t, &tr, "", true)
		// other words still work
		tr.Insert("x")
		assertSearch(t, &tr, "x", true)
		assertSearch(t, &tr, "", true)
	})
}
