//go:build !linux

package main

import (
	"os/exec"
	"runtime"
)

// cpuSet: lea heavy pins its command to CPUs on Linux only, where lea's containers run.
type cpuSet struct{}

// heavyCPUs is no set elsewhere: the workers, at most the CPUs.
func heavyCPUs(slot, workers int) (cpuSet, int) {
	return cpuSet{}, max(1, min(workers, runtime.NumCPU()))
}

func startOn(cmd *exec.Cmd, _ cpuSet) error { return cmd.Start() }
