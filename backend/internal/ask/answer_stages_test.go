package ask

import (
	"context"
	"strings"
	"testing"
)

// stagedSources is the shape of a configuration question once gathered: the
// job in the service, its default properties beside it, and the deployed
// value in every stage of the infrastructure repository.
func stagedSources() []Source {
	return []Source{
		{ChunkID: 1, Repo: "acme-service", Branch: "master", Path: "src/main/java/acme/JobSendDigest.java",
			StartLine: 1, EndLine: 20, Text: `@Scheduled(cron = "${acme.cron.send-digest}")`, Reason: "hit"},
		{ChunkID: 2, Repo: "acme-service", Branch: "master", Path: "src/main/resources/application-default.properties",
			StartLine: 1, EndLine: 10, Text: "acme.cron.send-digest=0/20 * * ? * * *", Reason: "hit"},
		{ChunkID: 3, Repo: "acme-infra", Branch: "main", Path: "intg/intranet/application.properties",
			StartLine: 1, EndLine: 10, Text: "acme.cron.send-digest=0 0 * ? * * *", Reason: "edge:property acme.cron.send-digest from acme-service/src/main/java/acme/JobSendDigest.java"},
		{ChunkID: 4, Repo: "acme-infra", Branch: "main", Path: "prod/intranet/application.properties",
			StartLine: 1, EndLine: 10, Text: "acme.cron.send-digest=0 0 * ? * * *", Reason: "edge:property acme.cron.send-digest from acme-service/src/main/java/acme/JobSendDigest.java"},
	}
}

func TestAnswer_stagedSourcesAreLabelledAndTheRuleSaysReportEveryStage(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	scope := Scope{Known: []string{"acme-service", "acme-infra"}, Stages: declaredStages}
	if _, err := NewAnswerer(c).Answer(context.Background(), "How often?", AudienceBA, LanguageEN, stagedSources(), scope, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	p := *prompt
	if !strings.Contains(p, "prod/intranet/application.properties:1-10 (stage prod)") {
		t.Errorf("the prod source is not labelled with its stage:\n%s", p)
	}
	if !strings.Contains(p, "intg/intranet/application.properties:1-10 (stage intg)") {
		t.Errorf("the intg source is not labelled with its stage:\n%s", p)
	}
	if strings.Contains(p, "application-default.properties:1-10 (stage") {
		t.Error("the service's own default properties got a stage label")
	}
	if !strings.Contains(p, "deployed configuration") {
		t.Error("the prompt does not say what a file under a stage directory is")
	}
	if !strings.Contains(p, "every stage") || strings.Contains(p, "was asked for the stage") {
		t.Error("with no stage asked the rule must say to report every stage, and not the one-stage rule")
	}
}

func TestAnswer_anAskedStageRulesOutTheOthers(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	scope := Scope{Known: []string{"acme-service", "acme-infra"}, Stages: declaredStages, Stage: "prod"}
	sources := stagedSources()
	sources = append(sources[:2], sources[3])
	if _, err := NewAnswerer(c).Answer(context.Background(), "How often in production?", AudienceBA, LanguageEN, sources, scope, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	p := *prompt
	if !strings.Contains(p, "was asked for the stage prod") {
		t.Errorf("the prompt does not name the asked stage:\n%s", p)
	}
	if !strings.Contains(p, "other stages were not looked at") {
		t.Error("the prompt does not tell the model to say the other stages were left out")
	}
}

func TestAnswer_noStagedSourceMeansNoStageRule(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	scope := Scope{Known: []string{"peeq"}, Stages: declaredStages}
	if _, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), scope, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if strings.Contains(*prompt, "deployed configuration") || strings.Contains(*prompt, "(stage ") {
		t.Error("a turn with no source under a stage directory carries the stage rule anyway")
	}
}

func TestAnswer_redactedMarkerIsExplainedToTheModel(t *testing.T) {
	c, prompt, _ := streamUpstream(t, "x")
	if _, err := NewAnswerer(c).Answer(context.Background(), "How?", AudienceBA, LanguageEN, twoSources(), Scope{}, "", nil); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if !strings.Contains(*prompt, "<redacted>") {
		t.Error("the prompt never says what the redaction marker means")
	}
}
