// Command mktestrepos creates a handful of plain restic repositories with
// different shapes, to be added by hand under Settings in the UI.
//
//	go run ./tools/mktestrepos -out testrepos -restic ./restic.exe
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const password = "test-password"

var (
	bin     string
	outDir  string
	now     = time.Now()
	oneDay  = 24 * time.Hour
	problem = false
)

// step is one backup: what to change in the source folder first, and how to back it up.
type step struct {
	ago    time.Duration
	host   string
	tags   []string
	change func(src string) error
}

type repoSpec struct {
	name  string
	about string
	steps []step
	// extra runs after the backups (e.g. leaving a lock behind).
	after func(repo string)
}

func main() {
	out := flag.String("out", "testrepos", "output directory")
	resticBin := flag.String("restic", "restic", "restic executable")
	flag.Parse()
	var err error
	if outDir, err = filepath.Abs(*out); err != nil {
		log.Fatal(err)
	}
	if bin, err = filepath.Abs(*resticBin); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		bin = *resticBin // on PATH
	}
	if err := os.RemoveAll(outDir); err != nil {
		log.Fatal(err)
	}

	specs := specs()
	var wg sync.WaitGroup
	for _, sp := range specs {
		wg.Add(1)
		go func(sp repoSpec) {
			defer wg.Done()
			if err := build(sp); err != nil {
				log.Printf("%s FAILED: %v", sp.name, err)
				problem = true
				return
			}
			log.Printf("built %s", sp.name)
		}(sp)
	}
	wg.Wait()
	_ = os.RemoveAll(filepath.Join(outDir, "_src"))
	if problem {
		os.Exit(1)
	}
	if err := writeReadme(specs); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nTest repositories are in %s (password for all: %s)\nSee %s for what each one is good for.\n",
		outDir, password, filepath.Join(outDir, "README.txt"))
}

func specs() []repoSpec {
	return []repoSpec{
		{
			name:  "docs",
			about: "Small, 5 snapshots over a week, files added, edited and deleted between them. Host laptop, tag docs.",
			steps: []step{
				{6 * oneDay, "laptop", []string{"docs"}, write("Report.md", "# Report\ndraft\n")},
				{5 * oneDay, "laptop", []string{"docs"}, chain(write("Report.md", "# Report\nfirst version\n"), write("Notes/ideas.txt", "ideas\n"))},
				{3 * oneDay, "laptop", []string{"docs"}, chain(write("Budget.csv", "item,cost\nrent,900\n"), remove("Notes/ideas.txt"))},
				{2 * oneDay, "laptop", []string{"docs", "keep"}, write("Report.md", "# Report\nfinal\n")},
				{3 * time.Hour, "laptop", []string{"docs"}, write("Notes/todo.txt", "call the bank\n")},
			},
		},
		{
			name:  "multihost",
			about: "Three hosts with different backup paths, tags (daily, weekly, keep, monthly) and 14 snapshots spread over 90 days. Try the host and tag filters, the 6-month and all-time chart, and the cleanup preview.",
			steps: multihostSteps(),
		},
		{
			name:  "media",
			about: "Real PNG and JPEG images (image preview in the explorer), unicode and space file names, nested folders, a larger binary, a symlink if the OS allows it. 4 snapshots.",
			steps: []step{
				{20 * oneDay, "nas-01", []string{"photos"}, chain(images("2025/Sommerurlaub am Meer", 4), binary("Videos/clip.mp4", 3<<20))},
				{12 * oneDay, "nas-01", []string{"photos"}, chain(images("2025/Café Wien", 3), write("2025/Café Wien/Notiz – Ünïcode.txt", "schön\n"))},
				{4 * oneDay, "nas-01", []string{"photos", "keep"}, chain(images("2026/Winter", 5), binary("Videos/clip2.mp4", 6<<20), symlink("latest", "2026/Winter"))},
				{5 * time.Hour, "nas-01", []string{"photos"}, chain(remove("Videos/clip.mp4"), images("2026/Winter/Edited", 2))},
			},
		},
		{
			name:  "empty",
			about: "Initialised but without any snapshot (empty state on every page).",
		},
		{
			name:  "stale",
			about: "Hourly backups that stopped 6 days ago, so the repository shows as overdue.",
			steps: staleSteps(),
		},
		{
			name:  "locked",
			about: "Has a lock left behind by a killed process (lock notice on the repository page, 3 snapshots).",
			steps: []step{
				{3 * oneDay, "server", nil, write("a.txt", "one\n")},
				{2 * oneDay, "server", nil, write("a.txt", "two\n")},
				{1 * oneDay, "server", nil, write("b.txt", "three\n")},
			},
			after: leaveLock,
		},
	}
}

