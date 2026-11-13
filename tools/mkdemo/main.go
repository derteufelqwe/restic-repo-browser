// Command mkdemo builds demo restic repositories with realistic history and an
// application database that already knows them, for trying out the UI.
//
//	go run ./tools/mkdemo -out demo -restic ./restic.exe
//	restic-browser -data-dir demo/data -restic ./restic.exe   (password: demo-password)
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log"
	mrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"restic-repo-browser/internal/auth"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/secret"
	"restic-repo-browser/internal/store"
)

const (
	repoPassword  = "demo-repo-password"
	loginPassword = "demo-password"
)

// spec describes one demo repository.
type spec struct {
	name    string
	hosts   []string
	paths   []string // source folders (relative to src/<name>), one per host group
	tags    []string
	count   int           // number of snapshots
	step    time.Duration // time between snapshots
	lastAgo time.Duration // age of the newest snapshot
	kbAdded int           // typical data added per snapshot, in KB
	monthly bool          // tag the first snapshot of each month
	job     *domain.Job
	failing bool // add a failed cleanup run
	locked  bool // leave a stale lock behind
	missing bool // register a path without a repository
}

func main() {
	out := flag.String("out", "demo", "output directory")
	bin := flag.String("restic", "restic", "restic executable")
	flag.Parse()
	abs, err := filepath.Abs(*out)
	if err != nil {
		log.Fatal(err)
	}
	resticBin, err := filepath.Abs(*bin)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(resticBin); err != nil {
		resticBin = *bin // maybe on PATH
	}
	if err := os.RemoveAll(abs); err != nil {
		log.Fatal(err)
	}

	now := time.Now()
	keep := domain.KeepPolicy{Last: 7, Daily: 14, Weekly: 8, Monthly: 12, Yearly: 2}
	specs := []spec{
		{name: "nas-photos", hosts: []string{"nas-01"}, paths: []string{"photos"}, tags: []string{"photos"}, count: 36, step: 24 * time.Hour, lastAgo: 3 * time.Hour, kbAdded: 1800, monthly: true,
			job: &domain.Job{Cron: "30 3 * * *", Keep: keep, Prune: true, Enabled: true}},
		{name: "homelab-docker", hosts: []string{"docker-01", "pi-gateway"}, paths: []string{"docker", "pihole"}, tags: []string{"daily"}, count: 40, step: 12 * time.Hour, lastAgo: 2 * time.Hour, kbAdded: 600, locked: true,
			job: &domain.Job{Cron: "0 4 * * 0", Keep: domain.KeepPolicy{Last: 4, Daily: 7, Weekly: 4, Monthly: 6}, Prune: true, Enabled: true}},
		{name: "vps-postgres", hosts: []string{"vps-fra-02"}, paths: []string{"pg"}, tags: []string{"pg_dump"}, count: 48, step: 6 * time.Hour, lastAgo: 4*24*time.Hour + 5*time.Hour, kbAdded: 300, failing: true,
			job: &domain.Job{Cron: "15 2 * * *", Keep: domain.KeepPolicy{Last: 24, Daily: 30, Monthly: 12}, Prune: true, Enabled: true}},
		{name: "nextcloud-data", hosts: []string{"cloud-01"}, paths: []string{"nextcloud"}, tags: []string{"nextcloud"}, count: 24, step: 24 * time.Hour, lastAgo: 40 * time.Hour, kbAdded: 900, monthly: true,
			job: &domain.Job{Cron: "0 5 1 * *", Keep: domain.KeepPolicy{Last: 3, Daily: 14, Weekly: 8, Monthly: 12, Yearly: 2}, Prune: false, Enabled: true}},
		{name: "new-laptop", hosts: []string{"laptop-arne"}, paths: []string{"laptop"}, count: 0},
		{name: "offsite-usb", missing: true},
	}

	var wg sync.WaitGroup
	for _, sp := range specs {
		if sp.missing {
			continue
		}
		wg.Add(1)
		go func(sp spec) {
			defer wg.Done()
			if err := build(resticBin, abs, sp, now); err != nil {
				log.Fatalf("%s: %v", sp.name, err)
			}
			log.Printf("built %s (%d snapshots)", sp.name, sp.count)
		}(sp)
	}
	wg.Wait()
	_ = os.RemoveAll(filepath.Join(abs, "src")) // only needed while backing up

	if err := register(abs, specs, now); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nDemo ready in %s\n  restic-browser -data-dir %s -restic %s\n  login password: %s\n", abs, filepath.Join(abs, "data"), *bin, loginPassword)
}

func repoPath(out, name string) string { return filepath.Join(out, "repos", name) }

