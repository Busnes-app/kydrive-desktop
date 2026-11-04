package main

import "syscall"

// watchBudget is how many watched entries kqueue may hold: half the descriptor limit.
func watchBudget() int {
	var l syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &l); err != nil {
		return 512
	}
	return int(l.Cur / 2)
}
