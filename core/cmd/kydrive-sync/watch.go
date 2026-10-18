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
	add := func(top string) {
		filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				if rel, _ := filepath.Rel(d.root, p); rel == "Unsynced" {
					return filepath.SkipDir
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
		if _, err := os.Stat(d.root); err == nil && len(w.WatchList()) == 0 {
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
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					add(ev.Name)
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