func multihostSteps() []step {
	var s []step
	for i := 0; i < 14; i++ {
		i := i
		host, tags := "web-01", []string{"daily"}
		switch i % 3 {
		case 1:
			host, tags = "db-01", []string{"daily", "pg_dump"}
		case 2:
			host, tags = "mail-01", []string{"weekly"}
		}
		if i%5 == 0 {
			tags = append(tags, "keep")
		}
		if i%7 == 6 {
			tags = append(tags, "monthly")
		}
		s = append(s, step{
			ago: time.Duration(90-i*6) * oneDay, host: host, tags: tags,
			change: chain(write(host+"/data.db", fmt.Sprintf("%s state %d %s", host, i, strings.Repeat("x", 4000*(i+1)))), binary(host+"/dump-"+fmt.Sprint(i)+".bin", 50000+i*30000)),
		})
	}
	return s
}

func staleSteps() []step {
	var s []step
	for i := 0; i < 30; i++ {
		i := i
		s = append(s, step{ago: 6*oneDay + time.Duration(30-i)*time.Hour, host: "backup-vm", tags: []string{"hourly"}, change: binary(fmt.Sprintf("log/part-%02d.bin", i), 20000+i*1500)})
	}
	return s
}

// ---- building ----

func build(sp repoSpec) error {
	repo := filepath.Join(outDir, sp.name)
	if err := restic(repo, nil, "init", "--quiet"); err != nil {
		return err
	}
	src := filepath.Join(outDir, "_src", sp.name)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return err
	}
	for _, st := range sp.steps {
		if st.change != nil {
			if err := st.change(src); err != nil {
				return err
			}
		}
		args := []string{"backup", src, "--quiet", "--host", st.host, "--time", now.Add(-st.ago).Format("2006-01-02 15:04:05")}
		for _, t := range st.tags {
			args = append(args, "--tag", t)
		}
		if err := restic(repo, nil, args...); err != nil {
			return err
		}
	}
	if sp.after != nil {
		sp.after(repo)
	}
	return nil
}

func restic(repo string, stdin *os.File, args ...string) error {
	cmd := exec.Command(bin, append([]string{"-r", repo}, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+password)
	cmd.Stdin = stdin
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restic %v: %w\n%s", args, err, out)
	}
	return nil
}

// leaveLock starts a backup that waits for stdin forever, then kills it.
func leaveLock(repo string) {
	cmd := exec.Command(bin, "-r", repo, "backup", "--stdin", "--stdin-filename", "dump.sql", "--host", "server", "--quiet")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+password)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("locked: skipped: %v", err)
		return
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		log.Printf("locked: skipped: %v", err)
		return
	}
	time.Sleep(4 * time.Second)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

// ---- source changes ----

type change = func(src string) error

func chain(fns ...change) change {
	return func(src string) error {
		for _, f := range fns {
			if err := f(src); err != nil {
				return err
			}
		}
		return nil
	}
}

func write(rel, content string) change {
	return func(src string) error {
		p := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o644)
	}
}

func remove(rel string) change {
	return func(src string) error { return os.RemoveAll(filepath.Join(src, filepath.FromSlash(rel))) }
}

// binary writes pseudo-random-looking bytes of the given size.
func binary(rel string, size int) change {
	return func(src string) error {
		b := make([]byte, size)
		x := uint32(size)*2654435761 + 12345
		for i := range b {
			x = x*1664525 + 1013904223
			b[i] = byte(x >> 24)
		}
		return write(rel, string(b))(src)
	}
}

func symlink(name, target string) change {
	return func(src string) error {
		p := filepath.Join(src, name)
		_ = os.Remove(p)
		if err := os.Symlink(filepath.FromSlash(target), p); err != nil {
			log.Printf("symlink skipped (%v)", err) // needs privileges on Windows
		}
		return nil
	}
}

// images writes n small coloured PNG and JPEG pictures into a folder.
func images(rel string, n int) change {
	return func(src string) error {
		dir := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			img := image.NewRGBA(image.Rect(0, 0, 320, 200))
			for y := 0; y < 200; y++ {
				for x := 0; x < 320; x++ {
					img.Set(x, y, color.RGBA{R: uint8(x*255/320 + i*30), G: uint8(y * 255 / 200), B: uint8(120 + i*25), A: 255})
				}
			}
			name := fmt.Sprintf("IMG_%04d", 1000+len(rel)*7+i)
			f, err := os.Create(filepath.Join(dir, name+".png"))
			if err != nil {
				return err
			}
			err = png.Encode(f, img)
			f.Close()
			if err != nil {
				return err
			}
			g, err := os.Create(filepath.Join(dir, name+".jpg"))
			if err != nil {
				return err
			}
			err = jpeg.Encode(g, img, &jpeg.Options{Quality: 80})
			g.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
}

func writeReadme(specs []repoSpec) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Test repositories for Restic Repo Browser\n\nPassword for all of them: %s\nAdd them in the app under Settings > Add repository (name, path, password).\n\n", password)
	for _, sp := range specs {
		fmt.Fprintf(&b, "%s\n  path: %s\n  %s\n\n", sp.name, filepath.Join(outDir, sp.name), sp.about)
	}
	return os.WriteFile(filepath.Join(outDir, "README.txt"), []byte(b.String()), 0o644)
}
