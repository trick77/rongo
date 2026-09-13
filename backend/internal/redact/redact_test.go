package redact

import (
	"strings"
	"testing"
)

func TestRedact_configLines(t *testing.T) {
	cases := []struct {
		name, path, in, want string
	}{
		{"jasypt value on any key", "app.properties", "spring.datasource.url=ENC(abc123def)", "spring.datasource.url=<redacted>"},
		{"password key, plaintext", "app.properties", "spring.datasource.password=hunter2", "spring.datasource.password=<redacted>"},
		{"password key, placeholder kept", "app.properties", "jasypt.encryptor.password=${MASTER_KEY}", "jasypt.encryptor.password=${MASTER_KEY}"},
		{"password key, empty value untouched", "app.properties", "spring.mail.password=", "spring.mail.password="},
		{"consumer key inside camel case", "app.properties", "esb.consumerKey=59dfa0b", "esb.consumerKey=<redacted>"},
		{"kebab consumer key", "app.properties", "acme.esb-consumer-key=59dfa0b", "acme.esb-consumer-key=<redacted>"},
		{"keystore password", "app.properties", "acme.jwtKeystorePassword=ENC(x)", "acme.jwtKeystorePassword=<redacted>"},
		{"bare key is not secret", "app.properties", "spring.kafka.producer.key-serializer=org.apache.kafka.common.serialization.StringSerializer", "spring.kafka.producer.key-serializer=org.apache.kafka.common.serialization.StringSerializer"},
		{"keystore path stays", "app.properties", "acme.jwtKeystorePath=file:/app/certs/store.jks", "acme.jwtKeystorePath=file:/app/certs/store.jks"},
		{"jaas line carries a password", "app.properties", `spring.kafka.properties.sasl.jaas.config=org.apache.kafka.common.security.plain.PlainLoginModule required username="svc" password="pw";`, "spring.kafka.properties.sasl.jaas.config=<redacted>"},
		{"comment untouched", "app.properties", "# password=notreally", "# password=notreally"},
		{"jwt value", "app.properties", "acme.client.assertion=eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0In0.abc", "acme.client.assertion=<redacted>"},
		{"base64 blob value", "app.properties", "acme.blob=QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5QUJDREVGR0g=", "acme.blob=<redacted>"},
		{"hex value", "app.properties", "acme.digest=0123456789abcdef0123456789abcdef", "acme.digest=<redacted>"},
		{"cron value stays", "app.properties", "acme.cron.send-digest=0 0 * ? * * *", "acme.cron.send-digest=0 0 * ? * * *"},
		{"credentials in a url", "app.properties", "spring.datasource.url=jdbc:postgresql://app:hunter2@db.example.invalid:5432/app", "spring.datasource.url=<redacted>"},
		{"url without credentials stays", "app.properties", "spring.datasource.url=jdbc:postgresql://db.example.invalid:5432/app", "spring.datasource.url=jdbc:postgresql://db.example.invalid:5432/app"},
		{"basic auth value", "values.yaml", "  authorization: Basic YWJjOmRlZg==", "  authorization: <redacted>"},
		{"bearer value", ".env", "AUTH=Bearer abc.def", "AUTH=<redacted>"},
		{"aws key id value", "config.ini", "aws_access_key_id = AKIAIOSFODNN7EXAMPLE", "aws_access_key_id = <redacted>"},
		{"vendor token prefix", "app.properties", "forge.pat=ghp_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8", "forge.pat=<redacted>"},
		{"access key name", "app.properties", "storage.accessKey=abc", "storage.accessKey=<redacted>"},
		{"passphrase name", "app.properties", "ssh.passphrase=abc", "ssh.passphrase=<redacted>"},
		{"pass as a segment", "app.properties", "acme.api.pass=abc", "acme.api.pass=<redacted>"},
		{"pass inside bypass stays", "app.properties", "proxy.bypass=localhost", "proxy.bypass=localhost"},
		{"key as the last segment", "app.properties", "acme.maps.key=abc", "acme.maps.key=<redacted>"},
		{"key as a camel segment", "app.properties", "acme.mapsKey=abc", "acme.mapsKey=<redacted>"},
		{"key as the only segment is a name", "deployment.yaml", "        key: application.properties", "        key: application.properties"},
		{"keystore is not key", "app.properties", "acme.keystore=file:/x", "acme.keystore=file:/x"},
		{"german password", "app.properties", "acme.link.passwort=abc", "acme.link.passwort=<redacted>"},
		{"fixed session id", "app.properties", "acme.authz.fixed-session-id=6f1c2b3a-0000-4000-8000-000000000000", "acme.authz.fixed-session-id=<redacted>"},
		{"nginx header line without a key", "nginx.conf", `    proxy_set_header Authorization "Basic YWJjOmRlZjEyMw==";`, `    proxy_set_header Authorization "<redacted>";`},
		{"nginx line without a credential stays", "nginx.conf", `    proxy_set_header Host $host;`, `    proxy_set_header Host $host;`},
		{"url credentials mid-line without a key", "nginx.conf", `    proxy_pass https://svc:hunter2@upstream.example.invalid/;`, `    proxy_pass https<redacted>upstream.example.invalid/;`},
		{"jasypt mid-line without a key", "app.conf", `export DB_URL_WITH_ENC "x ENC(abc) y"`, `export DB_URL_WITH_ENC "x <redacted> y"`},
		{"image name with long path stays", "kustomization.yaml", "  - name: registry.example.invalid/team-public/product/product-intranet-service", "  - name: registry.example.invalid/team-public/product/product-intranet-service"},
		{"url with long path stays", "app.properties", "acme.nexus=https://nexus.example.invalid/content/groups/public/ch/acme/stager2/maven-metadata.xml", "acme.nexus=https://nexus.example.invalid/content/groups/public/ch/acme/stager2/maven-metadata.xml"},
		{"yaml key", "values.yaml", "  dbPassword: hunter2", "  dbPassword: <redacted>"},
		{"yaml quoted value", "values.yaml", `  token: "abc"`, `  token: "<redacted>"`},
		{"yaml list item", "values.yaml", "  - apiKey: abc", "  - apiKey: <redacted>"},
		{"yaml placeholder kept", "values.yaml", "  password: ${DB_PASSWORD}", "  password: ${DB_PASSWORD}"},
		{"json quoted key", "config.json", `  "password": "hunter2",`, `  "password": "<redacted>",`},
		{"json bare key stays", "config.json", `  "port": 8080,`, `  "port": 8080,`},
		{"env file", ".env", "API_TOKEN=abc", "API_TOKEN=<redacted>"},
		{"toml", "config.toml", `secret = "abc"`, `secret = "<redacted>"`},
		{"java is not touched", "Config.java", `String password = System.getenv("PW");`, `String password = System.getenv("PW");`},
		{"go is not touched", "main.go", `password := os.Getenv("PW")`, `password := os.Getenv("PW")`},
		{"markdown is not touched", "README.md", "password=hunter2", "password=hunter2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(Redact(c.path, []byte(c.in)))
			if got != c.want {
				t.Fatalf("Redact(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

func TestRedact_yamlBlockScalar(t *testing.T) {
	in := strings.Join([]string{
		"data:",
		"  privateKey: |",
		"    line one",
		"    line two",
		"  other: keep",
		"  note: >-",
		"    folded",
		"top: keep",
	}, "\n")
	want := strings.Join([]string{
		"data:",
		"  privateKey: <redacted>",
		"  other: keep",
		"  note: >-",
		"    folded",
		"top: keep",
	}, "\n")
	if got := string(Redact("secret.yaml", []byte(in))); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRedact_keepsLineCount(t *testing.T) {
	in := "a=1\npassword=x\nb=2\n"
	got := string(Redact("x.properties", []byte(in)))
	if strings.Count(got, "\n") != 3 {
		t.Fatalf("line count changed: %q", got)
	}
	if !strings.HasSuffix(got, "b=2\n") {
		t.Fatalf("trailing newline lost: %q", got)
	}
}

func TestRedact_crlf(t *testing.T) {
	got := string(Redact("x.properties", []byte("password=x\r\nb=2\r\n")))
	if got != "password=<redacted>\r\nb=2\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRedact_untouchedBodyIsReturnedAsIs(t *testing.T) {
	in := []byte("a=1\nb=2\n")
	if got := Redact("x.properties", in); string(got) != string(in) {
		t.Fatalf("got %q", got)
	}
}

func TestIsConfigPath(t *testing.T) {
	for p, want := range map[string]bool{
		"a/b/application.properties": true, "x.yaml": true, "x.yml": true, ".env": true,
		"x.json": true, "x.toml": true, "x.ini": true, "nginx.conf": true,
		"Main.java": false, "main.go": false, "README.md": false, "x.jks": false,
	} {
		if got := IsConfigPath(p); got != want {
			t.Errorf("IsConfigPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestSecretManifest(t *testing.T) {
	cases := map[string]bool{
		"apiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n":                            true,
		"apiVersion: bitnami.com/v1alpha1\nkind: SealedSecret\nspec:\n  encryptedData:\n": true,
		"apiVersion: apps/v1\nkind: Deployment\n":                                         false,
		"kind: Kustomization\nsecretGenerator:\n":                                         false,
		"  kind: Secret\n": false,
	}
	for in, want := range cases {
		if got := SecretManifest("x.yaml", []byte(in)); got != want {
			t.Errorf("SecretManifest(%q) = %v, want %v", in, got, want)
		}
	}
	if SecretManifest("x.properties", []byte("kind: Secret\n")) {
		t.Error("a properties file is not a manifest")
	}
}