// build creates the repository and its snapshots.
func build(bin, out string, sp spec, now time.Time) error {
	repo := repoPath(out, sp.name)
	if err := restic(bin, repo, "init", "--quiet"); err != nil {
		return err
	}
	rnd := mrand.New(mrand.NewSource(int64(len(sp.name)) * 7919))
	srcRoot := filepath.Join(out, "src", sp.name)
	for g := range sp.paths {
		if err := seedSource(filepath.Join(srcRoot, sp.paths[g]), sp.name, rnd); err != nil {
			return err
		}
	}
	for i := 0; i < sp.count; i++ {
		at := now.Add(-sp.lastAgo - time.Duration(sp.count-1-i)*sp.step)
		g := i % len(sp.hosts)
		src := filepath.Join(srcRoot, sp.paths[g])
		if err := grow(src, i, sp.kbAdded, rnd); err != nil {
			return err
		}
		args := []string{"backup", src, "--quiet", "--host", sp.hosts[g], "--time", at.Format("2006-01-02 15:04:05")}
		for _, t := range sp.tags {
			args = append(args, "--tag", t)
		}
		if sp.monthly && at.Day() == 1 {
			args = append(args, "--tag", "monthly")
		}
		if err := restic(bin, repo, args...); err != nil {
			return err
		}
	}
	if sp.locked {
		leaveLock(bin, repo)
	}
	return nil
}

// seedSource writes the initial files of a source folder.
func seedSource(dir, kind string, rnd *mrand.Rand) error {
	names := []string{"README.txt", "config/app.ini", "config/secrets.env", "data/main.db", "data/index.json", "logs/app.log", "docs/Notes.md", "docs/Budget.ods"}
	for _, n := range names {
		if err := writeRandom(filepath.Join(dir, n), 2000+rnd.Intn(60000)); err != nil {
			return err
		}
	}
	for y := 2024; y <= 2026; y++ {
		for _, place := range []string{"Lisbon", "Harz", "Sylt"} {
			for k := 0; k < 3; k++ {
				p := filepath.Join(dir, fmt.Sprint(y), fmt.Sprintf("%d-0%d %s", y, 3+k, place), fmt.Sprintf("IMG_%d.jpg", 1000+rnd.Intn(8000)))
				if err := writeRandom(p, 20000+rnd.Intn(80000)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// grow adds new data before a backup so each snapshot has something to add.
func grow(dir string, i, kb int, rnd *mrand.Rand) error {
	size := (kb/2 + rnd.Intn(kb+1)) * 1024
	if i%9 == 8 {
		size *= 4 // occasional big change
	}
	if err := writeRandom(filepath.Join(dir, "incoming", fmt.Sprintf("batch-%03d.bin", i)), size); err != nil {
		return err
	}
	return writeRandom(filepath.Join(dir, "data", "main.db"), 30000+rnd.Intn(30000))
}

func writeRandom(path string, size int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func restic(bin, repo string, args ...string) error {
	cmd := exec.Command(bin, append([]string{"-r", repo}, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+repoPassword)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restic %v: %w\n%s", args, err, out)
	}
	return nil
}

// leaveLock starts a backup that waits forever for data on stdin and kills it,
// which leaves its lock in the repository like a crashed backup would.
func leaveLock(bin, repo string) {
	cmd := exec.Command(bin, "-r", repo, "backup", "--stdin", "--stdin-filename", "dump.sql", "--host", "docker-01", "--quiet")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+repoPassword)
	stdin, err := cmd.StdinPipe() // never written to and never closed
	if err != nil {
		log.Printf("lock demo skipped: %v", err)
		return
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		log.Printf("lock demo skipped: %v", err)
		return
	}
	time.Sleep(4 * time.Second)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// register writes the application database: repositories, jobs, some run history and the login password.
func register(out string, specs []spec, now time.Time) error {
	ctx := context.Background()
	dataDir := filepath.Join(out, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	box, err := secret.LoadOrCreate(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(dataDir, "restic-browser.db"), box)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := auth.New(st).SetPassword(ctx, loginPassword); err != nil {
		return err
	}
	for _, sp := range specs {
		path := repoPath(out, sp.name)
		if sp.missing {
			path = filepath.Join(out, "usb-offsite", "restic") // does not exist
		}
		id, err := st.CreateRepo(ctx, store.NewRepo{Name: sp.name, Path: path, Password: repoPassword}, now)
		if err != nil {
			return err
		}
		if sp.job == nil {
			continue
		}
		job := *sp.job
		job.RepoID = id
		if err := st.SaveJob(ctx, job); err != nil {
			return err
		}
		for k := 0; k < 3; k++ {
			at := now.Add(-time.Duration(k+1) * 24 * time.Hour)
			rid, _ := st.StartRun(ctx, id, at)
			status, summary, logText := domain.RunOK, "Forgot 2 snapshots, freed 1.2 GiB", "$ restic forget --keep-last 7\nkeep 30 snapshots\nremove 2 snapshots\n"
			if sp.failing && k < 2 {
				status, summary = domain.RunFailed, "The repository is locked by another process."
				logText = "$ restic forget --keep-last 24\nError: repository is already locked exclusively by PID 2231 on vps-fra-02\nJob stopped. It will try again at the next scheduled run.\n"
			}
			if err := st.FinishRun(ctx, rid, at.Add(14*time.Second), status, summary, logText); err != nil {
				return err
			}
		}
	}
	return nil
}
