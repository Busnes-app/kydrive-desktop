//go:build !darwin

package main

// watchBudget is 0, no limit: inotify and Windows watch folders, not each file.
func watchBudget() int { return 0 }
