package tagger

type PrefixTree struct {
	Next  [256]*PrefixTree
	Rules []*Tag
}

func (t *PrefixTree) Add(prefix []byte, rule *Tag) {
	x := t

	for i := 0; i < len(prefix); i++ {
		if x.Next[prefix[i]] == nil {
			x.Next[prefix[i]] = &PrefixTree{}
		}

		x = x.Next[prefix[i]]
	}

	if x.Rules == nil {
		x.Rules = make([]*Tag, 0)
	}

	x.Rules = append(x.Rules, rule)
}
