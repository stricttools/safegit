package trailer

import "sort"

// Projection: following one path across the commits that claim to have moved
// it.
//
// The whole design rests on one rule -- RECORDS ARE CLAIMS, TREES ARE THE
// ARBITER. A record says a move happened; the trees say what is actually there.
// Where they disagree the trees win, always, and the record is passed over
// without comment:
//
//   - a record whose old path no parent of its commit holds claims a move of
//     something that was never there, so it never applies;
//   - a subtree record whose prefix names nothing in any parent is the same
//     claim about a directory, and is passed over the same way;
//   - a record's ANSWER has to survive too. The path a record produces is
//     looked up in the commit's own tree, and a record whose answer is not
//     there did not produce the content that is. This is what settles a merge
//     with no special case for merges: two sides that moved one path to two
//     different names both offer an answer, and the one the merge tree kept is
//     the one that is right.
//
// Longest match wins among the records that do apply, so a file-form record
// beats the subtree record it sits inside. A file-form record NEVER applies to
// a descendant: `a -> b` says nothing about `a/x`, and only the subtree form
// (`a/ -> b/`) speaks for what is underneath.
//
// Nothing is stored about confidence. Whether a record was minted from an
// explicit `safegit mv` or declared with `--moved` changes nothing about what
// it claims, and how well the claim is borne out is recomputed here from the
// trees every time it is asked.

// Tree is the arbiter: the set of paths one commit holds.
type Tree interface {
	// Has reports whether the tree holds this exact path.
	Has(path string) bool
	// HasUnder reports whether the tree holds any path under this prefix. The
	// prefix carries no trailing slash.
	HasUnder(prefix string) bool
}

// PathSet is a Tree over an explicit set of paths.
type PathSet map[string]struct{}

// NewPathSet builds a PathSet from a list of paths.
func NewPathSet(paths ...string) PathSet {
	s := make(PathSet, len(paths))
	for _, p := range paths {
		s[p] = struct{}{}
	}
	return s
}

// Has reports whether the set holds this exact path.
func (s PathSet) Has(path string) bool {
	_, ok := s[path]
	return ok
}

// HasUnder reports whether the set holds any path under this prefix.
func (s PathSet) HasUnder(prefix string) bool {
	with := prefix + "/"
	for p := range s {
		if len(p) > len(with) && p[:len(with)] == with {
			return true
		}
	}
	return false
}

// Commit is one commit as a projection reads it: what it declared, and the
// trees that decide whether the declarations hold.
//
// Parents is every parent's tree, in parent order, and it is empty for a root
// commit -- which is why a root commit's records can never apply: nothing
// preceded it for anything to move from. Tree is the commit's own.
type Commit struct {
	// ID names the commit in a Hop. The projection never resolves it.
	ID      string
	Moves   Moves
	Parents []Tree
	Tree    Tree
}

// Hop is one record the projection applied.
type Hop struct {
	Commit string
	Record Record
	From   string
	To     string
}

// Projection is the answer for one followed path.
type Projection struct {
	// Path is what the path is called at the end of the chain.
	Path string
	// Hops are the records that were applied, oldest first.
	Hops []Hop
	// Present reports whether the last commit's tree holds Path. A projection
	// can land on a path nothing holds -- the content was deleted, or a record
	// claimed a move the trees never confirmed -- and saying so is the honest
	// answer rather than reporting the last name that did exist.
	Present bool
}

// Forward follows path through a chain of commits given oldest first, and
// answers what it is called at the end.
//
// The chain is the caller's: this package walks no history and resolves no
// revision. A caller with a linear range hands over that range; a caller
// following a first-parent line hands over that line, with each commit's OTHER
// parents still listed in Parents so a record made on the side that was merged
// in is recognized as applying to something that existed.
func Forward(path string, chain []Commit) Projection {
	retracted := RetractedIDs(chain)
	p := Projection{Path: path}
	for _, c := range chain {
		to, record, ok := step(c, p.Path, retracted)
		if !ok {
			continue
		}
		p.Hops = append(p.Hops, Hop{Commit: c.ID, Record: record, From: p.Path, To: to})
		p.Path = to
	}
	if len(chain) > 0 {
		p.Present = has(chain[len(chain)-1].Tree, p.Path)
	}
	return p
}

// RetractedIDs collects every record id the chain retracts.
//
// Folding is over the WHOLE chain rather than forward from each retraction: an
// id names exactly one record, so where in the chain the retraction sits cannot
// change which record it names. A retraction naming an id the chain does not
// carry is inert -- the record it retracts may simply be outside the range the
// caller handed over.
func RetractedIDs(chain []Commit) map[string]bool {
	out := make(map[string]bool)
	for _, c := range chain {
		for _, id := range c.Moves.Retractions {
			out[id] = true
		}
	}
	return out
}

// candidate is one record that could apply to a path at one commit.
type candidate struct {
	record Record
	to     string
	// match is the length of the old path or prefix that matched, which is what
	// "longest match wins" compares.
	match int
}

// step applies at most one of a commit's records to a path.
func step(c Commit, path string, retracted map[string]bool) (string, Record, bool) {
	var candidates []candidate
	for _, r := range c.Moves.Records {
		if retracted[r.ID] {
			continue
		}
		cand, ok := claim(c, r, path)
		if !ok {
			continue
		}
		candidates = append(candidates, cand)
	}
	if len(candidates) == 0 {
		return "", Record{}, false
	}
	// Longest match first; among equal matches, the answer that sorts first, so
	// a declaration that is ambiguous on its face still reads the same way for
	// everyone. The tree filter below is what normally decides between them.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].match != candidates[j].match {
			return candidates[i].match > candidates[j].match
		}
		return candidates[i].to < candidates[j].to
	})
	for _, cand := range candidates {
		if has(c.Tree, cand.to) {
			return cand.to, cand.record, true
		}
	}
	return "", Record{}, false
}

// claim reports what one record would answer for a path, and whether it applies
// at all -- which is where the parent trees veto a claim about something that
// was never there.
func claim(c Commit, r Record, path string) (candidate, bool) {
	if r.Subtree() {
		prefix := r.OldPrefix()
		var suffix string
		switch {
		case path == prefix:
			suffix = ""
		case len(path) > len(prefix) && path[:len(prefix)+1] == prefix+"/":
			suffix = path[len(prefix):]
		default:
			return candidate{}, false
		}
		if !anyParentHasUnder(c, prefix) {
			return candidate{}, false
		}
		return candidate{record: r, to: r.NewPrefix() + suffix, match: len(prefix)}, true
	}
	if path != r.Old {
		return candidate{}, false
	}
	if !anyParentHas(c, r.Old) {
		return candidate{}, false
	}
	return candidate{record: r, to: r.New, match: len(r.Old)}, true
}

// anyParentHas reports whether any parent tree holds the path. Any parent, not
// the first: a record made on the side a merge brought in describes a move out
// of a path only that side ever held.
func anyParentHas(c Commit, path string) bool {
	for _, t := range c.Parents {
		if has(t, path) {
			return true
		}
	}
	return false
}

// anyParentHasUnder is anyParentHas for a subtree record's prefix.
func anyParentHasUnder(c Commit, prefix string) bool {
	for _, t := range c.Parents {
		if t != nil && t.HasUnder(prefix) {
			return true
		}
	}
	return false
}

// has asks a tree about a path, treating an absent tree as holding nothing --
// so a caller that could not read one gets "the record is not confirmed"
// rather than a claim taken on trust.
func has(t Tree, path string) bool {
	return t != nil && t.Has(path)
}
