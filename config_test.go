package main

import (
	"reflect"
	"testing"
)

// A value made of letters, digits and . _ : - is handed over as given, under
// the parameter's name upper-cased, whatever it means. Anything that could add
// a line, a variable or an argument where the unit uses it is refused
func TestParametersCannotAddVariablesOrArguments(t *testing.T) {
	k := kind{Unit: "ondemand-bmc", Params: []string{"ip"}}

	for _, v := range []string{"10.0.0.12", "fe80::1", "bmc01.example.internal", "no-such-host"} {
		got, err := k.env(map[string][]string{"ip": {v}})
		if err != nil {
			t.Errorf("refused %q: %v", v, err)
			continue
		}
		if want := []string{"IP=" + v}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}

	for _, q := range []map[string][]string{
		{"ip": {"10.0.0.12\nPORT=22"}},
		{"ip": {"10.0.0.12 --privileged"}},
		{"ip": {"10.0.0.12\t--privileged"}},
		{"ip": {`"10.0.0.12"`}},
		{"ip": {"$HOME"}},
		{"ip": {"10.0.0.12\\"}},
		{"ip": {""}},
		{"ip": {"10.0.0.12", "10.0.0.13"}},
		{"ip": {"10.0.0.12"}, "port": {"22"}},
		{},
	} {
		if env, err := k.env(q); err == nil {
			t.Errorf("accepted %q as %q", q, env)
		}
	}
}
