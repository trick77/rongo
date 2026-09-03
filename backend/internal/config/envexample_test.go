package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// activeEnvExampleVars returns the BACKEND_* names that .env.example leaves
// uncommented, i.e. the ones it presents as "you must set this".
func activeEnvExampleVars(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../../.env.example")
	if err != nil {
		t.Fatalf("cannot read .env.example: %v", err)
	}
	defer f.Close()

	var active []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, _ := strings.Cut(line, "=")
		active = append(active, name)
	}
	if err := s.Err(); err != nil {
		t.Fatalf("cannot read .env.example: %v", err)
	}
	return active
}

// The file drifted once already: settings that Load happily defaults stood
// active next to real mandatory ones, and the section header was the only
// thing saying which was which. Load is the arbiter, not the header.
func TestEnvExample_activeLinesAreTheOnesLoadCannotDefault(t *testing.T) {
	// Given a copy of .env.example with every mandatory value filled in
	active := activeEnvExampleVars(t)
	filled := mandatoryEnv
	for _, name := range active {
		if _, ok := filled[name]; !ok {
			t.Fatalf("%s is active in .env.example but this test has no value for it; "+
				"either it belongs behind a #, or add it here on purpose", name)
		}
	}

	// and nothing mandatory hiding behind a "#"
	for name := range filled {
		found := false
		for _, a := range active {
			if a == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is mandatory but commented out in .env.example; "+
				"whoever copies the file would not know to set it", name)
		}
	}

	// When every active variable is set
	setEnv(t, filled)

	// Then the process starts, and dropping any one of them stops it
	if _, err := Load(); err != nil {
		t.Fatalf("a filled-in .env.example copy must load, got: %v", err)
	}
	for _, name := range active {
		t.Run(name, func(t *testing.T) {
			// Given the same environment minus this one variable. setEnv seeds
			// the mandatory set, so dropping one means overriding it to "".
			setEnv(t, map[string]string{name: ""})

			// When / Then
			if _, err := Load(); err == nil {
				t.Errorf("%s is active in .env.example, so it must be mandatory, "+
					"but Load starts without it - comment it out with its default", name)
			}
		})
	}
}
