package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// The repository dialog takes an optional cron expression for when backups are expected.
func TestBackupScheduleInRepoForm(t *testing.T) {
	e := newEnv(t)
	e.login()
	edit := fmt.Sprintf("/settings/repos/%d", e.repoID)

	_, form := e.hx("GET", edit+"/edit", nil)
	wantContains(t, "edit form", form, `name="backup_cron"`, "Judged from the gaps between snapshots")

	_, frag := e.hx("GET", "/settings/backup-cron?backup_cron="+url.QueryEscape("0 3 * * *"), nil)
	wantContains(t, "explanation", frag, "Every day at 03:00", "Next runs:")
	_, frag = e.hx("GET", "/settings/backup-cron?backup_cron=nonsense", nil)
	wantContains(t, "invalid explanation", frag, "not a valid schedule")

	resp, body := e.hx("POST", edit, url.Values{"name": {"photos"}, "path": {e.repoDir}, "backup_cron": {"nonsense"}, "force": {"1"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid cron = %d", resp.StatusCode)
	}
	wantContains(t, "invalid cron", body, "not a valid schedule", `value="nonsense"`)

	resp, body = e.hx("POST", edit, url.Values{"name": {"photos"}, "path": {e.repoDir}, "backup_cron": {"  0   3 * * 0 "}, "force": {"1"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save = %d\n%s", resp.StatusCode, body)
	}
	repo, err := e.store.Repo(context.Background(), e.repoID)
	must(t, err)
	if repo.BackupCron != "0 3 * * 0" {
		t.Errorf("stored schedule = %q", repo.BackupCron)
	}
	_, form = e.hx("GET", edit+"/edit", nil)
	wantContains(t, "edit form", form, `value="0 3 * * 0"`, "Every Sunday at 03:00")

	// Clearing the field goes back to judging from the snapshot gaps.
	e.hx("POST", edit, url.Values{"name": {"photos"}, "path": {e.repoDir}, "backup_cron": {""}, "force": {"1"}})
	repo, err = e.store.Repo(context.Background(), e.repoID)
	must(t, err)
	if repo.BackupCron != "" {
		t.Errorf("schedule not cleared: %q", repo.BackupCron)
	}
}
