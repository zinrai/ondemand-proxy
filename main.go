// ondemand-proxy starts a backend as a systemd unit for each request, relays
// HTTP and WebSocket traffic to it, and stops it once nothing has been relayed
// to it for a while.
//
// The lifecycle is left to systemd rather than run here: a template unit
// already says how to start, stop, time out and clean up a backend, and the
// proxy only asks systemd to start and stop it
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(0)

	configPath := flag.String("config", "/etc/ondemand-proxy/config.json", "configuration file")
	listen := flag.String("listen", ":8080", "address to serve on")
	runDir := flag.String("run-dir", "/run/ondemand-proxy", "where each unit's environment file is written")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	p := newProxy(cfg, *runDir)
	p.sweep()

	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	// No WriteTimeout: a WebSocket is written to for as long as it is open
	srv := &http.Server{Handler: p.routes(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(l) }()
	log.Printf("listening on %s", *listen)

	select {
	case err := <-errc:
		p.closeAll("the proxy failed")
		log.Fatal(err)
	case <-ctx.Done():
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	p.closeAll("the proxy stopped")
}
