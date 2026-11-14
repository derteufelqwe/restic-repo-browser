package web

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"restic-repo-browser/internal/domain"
)

// Some snapshots record backup paths that do not exist in their file tree (for
// example when the backup ran in a container with the folder mounted elsewhere).
// The explorer does not look at the recorded paths at all: it shows what restic ls
// returns, starting at the root of the tree.
func TestExplorerIgnoresRecordedPaths(t *testing.T) {
	for name, recorded := range map[string][]string{
		"one path":  {"/home/arne/docker/app/metadata/backups"},
		"two paths": {"/home/arne/docker/app/metadata/backups", "/home/arne/docker/app/config"},
		"no paths":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.login()
			ctx := context.Background()
			snaps, err := e.store.Snapshots(ctx, e.repoID)
			must(t, err)
			snap := mostRecent(snaps)
			base := fmt.Sprintf("/repos/%d/snapshots/%s", e.repoID, snap.ID)
			top := topFolder(treePath(snap.Paths[0]))
			browser := regexp.MustCompile(`(?s)<div class="browser">.*?</section>`)

			_, before := e.get(base)
			want := browser.FindString(before)
			if want == "" {
				t.Fatal("no browser section in the page")
			}

			for i := range snaps {
				snaps[i].Paths = recorded
			}
			// A sync does not update paths: delete the rows, then insert them again.
			must(t, e.store.SyncSnapshots(ctx, e.repoID, nil))
			must(t, e.store.SyncSnapshots(ctx, e.repoID, snaps))

			_, after := e.get(base)
			wantContains(t, "explorer start", after, `data-path="/"`, "Snapshot root", `data-name="`+top+`"`)
			if got := browser.FindString(after); got != want {
				t.Errorf("recorded paths changed what the explorer shows:\n%s", got)
			}
			for _, p := range recorded {
				if strings.Contains(browser.FindString(after), p) {
					t.Errorf("the recorded path %s appears in the browser", p)
				}
			}
		})
	}
}

func mostRecent(snaps []domain.Snapshot) domain.Snapshot {
	best := snaps[0]
	for _, s := range snaps {
		if s.Time.After(best.Time) {
			best = s
		}
	}
	return best
}
