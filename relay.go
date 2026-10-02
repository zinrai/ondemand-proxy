package main

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func (p *proxy) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{kind}", p.start)
	mux.HandleFunc("/s/{id}/{path...}", p.relay)
	return mux
}

func (p *proxy) start(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("kind")
	k, ok := p.cfg.Kinds[name]
	if !ok {
		http.NotFound(w, r)
		return
	}

	s, err := p.open(name, k, r.URL.Query())
	switch {
	case errors.Is(err, errBadQuery):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, errFull):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		log.Printf("%s: %v", name, err)
		http.Error(w, "the session could not be started", http.StatusBadGateway)
		return
	}

	http.Redirect(w, r, "/s/"+s.id+"/"+k.entryFor(s.id), http.StatusSeeOther)
}

func (p *proxy) relay(w http.ResponseWriter, r *http.Request) {
	// Every request counts as use, not only a WebSocket: a backend that never
	// opens one would otherwise be stopped while it is being used
	s := p.attach(r.PathValue("id"))
	if s == nil {
		http.NotFound(w, r)
		return
	}
	defer p.detach(s)
	s.proxy.ServeHTTP(w, r)
}

func relayTo(port int) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: backendAddr(port)}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + pr.In.PathValue("path")
			pr.Out.URL.RawPath = ""
		},
	}
}
