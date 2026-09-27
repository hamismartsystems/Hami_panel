package xray

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

// Canary is one live check: start the core from this endpoint, dial the
// link that was built from the same endpoint, and confirm a request comes
// back. A link that only looks right fails here.
type Canary struct {
	Bin      string
	Endpoint Endpoint
	// DialHost is where the client connects. The link keeps its own port
	// and every security field. Default 127.0.0.1, so the check does not
	// depend on the public address being reachable.
	DialHost string
}

// Report is the outcome of one canary.
type Report struct {
	OK     bool
	Link   string
	Detail string
}

// Run starts the core, connects with the generated link, and fetches a
// local page through that tunnel. It stops the core before returning.
func (c Canary) Run(ctx context.Context) (Report, error) {
	if c.Bin == "" {
		return Report{}, fmt.Errorf("canary: xray binary is empty")
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return Report{}, fmt.Errorf("canary: xray binary: %w", err)
	}
	if len(c.Endpoint.Clients) == 0 {
		return Report{}, fmt.Errorf("canary: endpoint has no client")
	}
	ep := c.Endpoint
	if ep.Listen == "" {
		ep.Listen = "127.0.0.1"
	}
	share, err := link.Build(ep.Inbound, ep.Clients[0])
	if err != nil {
		return Report{}, err
	}
	parsed, err := link.Parse(share)
	if err != nil {
		return Report{}, err
	}
	if err := agree(ep, parsed); err != nil {
		return Report{Link: share}, err
	}
	if ep.PrivateKey != "" && strings.Contains(share, ep.PrivateKey) {
		return Report{Link: share}, fmt.Errorf("canary: private key leaked into the link")
	}

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Report{Link: share}, err
	}
	defer target.Close()
	go http.Serve(target, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/canary" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "hami-canary-ok")
	}))

	dir, err := os.MkdirTemp("", "hami-canary-")
	if err != nil {
		return Report{Link: share}, err
	}
	defer os.RemoveAll(dir)

	serverCfg, err := Build([]Endpoint{ep})
	if err != nil {
		return Report{Link: share}, err
	}
	proxyPort, err := freePort()
	if err != nil {
		return Report{Link: share}, err
	}
	dialHost := c.DialHost
	if dialHost == "" {
		dialHost = "127.0.0.1"
	}
	clientCfg, err := ClientConfig(share, dialHost, proxyPort)
	if err != nil {
		return Report{Link: share}, err
	}
	serverPath := filepath.Join(dir, "server.json")
	clientPath := filepath.Join(dir, "client.json")
	if err := os.WriteFile(serverPath, serverCfg, 0o600); err != nil {
		return Report{Link: share}, err
	}
	if err := os.WriteFile(clientPath, clientCfg, 0o600); err != nil {
		return Report{Link: share}, err
	}

	srv, err := startXray(ctx, c.Bin, serverPath)
	if err != nil {
		return Report{Link: share}, fmt.Errorf("canary: server: %w", err)
	}
	defer stopXray(srv)
	cli, err := startXray(ctx, c.Bin, clientPath)
	if err != nil {
		return Report{Link: share, Detail: srv.tail()}, fmt.Errorf("canary: client: %w", err)
	}
	defer stopXray(cli)

	targetURL := "http://" + target.Addr().String() + "/canary"
	if err := waitHTTP(ctx, proxyPort, targetURL); err != nil {
		return Report{
			Link:   share,
			Detail: "server: " + srv.tail() + " client: " + cli.tail(),
		}, fmt.Errorf("canary: traffic did not pass: %w", err)
	}
	return Report{OK: true, Link: share, Detail: "traffic passed"}, nil
}

func agree(ep Endpoint, s link.Share) error {
	in := ep.Inbound
	if s.UUID != ep.Clients[0].UUID {
		return fmt.Errorf("canary: link uuid does not match the client")
	}
	if s.Port != in.Port {
		return fmt.Errorf("canary: link port %d != inbound port %d", s.Port, in.Port)
	}
	if s.Security != in.Security {
		return fmt.Errorf("canary: link security %s != inbound %s", s.Security, in.Security)
	}
	if s.Transport != in.Transport && !(s.Transport == link.TCP && in.Transport == "") {
		return fmt.Errorf("canary: link transport %s != inbound %s", s.Transport, in.Transport)
	}
	if in.Security == link.Reality {
		if s.PublicKey != in.PublicKey || s.SNI != in.SNI || s.ShortID != in.ShortID {
			return fmt.Errorf("canary: link reality fields do not match this inbound")
		}
	}
	if in.Security == link.TLS && s.SNI != in.SNI {
		return fmt.Errorf("canary: link sni does not match this inbound")
	}
	return nil
}

func waitHTTP(ctx context.Context, proxyPort int, rawURL string) error {
	proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	var last error
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if resp.StatusCode == 200 && string(body) == "hami-canary-ok" {
			return nil
		}
		last = fmt.Errorf("status %d body %q", resp.StatusCode, body)
		time.Sleep(200 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return last
}

type xrayProc struct {
	cmd *exec.Cmd
	log *os.File
}

func startXray(ctx context.Context, bin, cfg string) (*xrayProc, error) {
	log, err := os.Create(cfg + ".log")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "run", "-c", cfg)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	return &xrayProc{cmd: cmd, log: log}, nil
}

func stopXray(p *xrayProc) {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()
	if p.log != nil {
		_ = p.log.Close()
	}
}

func (p *xrayProc) tail() string {
	if p == nil || p.log == nil {
		return ""
	}
	b, err := os.ReadFile(p.log.Name())
	if err != nil {
		return ""
	}
	if len(b) > 800 {
		b = b[len(b)-800:]
	}
	return string(b)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}
