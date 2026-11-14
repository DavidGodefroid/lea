package web

// lea web on the network (#416): an explicit opt-in, lea web --listen ADDR:PORT, for a browser on
// another machine when an SSH tunnel will not do. The loopback stays the default and the
// recommended way (ssh -L). On the network lea web refuses to start unless:
//
//   - it serves TLS: the user's certificate and key, or a self-signed certificate lea makes once
//     and keeps in its host data dir (0600), whose SHA-256 fingerprint it prints on every start;
//   - it has a non-ephemeral credential: a 256-bit token kept in the host data dir (0600), made
//     on the first --listen and changed by --new-token. The browser signs in by typing it into the
//     sign-in page (a POST, never in an address, which history and logs would keep); each sign-in
//     gets its own Secure, HttpOnly, SameSite=Strict session cookie, held in memory only (a
//     restart signs every browser out); failed sign-ins slow down the next ones;
//   - ADDR is one address of a host interface that no container reaches: never a wildcard (it
//     listens on every interface, the container networks' included), never the address of a
//     container bridge (docker0, br-*, veth*, podman*, cni*…) or one inside a container network's
//     subnet, never an address the host does not have.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RemoteOptions are lea web --listen's.
type RemoteOptions struct {
	// Addr is ADDR:PORT, ADDR an IP address of this host.
	Addr string
	// CertFile and KeyFile are the user's TLS certificate and key; SelfSigned, instead, a
	// certificate lea makes and keeps in Dir.
	CertFile, KeyFile string
	SelfSigned        bool
	// Dir keeps the token and the self-signed certificate (the host data dir's web/, 0700).
	Dir string
	// NewToken replaces the token: every browser signed in with the old one must sign in again.
	NewToken bool
}

// Remote is what lea web --listen prints when it starts.
type Remote struct {
	URL string
	// TokenFile holds the token; Token is the token itself, only when this run made it.
	TokenFile, Token string
	// Fingerprint is the SHA-256 of the certificate lea web serves, CertFile where it is.
	Fingerprint, CertFile string
}

// hostAddr is one address of a host interface.
type hostAddr struct {
	iface string
	ip    net.IP
}

// hostAddrs lists the host's interface addresses; a seam for the tests.
var hostAddrs = func() ([]hostAddr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []hostAddr
	for _, i := range ifs {
		as, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok {
				out = append(out, hostAddr{i.Name, n.IP})
			}
		}
	}
	return out, nil
}

// containerSubnets lists the subnets of the container engine's networks (lea's agent networks
// among them); a seam for the tests.
var containerSubnets = func() ([]*net.IPNet, error) {
	ids, err := exec.Command("docker", "network", "ls", "-q").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot list the container networks (docker network ls): %v", err)
	}
	args := append([]string{"network", "inspect", "-f", "{{range .IPAM.Config}}{{.Subnet}} {{end}}"}, strings.Fields(string(ids))...)
	if len(args) == 4 {
		return nil, nil
	}
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read the container networks' subnets (docker network inspect): %v", err)
	}
	var nets []*net.IPNet
	for _, f := range strings.Fields(string(out)) {
		if _, n, err := net.ParseCIDR(f); err == nil {
			nets = append(nets, n)
		}
	}
	return nets, nil
}

// listenTCP binds a TCP address; a seam for the tests, which cannot bind a non-loopback address.
var listenTCP = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// bridgeIface names the interfaces of container networks: Docker's, Podman's, CNI's, libvirt's,
// LXC/LXD's and the usual Kubernetes overlays'. An address on one of them is a container's way in.
var bridgeIface = regexp.MustCompile(`^(docker|br-|veth|podman|cni|virbr|lxcbr|lxdbr|cbr|flannel|cali|weave|kube|vxlan|tun|tap)`)

// checkListenIP refuses an address a container could reach, or one this host does not have.
func checkListenIP(ip net.IP) error {
	if ip.IsUnspecified() {
		return fmt.Errorf("%s listens on every interface, the container networks' included: name one address of this host", ip)
	}
	if ip.IsMulticast() {
		return fmt.Errorf("%s is a multicast address", ip)
	}
	as, err := hostAddrs()
	if err != nil {
		return fmt.Errorf("cannot list this host's addresses: %v", err)
	}
	iface := ""
	for _, a := range as {
		if a.ip.Equal(ip) {
			iface = a.iface
			break
		}
	}
	if iface == "" {
		return fmt.Errorf("%s is not an address of this host", ip)
	}
	if bridgeIface.MatchString(iface) {
		return fmt.Errorf("%s is on %s, a container network's interface: the agents could reach lea web there", ip, iface)
	}
	nets, err := containerSubnets()
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return fmt.Errorf("%s is inside the container network %s: the agents could reach lea web there", ip, n)
		}
	}
	return nil
}

// ParseListen reads ADDR:PORT: an IP address (IPv6 in brackets) and a port; loopback reports a
// loopback address, which needs none of the network's safeguards.
func ParseListen(addr string) (ip net.IP, port int, loopback bool, err error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, false, fmt.Errorf("--listen takes ADDR:PORT: %v", err)
	}
	ip = net.ParseIP(h)
	if ip == nil {
		return nil, 0, false, fmt.Errorf("--listen takes an IP address, not %q", h)
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return nil, 0, false, errors.New("--listen needs a port number (1-65535)")
	}
	return ip, port, ip.IsLoopback(), nil
}

