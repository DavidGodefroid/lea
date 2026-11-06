package locks

import (
	"fmt"
	"strings"
)

// Status is a compatibility verdict's severity, as lea doctor prints it (ok, warn, FAIL).
type Status int

const (
	OK Status = iota
	Warn
	Fail
)

// Hello is what a lock's console answers to the hello handshake (lockkit's ipc.HelloResult).
type Hello struct {
	ProtocolMajor  int    `json:"protocol_major"`
	ConsoleVersion string `json:"console_version"`
	Profile        string `json:"profile"`
	// Refused: the console refused the handshake of lea's client's protocol major (lockkit's
	// "protocol mismatch"), ProtocolMajor being the one it says it speaks (0: it did not say).
	// lea sets it, never the console's answer.
	Refused bool `json:"refused,omitempty"`
}

// Observed is what lea knows of one lock lox runs.
type Observed struct {
	Type, Instance string
	// Image is the image lockfamily.yaml pins for the lock (what lox runs), "" when unknown.
	Image string
	// Hello is the console's handshake when lea asked it (nil: not asked, or HelloErr says why
	// it could not be read).
	Hello    *Hello
	HelloErr string
}

// Client is lea's client of a Lock: the set's pin, and the version lea's image has of it (the
// pin's, or a dev override's: LEA_<LOCK>_REF, a release or "" for a checkout of unknown version).
type Client struct {
	Pin     Pin
	Version string
}

// Verdict is the compatibility of a lock with lea's client of its type: its severity, the
// subject of the doctor line and what it says, with the fix to run when there is one.
type Verdict struct {
	Status  Status
	Subject string
	Detail  string
}

// lockVersion is the lock's release from what lea observed: the set's when lockfamily.yaml pins
// the set's image (or a variant), else the console's handshake; "" when neither tells.
func lockVersion(c Client, o Observed) (version, variant string, protocol int, fromImage bool) {
	if v, ok := c.Pin.ImageOf(o.Image); ok {
		return c.Pin.Version, v, c.Pin.Protocol, true
	}
	if o.Hello != nil && o.Hello.Refused {
		// The console refused lea's protocol: another major, the one it said or an unknown one (-1).
		p := o.Hello.ProtocolMajor
		if p < 1 || p == c.Pin.Protocol {
			p = -1
		}
		return "", "", p, false
	}
	if o.Hello != nil {
		v := o.Hello.ConsoleVersion
		if v != "" && !strings.HasPrefix(v, "v") {
			v = "v" + v
		}
		if !Version.MatchString(v) {
			v = ""
		}
		return v, "", o.Hello.ProtocolMajor, false
	}
	return "", "", 0, false
}

// Check is the compatibility of the lock o with lea's client c of its type, and the fix: `lea
// update` when the lock is newer (a lea release brings newer clients, as a set), `brew upgrade
// lox` and `lox add TYPE NAME --version` when it is older or unknown (lox pins and runs the lock:
// lea never does). Protocol majors that differ are incompatible: the client and the console
// refuse to talk.
func Check(c Client, o Observed) Verdict {
	subj := fmt.Sprintf("lock %s (%s)", o.Instance, o.Type)
	pinFix := fmt.Sprintf("lox add %s %s --version %s, then lox up", o.Type, o.Instance, c.Pin.Version)
	if c.Version != "" && c.Version != c.Pin.Version {
		pinFix = fmt.Sprintf("lox add %s %s --version %s, then lox up", o.Type, o.Instance, c.Version)
	}
	ver, variant, proto, fromImage := lockVersion(c, o)
	clientDesc := "lea's " + o.Type + " client"
	if c.Version != "" {
		clientDesc += " " + c.Version
	} else {
		clientDesc += " (a dev checkout, LEA_" + strings.ToUpper(o.Type) + "_REF)"
	}
	if proto == 0 {
		why := "lockfamily.yaml pins no image for it"
		if o.Image != "" {
			why = "it runs " + o.Image + ", not an image of " + o.Type + " " + c.Pin.Version + " (lea's set " + "pins " + c.Pin.Image + ")"
		}
		if o.HelloErr != "" {
			why += ", and its console's handshake could not be read: " + o.HelloErr
		}
		return Verdict{Warn, subj, "cannot tell whether " + clientDesc + " speaks its protocol: " + why + "; to run the release lea's client speaks: " + pinFix}
	}
	how := ""
	if variant != "" {
		how = " (" + variant + " image)"
	}
	if !fromImage {
		how = " (its console's handshake)"
	}
	newer := ver != "" && c.Version != "" && Older(c.Version, ver)
	older := ver != "" && c.Version != "" && Older(ver, c.Version)
	lockDesc := o.Type + " " + ver
	if ver == "" {
		lockDesc = o.Type + " of an unknown version"
	}
	if proto != c.Pin.Protocol {
		fix := pinFix
		switch {
		case newer:
			fix = "lea update (a lea whose set has " + o.Type + " " + ver + "), or " + pinFix
		case older:
			fix = "brew upgrade lox, then " + pinFix
		case ver == "" && proto > c.Pin.Protocol:
			fix = "lea update (a lea whose set speaks protocol " + fmt.Sprint(proto) + "), or " + pinFix
		case ver == "" && proto > 0:
			fix = "brew upgrade lox, then " + pinFix
		}
		if o.Hello != nil && o.Hello.Refused {
			speaks := "another protocol"
			if proto > 0 {
				speaks = fmt.Sprintf("protocol %d", proto)
			}
			return Verdict{Fail, subj, fmt.Sprintf("incompatible: its console refuses the handshake of %s, which speaks protocol %d (the console speaks %s), so every call of lea's agents is refused: %s",
				clientDesc, c.Pin.Protocol, speaks, fix)}
		}
		return Verdict{Fail, subj, fmt.Sprintf("incompatible: it runs %s%s, protocol %d; %s speaks protocol %d, so every call of lea's agents is refused: %s",
			lockDesc, how, proto, clientDesc, c.Pin.Protocol, fix)}
	}
	switch {
	case c.Version == "":
		return Verdict{Warn, subj, fmt.Sprintf("runs %s%s, protocol %d, as %s; the dev override is not a release of lea's set (remove LEA_%s_REF)", lockDesc, how, proto, clientDesc, strings.ToUpper(o.Type))}
	case ver == c.Version:
		return Verdict{OK, subj, fmt.Sprintf("runs %s%s, the release of %s (protocol %d)", lockDesc, how, clientDesc, proto)}
	case newer:
		return Verdict{Warn, subj, fmt.Sprintf("runs %s%s, newer than %s (same protocol %d): lea update when a lea release pins it, or %s", lockDesc, how, clientDesc, proto, pinFix)}
	case older:
		return Verdict{Warn, subj, fmt.Sprintf("runs %s%s, older than %s (same protocol %d): brew upgrade lox, then %s", lockDesc, how, clientDesc, proto, pinFix)}
	}
	return Verdict{Warn, subj, fmt.Sprintf("runs %s%s (protocol %d, as %s), not the release of lea's set: %s", lockDesc, how, proto, clientDesc, pinFix)}
}
