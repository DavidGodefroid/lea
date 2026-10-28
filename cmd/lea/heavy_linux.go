//go:build linux

package main

import (
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// heavyCPUs is the CPUs of slot: workers of the CPUs this process may run on, from slot ×
// workers on (wrapping), so that the slots of a container use different CPUs; and their number.
func heavyCPUs(slot, workers int) (cpuSet, int) {
	var cur unix.CPUSet
	if unix.SchedGetaffinity(0, &cur) != nil {
		return cur, max(1, min(workers, runtime.NumCPU()))
	}
	var cpus []int
	for i := 0; i < len(cur)*64; i++ {
		if cur.IsSet(i) {
			cpus = append(cpus, i)
		}
	}
	if len(cpus) == 0 || workers >= len(cpus) {
		return cur, max(1, len(cpus))
	}
	var set unix.CPUSet
	for i := 0; i < workers; i++ {
		set.Set(cpus[(slot*workers+i)%len(cpus)])
	}
	return set, workers
}

// startOn starts cmd on the CPUs of set: the child inherits the affinity of the thread that forks
// it, which is set for the start only.
func startOn(cmd *exec.Cmd, set cpuSet) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var old unix.CPUSet
	if unix.SchedGetaffinity(0, &old) == nil && unix.SchedSetaffinity(0, &set) == nil {
		defer unix.SchedSetaffinity(0, &old)
	}
	return cmd.Start()
}

// cpuSet is a set of CPUs a command is pinned to.
type cpuSet = unix.CPUSet
