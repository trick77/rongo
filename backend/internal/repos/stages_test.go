package repos

import (
	"strings"
	"testing"
)

const stagedProject = `
projects:
  - name: acme
    repositories:
      - name: acme-service
        clone_url: https://forge.example.invalid/acme/acme-service.git
        part: backend
      - name: acme-infra
        clone_url: https://forge.example.invalid/acme/acme-infra.git
        part: infra
        stages:
          - name: prod
            path: prod/**
            aliases: [production, Produktion, produktiv]
          - name: intg
            path: intg/
`

func TestLoad_readsStages(t *testing.T) {
	// Given: an infrastructure repository declaring two of its three stage
	// directories, one with aliases and one without.
	specs, err := Load(writeYAML(t, stagedProject))

	// Then
	if err != nil {
		t.Fatalf("Load() err = %v, want nil", err)
	}
	infra := specs[1]
	if len(infra.Stages) != 2 {
		t.Fatalf("Stages = %+v, want two", infra.Stages)
	}
	if got := infra.Stages[0]; got.Name != "prod" || got.Prefix != "prod/" {
		t.Errorf("stage 0 = %+v, want prod under prod/", got)
	}
	if got := infra.Stages[0].Aliases; len(got) != 3 || got[0] != "production" || got[1] != "produktion" {
		t.Errorf("aliases = %v, want three, lower-cased", got)
	}
	if got := infra.Stages[1]; got.Name != "intg" || got.Prefix != "intg/" || len(got.Aliases) != 0 {
		t.Errorf("stage 1 = %+v, want intg under intg/ with no aliases", got)
	}
	if len(specs[0].Stages) != 0 {
		t.Errorf("the service declares stages: %+v", specs[0].Stages)
	}
}

func TestLoad_refusesBadStages(t *testing.T) {
	cases := []struct {
		name, stages, want string
	}{
		{"no name", "          - path: prod/**\n", "stage with no name"},
		{"no path", "          - name: prod\n", "stage \"prod\" has no path"},
		{"absolute path", "          - name: prod\n            path: /prod/**\n", "relative"},
		{"duplicate name", "          - name: prod\n            path: prod/\n          - name: prod\n            path: production/\n", "twice"},
		{"alias equals another stage", "          - name: prod\n            path: prod/\n            aliases: [intg]\n          - name: intg\n            path: intg/\n", "twice"},
		{"alias is a repository", "          - name: prod\n            path: prod/\n            aliases: [acme-service]\n", "repository"},
		{"alias is a project", "          - name: prod\n            path: prod/\n            aliases: [acme]\n", "project"},
		{"alias is an ordinary word", "          - name: intg\n            path: intg/\n            aliases: [integration]\n", "ordinary word"},
		{"name is an ordinary word", "          - name: test\n            path: test/\n", "ordinary word"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := `
projects:
  - name: acme
    repositories:
      - name: acme-service
        clone_url: https://forge.example.invalid/acme/acme-service.git
      - name: acme-infra
        clone_url: https://forge.example.invalid/acme/acme-infra.git
        stages:
` + c.stages
			_, err := Load(writeYAML(t, body))
			if err == nil {
				t.Fatalf("Load() err = nil, want a refusal mentioning %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want it to mention %q", err, c.want)
			}
		})
	}
}
