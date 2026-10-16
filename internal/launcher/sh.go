package launcher

// sh runs one command of libexec/lea-sh, the shell parts not ported yet, with the launcher's
// terminal and environment. The command list lives in lea-sh; anything else exits 2 there
// before any lea function runs.
func (l *Launcher) sh(command string, args ...string) error {
	return l.foreground(l.Root+"/libexec/lea-sh", append([]string{command}, args...)...)
}

// shNoInput is sh with stdin from /dev/null (`(lea_scan_finish …) </dev/null`).
func (l *Launcher) shNoInput(command string, args ...string) error {
	c := l.cmd(l.Root+"/libexec/lea-sh", append([]string{command}, args...)...)
	c.Stdout, c.Stderr = l.Stdout, l.Stderr
	return l.exec(c)
}
