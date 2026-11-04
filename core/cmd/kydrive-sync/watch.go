package main

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watch asks for a local scan once file system activity under the root has been quiet for
// longer than a file needs to settle. Without watches the 10-minute scan still runs.
func (d *daemon) watch(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watch: %v; relying on periodic scans", err)
		return
	}
	defer w.Close()
	// kqueue (macOS) holds a descriptor for every file in a watched folder, including files
	// created later: past the budget, folders rely on the periodic scan rather than leaving
	// none for syncing and the socket. counts holds what each watched folder was charged.
	budget, used, full := watchBudget(), 0, false
	counts := map[string]int{}
	add := func(top string) {
		filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				if rel, _ := filepath.Rel(d.root, p); rel == "Unsynced" {
					return filepath.SkipDir
				}
				if budget > 0 {
					list, _ := os.ReadDir(p)
					if used+len(list)+1 > budget {
						log.Printf("watch: %s and below rely on periodic scans (%d entries watched)", p, used)
						full = true
						return filepath.SkipDir
					}
					counts[p] = len(list) + 1
					used += counts[p]
				}
				if err := w.Add(p); err != nil {
					log.Printf("watch %s: %v", p, err)
				}
			}
			return nil
		})
	}
	quiet := time.NewTimer(time.Hour)
	quiet.Stop()
	for {
		if _, err := os.Stat(d.root); err == nil && len(w.WatchList()) == 0 && !full {
			used, counts = 0, map[string]int{}
			add(d.root)
			d.trigger(true) // whatever changed while unwatched
		}
		select {
		case <-ctx.Done():
			return
		case ev := <-w.Events:
			if name := filepath.Base(ev.Name); strings.HasPrefix(name, ".kydrive-") {
				continue // our own downloads
			}
			if dir := filepath.Dir(ev.Name); counts[dir] > 1 && (ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)) {
				counts[dir]--
				used--
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					add(ev.Name)
				}
				if dir := filepath.Dir(ev.Name); budget > 0 && counts[dir] > 0 {
					counts[dir]++
					if used++; used > budget {
						// Its files' descriptors close with it; the periodic scan covers it.
						log.Printf("watch: %s relies on periodic scans (%d entries watched)", dir, used)
						w.Remove(dir)
						used -= counts[dir]
						delete(counts, dir)
						full = true
					}
				}
			}
			quiet.Reset(settle + time.Second)
		case err := <-w.Errors:
			log.Printf("watch: %v", err)
		case <-quiet.C:
			d.trigger(true)
		case <-time.After(5 * time.Second):
			// Picks up a root created after start, or deleted and created again.
		}
	}
}
