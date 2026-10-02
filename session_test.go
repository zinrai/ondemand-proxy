package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

const grace = 50 * time.Millisecond

// A session that nothing is relayed to is stopped once the grace period has
// passed, and its port is free again
func TestIdleSessionIsStopped(t *testing.T) {
	p, units := testProxy(t)
	s := addSession(t, p, "a", backend(t, "a"))

	time.Sleep(4 * grace)

	if !units.stopped(s.unit) {
		t.Errorf("%s was not stopped", s.unit)
	}
	if p.attach(s.id) != nil {
		t.Error("the session is still served")
	}
	p.mu.Lock()
	if _, ok := p.freePort(); !ok {
		t.Error("the port was not freed")
	}
	p.mu.Unlock()
}

// A session is not stopped while a request to it is still being relayed, even
// a plain HTTP one, and is stopped once it ends and the grace period passes
func TestSessionInUseIsKept(t *testing.T) {
	p, units := testProxy(t)

	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(slow.Close)
	s := addSession(t, p, "a", slow)

	front := httptest.NewServer(p.routes())
	t.Cleanup(front.Close)
	// Cleanups run last first, so a failed test frees the request before
	// either server waits on it
	t.Cleanup(free)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Get(front.URL + "/s/" + s.id + "/")
		if err == nil {
			resp.Body.Close()
		}
	}()

	time.Sleep(4 * grace)
	if units.stopped(s.unit) {
		t.Fatal("stopped while a request was being relayed")
	}

	free()
	<-done
	time.Sleep(4 * grace)
	if !units.stopped(s.unit) {
		t.Error("not stopped after the request ended")
	}
}

// A request is relayed to the session its path names, with the session prefix
// taken off, and a session id that does not exist reaches nothing
func TestRequestReachesItsOwnSession(t *testing.T) {
	p, _ := testProxy(t)
	a := addSession(t, p, "a", backend(t, "a"))
	b := addSession(t, p, "b", backend(t, "b"))

	front := httptest.NewServer(p.routes())
	t.Cleanup(front.Close)

	for _, c := range []struct{ id, want string }{
		{a.id, "a /vnc.html?path=x"},
		{b.id, "b /vnc.html?path=x"},
	} {
		resp, err := http.Get(front.URL + "/s/" + c.id + "/vnc.html?path=x")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != c.want {
			t.Errorf("session %s: got %q, want %q", c.id, body, c.want)
		}
	}

	resp, err := http.Get(front.URL + "/s/" + newID() + "/vnc.html")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown session gave %d", resp.StatusCode)
	}
}

type fakeUnits struct {
	mu   sync.Mutex
	stop map[string]bool
}

func (f *fakeUnits) systemctl(args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 2 && args[0] == "stop" {
		f.stop[args[1]] = true
	}
	return nil
}

func (f *fakeUnits) stopped(unit string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stop[unit]
}

func testProxy(t *testing.T) (*proxy, *fakeUnits) {
	t.Helper()
	cfg := config{Grace: duration(grace), FirstPort: 20000, Sessions: 2}
	p := newProxy(cfg, t.TempDir())
	units := &fakeUnits{stop: map[string]bool{}}
	p.systemctl = units.systemctl
	return p, units
}

// Answers every request with its name and the path and query it was given
func backend(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", name, r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A session as open leaves it once its unit answers, with srv standing in for
// the unit
func addSession(t *testing.T, p *proxy, name string, srv *httptest.Server) *session {
	t.Helper()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}

	id := newID()
	s := &session{id: id, kind: name, unit: unitFor("ondemand-test", id), port: n, proxy: relayTo(n)}
	p.mu.Lock()
	p.sessions[id] = s
	p.idle(s)
	p.mu.Unlock()
	return s
}
