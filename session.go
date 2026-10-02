package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	errBadQuery = errors.New("bad query")
	errFull     = errors.New("no session can be started until another one ends")
)

type proxy struct {
	cfg       config
	runDir    string
	systemctl func(args ...string) error

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	id    string
	kind  string
	unit  string
	port  int
	proxy *httputil.ReverseProxy

	inflight int
	timer    *time.Timer
	closing  bool
}

func newProxy(cfg config, runDir string) *proxy {
	return &proxy{
		cfg:       cfg,
		runDir:    runDir,
		systemctl: systemctl,
		sessions:  map[string]*session{},
	}
}

// Not taken from anything the caller sends: the id is all that stands between
// one caller and another caller's session
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *proxy) envPath(id string) string {
	return filepath.Join(p.runDir, id+".env")
}

func (p *proxy) open(name string, k kind, query url.Values) (*session, error) {
	vars, err := k.env(query)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadQuery, err)
	}

	s, err := p.reserve(name, k)
	if err != nil {
		return nil, err
	}
	env := append([]string{fmt.Sprintf("PORT=%d", s.port)}, vars...)
	if err := p.launch(s, env); err != nil {
		return nil, err
	}

	// Started here rather than at the first relayed request: a caller who never
	// follows the redirect would otherwise keep the session for good
	p.mu.Lock()
	p.idle(s)
	p.mu.Unlock()

	log.Printf("%s %s: started, %s", name, s.id, strings.Join(env, " "))
	return s, nil
}

func (p *proxy) reserve(name string, k kind) (*session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	port, ok := p.freePort()
	if !ok {
		return nil, errFull
	}
	id := newID()
	s := &session{id: id, kind: name, unit: unitFor(k.Unit, id), port: port, proxy: relayTo(port)}
	p.sessions[id] = s
	return s, nil
}

func (p *proxy) launch(s *session, env []string) error {
	if err := os.WriteFile(p.envPath(s.id), []byte(strings.Join(env, "\n")+"\n"), 0o600); err != nil {
		p.forget(s)
		return err
	}
	if err := p.systemctl("start", s.unit); err != nil {
		p.forget(s)
		return err
	}
	if err := waitReady(s.port, time.Duration(p.cfg.ReadyTimeout)); err != nil {
		p.close(s, "never answered")
		return err
	}
	return nil
}

// Called with mu held. A session being stopped keeps its port until the unit
// is gone, or the next session could be given a port still bound by it
func (p *proxy) freePort() (int, bool) {
	used := map[int]bool{}
	for _, s := range p.sessions {
		used[s.port] = true
	}
	for port := p.cfg.FirstPort; port < p.cfg.FirstPort+p.cfg.Sessions; port++ {
		if !used[port] {
			return port, true
		}
	}
	return 0, false
}

// Not handed to the browser at once: right after the unit starts nothing is
// listening yet, and the browser would be shown a bad gateway
func waitReady(port int, timeout time.Duration) error {
	u := "http://" + backendAddr(port) + "/"
	c := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := c.Get(u)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing answered on port %d within %v", port, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func backendAddr(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func (p *proxy) attach(id string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[id]
	if s == nil || s.closing {
		return nil
	}
	s.inflight++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return s
}

func (p *proxy) detach(s *session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s.inflight--
	if s.inflight == 0 {
		p.idle(s)
	}
}

// Called with mu held. Not stopped the moment the last request ends: a page
// loads its assets before it opens a WebSocket, and a reload drops the
// WebSocket before opening a new one
func (p *proxy) idle(s *session) {
	if s.closing {
		return
	}
	s.timer = time.AfterFunc(time.Duration(p.cfg.Grace), func() { p.expire(s) })
}

func (p *proxy) expire(s *session) {
	p.mu.Lock()
	// A request can arrive while the timer is firing
	if s.inflight > 0 || s.closing {
		p.mu.Unlock()
		return
	}
	s.closing = true
	p.mu.Unlock()
	p.stop(s, "idle")
}

func (p *proxy) close(s *session, why string) {
	p.mu.Lock()
	if s.closing {
		p.mu.Unlock()
		return
	}
	s.closing = true
	if s.timer != nil {
		s.timer.Stop()
	}
	p.mu.Unlock()
	p.stop(s, why)
}

// Called once for each session, after closing is set
func (p *proxy) stop(s *session, why string) {
	if err := p.systemctl("stop", s.unit); err != nil {
		log.Printf("%s %s: %v", s.kind, s.id, err)
	}
	p.forget(s)
	log.Printf("%s %s: stopped, %s", s.kind, s.id, why)
}

func (p *proxy) forget(s *session) {
	os.Remove(p.envPath(s.id))
	p.mu.Lock()
	delete(p.sessions, s.id)
	p.mu.Unlock()
}

// Not left for the unit's RuntimeMaxSec to collect: whatever someone logged
// in to through a session stays logged in until then
func (p *proxy) closeAll(why string) {
	p.mu.Lock()
	all := make([]*session, 0, len(p.sessions))
	for _, s := range p.sessions {
		all = append(all, s)
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.close(s, why)
		}()
	}
	wg.Wait()
}

func (p *proxy) sweep() {
	for _, k := range p.cfg.Kinds {
		if err := p.systemctl("stop", k.Unit+"@*.service"); err != nil {
			log.Print(err)
		}
	}
	envs, _ := filepath.Glob(filepath.Join(p.runDir, "*.env"))
	for _, e := range envs {
		os.Remove(e)
	}
}
