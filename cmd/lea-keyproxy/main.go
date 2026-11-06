// Command lea-keyproxy is the key forwarder of the lea proxy container: it forwards the agents'
// requests to the OpenAI-compatible endpoints of the endpoints file, adding each endpoint's API
// key, through the proxy's squid (see internal/keyproxy).
package main

import (
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/DavidGodefroid/lea/internal/keyproxy"
	"github.com/DavidGodefroid/lea/internal/lockhttp"
	"github.com/DavidGodefroid/lea/internal/providers"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lea-keyproxy:", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("lea-keyproxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoints := fs.String("endpoints", "/etc/lea/endpoints", "the endpoints file: <name> <base-url> [<key file>] per line")
	internal := fs.String("internal-hosts", "/etc/lea/internal-hosts", "the internal-hosts file")
	listen := fs.String("listen", fmt.Sprintf(":%d", providers.ForwarderPort), "the listen address")
	viaFlag := fs.String("via", "http://127.0.0.1:3128", "the forward proxy every upstream request goes through")
	sources := fs.String("sources", "", "LockHTTP: the gate's sources.json, which names the calling container (with $LEA_KEYPROXY_TOKEN, the gate's password); empty: -via is plain squid")
	caCert := fs.String("ca-cert", "", "LockHTTP MITM: a CA certificate (PEM) to trust besides the system's: the gate's, which re-signs decrypted upstreams")
	forgeListen := fs.String("forge-listen", "", "the forge forwarder's listen address (GitHub, its token from -forge-keys); empty: off")
	forgeKeys := fs.String("forge-keys", "/etc/lea/forge", "the forge forwarder's key directory (github.com: the GitHub token)")
	routes := fs.String("routes", "/proc/net/route", "the routing table: connections on the default route's interface (the egress network) are refused; empty refuses none")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("no positional arguments")
	}
	hosts := providers.Hosts{}
	if hf, err := os.Open(*internal); err == nil {
		hosts = providers.ParseHosts(hf)
		hf.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("internal-hosts file: %w", err)
	}
	var eps []keyproxy.Endpoint
	ef, err := os.Open(*endpoints)
	switch {
	case err == nil:
		eps, err = keyproxy.ParseEndpoints(ef, hosts, providers.KeyDir)
		ef.Close()
		if err != nil {
			return fmt.Errorf("endpoints file %s: %w", *endpoints, err)
		}
	case !errors.Is(err, os.ErrNotExist) || *forgeListen == "":
		return fmt.Errorf("endpoints file: %w", err)
	}
	if len(eps) == 0 && *forgeListen == "" {
		return errors.New("the endpoints file lists no endpoint")
	}
	via, err := url.Parse(*viaFlag)
	if err != nil {
		return errors.New("invalid -via URL")
	}
	cfg := keyproxy.Config{Endpoints: eps, Via: via}
	if *sources != "" {
		// From the environment, never argv: ps in the proxy container would show it.
		tok := os.Getenv("LEA_KEYPROXY_TOKEN")
		if tok == "" {
			return errors.New("-sources needs LEA_KEYPROXY_TOKEN")
		}
		os.Unsetenv("LEA_KEYPROXY_TOKEN")
		cfg.Gate = &keyproxy.Gate{Token: tok, Source: lockhttp.NewSourcer(*sources).Source}
	}
	if *caCert != "" {
		pem, err := os.ReadFile(*caCert)
		if err != nil {
			return fmt.Errorf("-ca-cert: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return errors.New("-ca-cert: no certificate in " + *caCert)
		}
		cfg.RootCAs = pool
	}
	if *routes != "" {
		cfg.Refused = keyproxy.EgressAddrs(*routes)
	}
	f, err := keyproxy.New(cfg)
	if err != nil {
		return err
	}
	for _, ep := range eps {
		if ep.KeyFile != "" {
			if _, err := providers.ReadKey(ep.KeyFile); err != nil {
				// Never fatal: the key may be rotated in place; each request reads it again.
				fmt.Fprintf(stderr, "lea-keyproxy: warning: %s: %v\n", ep.Name, err)
			}
		}
	}
	errs := make(chan error, 2)
	if *forgeListen != "" {
		fmt.Fprintf(stderr, "lea-keyproxy: forge forwarder (GitHub) listening on %s\n", *forgeListen)
		go func() { errs <- keyproxy.Server(*forgeListen, f.Forge(*forgeKeys, nil)).ListenAndServe() }()
	}
	if len(eps) > 0 {
		fmt.Fprintf(stderr, "lea-keyproxy: %d endpoint(s), listening on %s\n", len(eps), *listen)
		go func() { errs <- keyproxy.Server(*listen, f).ListenAndServe() }()
	}
	return <-errs
}
