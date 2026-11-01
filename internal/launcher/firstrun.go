package launcher

import (
	"errors"
	"fmt"
	"strings"
)

// loxCheck is the lox lea needs to start the Lock Family hub: on PATH and at least the version
// of config/host-minimums. A dev build is not compared.
func (l *Launcher) loxCheck() error {
	out, err := l.output(false, "lox", "version")
	if err != nil {
		return errors.New("lox not found: lea starts the Lock Family hub with it: brew install davidgodefroid/tap/lox, then run lea again")
	}
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "lox"))
	if v == "dev" {
		l.logf("warning: lox dev: its version is not checked")
		return nil
	}
	if min := l.hostMinimum("lox"); min != "" && !versionAtLeast(v, min) {
		return fmt.Errorf("lox %s is older than %s, which lea needs to start the hub: brew upgrade lox, then run lea again", v, min)
	}
	return nil
}
