package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

type config struct {
	Grace        duration        `json:"grace"`
	ReadyTimeout duration        `json:"ready_timeout"`
	FirstPort    int             `json:"first_port"`
	Sessions     int             `json:"sessions"`
	Kinds        map[string]kind `json:"kinds"`
}

type kind struct {
	Unit   string   `json:"unit"`
	Params []string `json:"params"`
	Entry  string   `json:"entry"`
}

type duration time.Duration

func (d *duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = duration(v)
	return nil
}

var (
	kindName  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	paramName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	unitName  = regexp.MustCompile(`^[A-Za-z0-9:_.-]+$`)

	// Not checked for meaning, such as being an address: a wrong value fails in
	// the unit, in plain sight. Only characters that cannot add a variable to the
	// EnvironmentFile or an argument to the unit's command line are let through
	paramValue = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
)

func loadConfig(path string) (config, error) {
	c := config{
		Grace:        duration(2 * time.Minute),
		ReadyTimeout: duration(30 * time.Second),
		FirstPort:    20000,
		Sessions:     8,
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return config{}, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.check(); err != nil {
		return config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c config) check() error {
	if c.FirstPort < 1024 || c.Sessions < 1 || c.FirstPort+c.Sessions-1 > 65535 {
		return fmt.Errorf("ports %d to %d are out of range", c.FirstPort, c.FirstPort+c.Sessions-1)
	}
	if len(c.Kinds) == 0 {
		return fmt.Errorf("no kinds")
	}
	for name, k := range c.Kinds {
		if err := k.check(name); err != nil {
			return fmt.Errorf("kind %q: %w", name, err)
		}
	}
	return nil
}

func (k kind) check(name string) error {
	// Not s: /s/ is where sessions are served
	if !kindName.MatchString(name) || name == "s" {
		return fmt.Errorf("bad name")
	}
	if !unitName.MatchString(k.Unit) {
		return fmt.Errorf("bad unit %q", k.Unit)
	}
	if strings.HasPrefix(k.Entry, "/") {
		return fmt.Errorf("entry has to be relative to the session")
	}
	seen := map[string]bool{}
	for _, p := range k.Params {
		// Not PORT: the proxy sets it
		if !paramName.MatchString(p) || strings.ToUpper(p) == "PORT" || seen[p] {
			return fmt.Errorf("bad parameter name %q", p)
		}
		seen[p] = true
	}
	return nil
}

func (k kind) entryFor(id string) string {
	return strings.ReplaceAll(k.Entry, "{id}", id)
}

func (k kind) env(query map[string][]string) ([]string, error) {
	for p := range query {
		if !slices.Contains(k.Params, p) {
			return nil, fmt.Errorf("unknown parameter %q", p)
		}
	}

	var env []string
	for _, p := range k.Params {
		vs := query[p]
		if len(vs) != 1 {
			return nil, fmt.Errorf("parameter %q has to be given once", p)
		}
		if !paramValue.MatchString(vs[0]) {
			return nil, fmt.Errorf("parameter %q has characters other than letters, digits and . _ : -", p)
		}
		env = append(env, strings.ToUpper(p)+"="+vs[0])
	}
	return env, nil
}