// ListenRemote binds the server on o.Addr, a non-loopback address, behind TLS and the persistent
// token. It refuses to start when any safeguard is missing.
func (s *Server) ListenRemote(o RemoteOptions) (net.Listener, Remote, error) {
	ip, port, loopback, err := ParseListen(o.Addr)
	if err != nil {
		return nil, Remote{}, err
	}
	if loopback {
		return nil, Remote{}, errors.New("a loopback address needs no --listen: lea web listens on 127.0.0.1 by default (--port)")
	}
	if o.SelfSigned == (o.CertFile != "" || o.KeyFile != "") {
		return nil, Remote{}, errors.New("--listen on the network needs TLS: --tls-cert FILE --tls-key FILE, or --tls-self-signed")
	}
	if !o.SelfSigned && (o.CertFile == "" || o.KeyFile == "") {
		return nil, Remote{}, errors.New("--tls-cert and --tls-key go together")
	}
	if o.Dir == "" {
		return nil, Remote{}, errors.New("--listen on the network needs a persistent token, and lea has no host data dir to keep it")
	}
	if err := checkListenIP(ip); err != nil {
		return nil, Remote{}, err
	}
	if err := privateDir(o.Dir); err != nil {
		return nil, Remote{}, err
	}
	tokFile := filepath.Join(o.Dir, "token")
	tok, made, err := persistentToken(tokFile, o.NewToken)
	if err != nil {
		return nil, Remote{}, err
	}
	var cert tls.Certificate
	certFile := o.CertFile
	if o.SelfSigned {
		certFile = filepath.Join(o.Dir, "tls-cert.pem")
		cert, err = selfSigned(certFile, filepath.Join(o.Dir, "tls-key.pem"), ip, time.Now())
	} else {
		cert, err = tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
	}
	if err != nil {
		return nil, Remote{}, fmt.Errorf("TLS certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, Remote{}, fmt.Errorf("TLS certificate: %v", err)
	}
	tl, err := listenTCP(net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return nil, Remote{}, err
	}
	s.port = port
	hostport := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	s.hosts = map[string]bool{hostport: true}
	for _, n := range leaf.DNSNames {
		s.hosts[net.JoinHostPort(n, strconv.Itoa(s.port))] = true
	}
	s.remote = &remoteAuth{token: tok, sessions: map[string]time.Time{}, sources: map[string]*failures{}, now: time.Now}
	s.scheme = "https"
	l := tls.NewListener(tl, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	r := Remote{URL: "https://" + hostport + "/", TokenFile: tokFile, Fingerprint: fingerprint(leaf.Raw), CertFile: certFile}
	if made {
		r.Token = tok
	}
	return l, r, nil
}

// fingerprint is a certificate's SHA-256, as browsers show it: hex pairs with colons.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(sum))
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// privateDir makes dir (0700) and refuses it when it is a symlink or another user's.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is a symlink or not a directory, refused", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not yours, refused", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// readPrivate reads a file only its owner may read: a regular file, not a symlink, mode 0600 or
// stricter. It reports fs.ErrNotExist when there is none.
func readPrivate(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file, refused", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s may be read by others (mode %04o), refused: chmod 600 it or use --new-token", path, fi.Mode().Perm())
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is too large, refused", path)
	}
	return b, nil
}

// writePrivate writes path whole and atomically, mode 0600 (a temporary file renamed over it).
func writePrivate(path string, b []byte) error {
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file, refused", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var tokenText = regexp.MustCompile(`^[0-9a-f]{64}$`)

// persistentToken reads the token of path, or makes one (renew: always); made reports a new one.
func persistentToken(path string, renew bool) (tok string, made bool, err error) {
	if !renew {
		b, err := readPrivate(path, 128)
		if err == nil {
			t := strings.TrimSpace(string(b))
			if !tokenText.MatchString(t) {
				return "", false, fmt.Errorf("%s holds no lea web token, refused: --new-token makes one", path)
			}
			return t, false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
	}
	t, err := randomHex(32)
	if err != nil {
		return "", false, err
	}
	if err := writePrivate(path, []byte(t+"\n")); err != nil {
		return "", false, err
	}
	return t, true, nil
}

// selfSigned loads the self-signed certificate of certFile and keyFile, or makes a new one when
// there is none, it does not name ip, or it expires within 30 days.
func selfSigned(certFile, keyFile string, ip net.IP, now time.Time) (tls.Certificate, error) {
	cb, cerr := readPrivate(certFile, 64<<10)
	kb, kerr := readPrivate(keyFile, 64<<10)
	if cerr == nil && kerr == nil {
		c, err := tls.X509KeyPair(cb, kb)
		if err == nil {
			if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil &&
				now.Add(30*24*time.Hour).Before(leaf.NotAfter) && leaf.VerifyHostname(ip.String()) == nil {
				return c, nil
			}
		}
	} else if (cerr != nil && !errors.Is(cerr, fs.ErrNotExist)) || (kerr != nil && !errors.Is(kerr, fs.ErrNotExist)) {
		return tls.Certificate{}, errors.Join(cerr, kerr)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "lea web"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{ip}, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	cb = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kb = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	if err := writePrivate(keyFile, kb); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePrivate(certFile, cb); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(cb, kb)
}
