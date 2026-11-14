package web

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"restic-repo-browser/internal/domain"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 25, "short"},
		{strings.Repeat("a", 25), 25, strings.Repeat("a", 25)},
		{strings.Repeat("a", 26), 25, strings.Repeat("a", 25) + "…"},
		{strings.Repeat("ä", 30), 25, strings.Repeat("ä", 25) + "…"}, // cuts characters, not bytes
		{"", 25, ""},
	}
	for _, tc := range tests {
		if got := truncate(tc.in, tc.n); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// render executes a named template of the page set with data.
func render(t *testing.T, name string, data any) string {
	t.Helper()
	s := &Server{Now: time.Now}
	pages, err := loadTemplates(s.funcs())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pages["overview"].ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTagShowsFullTextOnHover(t *testing.T) {
	long := strings.Repeat("x", 40)
	got := render(t, "tag", long)
	wantContains(t, "long tag", got, `title="`+long+`"`, strings.Repeat("x", 25)+"…")
	if strings.Contains(strings.SplitN(got, ">", 2)[1], long) {
		t.Errorf("tag text is not truncated: %s", got)
	}
	got = render(t, "tag", "backup")
	wantContains(t, "short tag", got, `title="backup"`, ">backup<")
}

func TestPathsAreExpandable(t *testing.T) {
	one := render(t, "paths", []string{"/home"})
	if strings.Contains(one, "<details") {
		t.Errorf("a single path should not be expandable: %s", one)
	}
	wantContains(t, "single path", one, "/home")

	many := render(t, "paths", []string{"/home", "/etc", "/var"})
	wantContains(t, "several paths", many, `<details class="paths">`, "and 2 more", "show less", ">/etc<", ">/var<")
	// "and 2 more" belongs to the summary; "show less" comes after every path.
	summaryEnd := strings.Index(many, "</summary>")
	if i := strings.Index(many, "and 2 more"); i < 0 || i > summaryEnd {
		t.Errorf("\"and 2 more\" is not inside the summary: %s", many)
	}
	if less := strings.Index(many, "show less"); less < strings.Index(many, ">/var<") {
		t.Errorf("\"show less\" must come after the last path: %s", many)
	}
}

func TestSparkJSONHasSnapshotIDs(t *testing.T) {
	snaps := []domain.Snapshot{
		{ShortID: "aaaa1111", Time: time.Unix(1000, 0)},
		{ShortID: "bbbb2222", Time: time.Unix(2000, 0)},
		{ShortID: "cccc3333", Time: time.Unix(3000, 0)},
	}
	got := sparkJSON(snaps)
	if !strings.Contains(got, `"s":["bbbb2222","cccc3333"]`) {
		t.Errorf("spark json = %s", got)
	}
}
