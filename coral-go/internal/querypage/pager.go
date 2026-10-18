// Package querypage cuts one page, and says whether there are pages on either side of it, out of an ordered stream of matches (#4745).
// The search routes (board messages, agent events) read their rows once in the query's order and feed each match to a Pager; the page is
// the first Limit matches after the cursor, or, for a "previous" cursor, the last Limit before it, and the total counts every match.
package querypage

// Pager collects a page as the matches go by. Set Limit, and After and AtCursor when there is a cursor, then Add each match in order.
type Pager[T any] struct {
	Limit int
	// After reports whether an item comes after the cursor in the order (nil: there is no cursor, the page is the first). AtCursor
	// reports whether it is the cursor's own item; Prev asks for the page before the cursor instead of the one after it.
	After    func(T) bool
	AtCursor func(T) bool
	Prev     bool

	Page            []T
	Total           int
	before, afterPg int
}

// Add takes the next match in the order.
func (p *Pager[T]) Add(item T) {
	p.Total++
	isAfter := p.After != nil && p.After(item)
	switch {
	case p.After == nil || (!p.Prev && isAfter):
		// the first Limit matches after the cursor (or from the start); the rest only count
		if len(p.Page) < p.Limit {
			p.Page = append(p.Page, item)
		} else {
			p.afterPg++
		}
	case !p.Prev:
		// a match at or before the cursor, on the way to a later page
		p.before++
	case isAfter || (p.AtCursor != nil && p.AtCursor(item)):
		// the previous page: the cursor's own place and what follows it
		p.afterPg++
	default:
		// the previous page: a match before the cursor; the last Limit of them are kept
		if len(p.Page) == p.Limit {
			p.Page = p.Page[1:]
			p.before++
		}
		p.Page = append(p.Page, item)
	}
}

// HasNext says there are matches after the page; HasPrev, that there are matches before it.
func (p *Pager[T]) HasNext() bool { return len(p.Page) > 0 && p.afterPg > 0 }
func (p *Pager[T]) HasPrev() bool { return len(p.Page) > 0 && p.before > 0 }
