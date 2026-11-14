package web

// defaultPerPage is the page size of the snapshot list.
const defaultPerPage = 15

// Page is one page of a result list.
type Page[T any] struct {
	Items      []T
	Number     int // 1-based, clamped to the valid range
	Pages      int
	PerPage    int
	Total      int
	FirstIndex int // 1-based index of the first item on this page, 0 when empty
}

// Paginate returns page number (1-based) of items.
func Paginate[T any](items []T, number, perPage int) Page[T] {
	if perPage < 1 {
		perPage = defaultPerPage
	}
	pages := max(1, (len(items)+perPage-1)/perPage)
	number = min(max(number, 1), pages)
	start := (number - 1) * perPage
	end := min(start+perPage, len(items))
	p := Page[T]{Items: items[start:end], Number: number, Pages: pages, PerPage: perPage, Total: len(items)}
	if len(items) > 0 {
		p.FirstIndex = start + 1
	}
	return p
}

// LastIndex is the 1-based index of the last item on the page.
func (p Page[T]) LastIndex() int {
	if len(p.Items) == 0 {
		return 0
	}
	return p.FirstIndex + len(p.Items) - 1
}

// PageLinks lists the page numbers to render, with 0 standing for an ellipsis.
func (p Page[T]) PageLinks() []int {
	set := map[int]bool{1: true, p.Pages: true, p.Number - 1: true, p.Number: true, p.Number + 1: true}
	if p.Number <= 3 {
		set[2], set[3], set[4] = true, true, true
	}
	if p.Number >= p.Pages-2 {
		set[p.Pages-1], set[p.Pages-2], set[p.Pages-3] = true, true, true
	}
	var nums []int
	for n := 1; n <= p.Pages; n++ {
		if set[n] {
			nums = append(nums, n)
		}
	}
	var out []int
	for i, n := range nums {
		if i > 0 {
			switch gap := n - nums[i-1]; {
			case gap == 2:
				out = append(out, n-1)
			case gap > 2:
				out = append(out, 0)
			}
		}
		out = append(out, n)
	}
	return out
}
