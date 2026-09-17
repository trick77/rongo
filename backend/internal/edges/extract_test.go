package edges

import (
	"testing"
)

// values returns the tokens of one kind, so a test can say what it means
// without walking the slice.
func values(toks []Token, kind Kind) []string {
	var out []string
	for _, t := range toks {
		if t.Kind == kind {
			out = append(out, t.Value)
		}
	}
	return out
}

func has(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

// The three edges of the pinned Sock Shop corpus, each written the way the real
// file writes it. If any of these stops being found, an answer stops being able
// to cross that boundary — so they are here as fixtures rather than as a corpus
// run that needs credentials.
func TestExtractFindsTheProducerSideOfAQueue(t *testing.T) {
	// Given: shipping's configuration and controller, verbatim in shape.
	testee := []byte(`
public class RabbitMqConfiguration {
    final static String queueName = "shipping-task";

    @Bean
    Queue queue() {
        return new Queue(queueName, false);
    }
}
`)

	// When
	got := Extract("RabbitMqConfiguration.java", testee)

	// Then
	if !has(values(got, KindDestination), "shipping-task") {
		t.Fatalf("the producer's queue name was not extracted: %+v", got)
	}
}

func TestExtractFindsTheConsumerSideOfAQueue(t *testing.T) {
	// Given: queue-master, which names the same queue in its own words.
	testee := []byte(`
public class ShippingConsumerConfiguration {
	protected final String queueName = "shipping-task";

	public SimpleMessageListenerContainer container() {
		container.setQueueNames(this.queueName);
	}
}
`)

	// When
	got := Extract("ShippingConsumerConfiguration.java", testee)

	// Then
	if !has(values(got, KindDestination), "shipping-task") {
		t.Fatalf("the consumer's queue name was not extracted: %+v", got)
	}
}

func TestExtractFindsADestinationPassedStraightToTheCall(t *testing.T) {
	// Given: the send that names its queue inline, with no constant.
	testee := []byte(`rabbitTemplate.convertAndSend("shipping-task", shipment);`)

	// When
	got := Extract("ShippingController.java", testee)

	// Then
	if !has(values(got, KindDestination), "shipping-task") {
		t.Fatalf("an inline destination was not extracted: %+v", got)
	}
}

func TestExtractFindsBothEndsOfAnHTTPRoute(t *testing.T) {
	// Given: the Java client that calls the route, and the Go server that
	// serves it.
	client := []byte(`return new ServiceUri(new Hostname("payment"), new Domain(domain), "/paymentAuth").toUri();`)
	server := []byte(`r.Methods("POST").Path("/paymentAuth").Handler(httptransport.NewServer(`)

	// When
	gotClient := Extract("OrdersConfigurationProperties.java", client)
	gotServer := Extract("transport.go", server)

	// Then
	if !has(values(gotClient, KindRoute), "/paymentAuth") {
		t.Errorf("the client end of the route was not extracted: %+v", gotClient)
	}
	if !has(values(gotServer, KindRoute), "/paymentAuth") {
		t.Errorf("the server end of the route was not extracted: %+v", gotServer)
	}
}

func TestExtractFindsAnExpressRoute(t *testing.T) {
	// Given: the Node front-end, both as a server and as a client.
	testee := []byte(`
  app.post("/orders", function(req, res, next) {
    request({uri: endpoints.ordersUrl + '/orders', method: 'POST'});
  });
`)

	// When
	got := Extract("index.js", testee)

	// Then
	if !has(values(got, KindRoute), "/orders") {
		t.Fatalf("the Express route was not extracted: %+v", got)
	}
}

func TestExtractIgnoresRoutesEveryServiceHas(t *testing.T) {
	// Given: the health endpoint, which exists in all eight repositories.
	testee := []byte(`@RequestMapping(method = RequestMethod.GET, path = "/health")`)

	// When
	got := Extract("HealthCheckController.java", testee)

	// Then: no token, because an edge on /health would join every repository to
	// every other and mean nothing.
	if len(values(got, KindRoute)) != 0 {
		t.Fatalf("/health was recorded as an edge: %+v", got)
	}
}

func TestExtractIgnoresProseAndDocumentation(t *testing.T) {
	// Given: a markdown file that talks about the very same route.
	testee := []byte("The order service posts to `/paymentAuth` when checking out.\n")

	// When
	got := Extract("README.md", testee)

	// Then: documentation is a claim about the code, not the code.
	if len(got) != 0 {
		t.Fatalf("a route was extracted from prose: %+v", got)
	}
}

func TestExtractRefusesASentenceAsADestination(t *testing.T) {
	// Given: a messaging line whose literal is a log message.
	testee := []byte(`log.info("Unable to add to queue (the queue is probably down)"); rabbitTemplate.convertAndSend(q, m);`)

	// When
	got := Extract("ShippingController.java", testee)

	// Then: a sentence is not a queue name.
	for _, v := range values(got, KindDestination) {
		if v != "" && len(v) > 40 {
			t.Fatalf("a sentence was recorded as a destination: %q", v)
		}
	}
}

func TestExtractDeduplicatesWithinAFile(t *testing.T) {
	// Given: the same route declared twice, as controllers often do.
	testee := []byte(`
	@RequestMapping(value = "/shipping", method = RequestMethod.GET)
	@RequestMapping(value = "/shipping", method = RequestMethod.POST)
`)

	// When
	got := Extract("ShippingController.java", testee)

	// Then
	if n := len(values(got, KindRoute)); n != 1 {
		t.Fatalf("expected one route, got %d: %+v", n, got)
	}
}

func TestExtractFindsAPropertyPlaceholderInJVMCode(t *testing.T) {
	// Given: a Spring job whose schedule is a property, and a value with a
	// default. The key is the literal the deployed configuration repeats.
	testee := []byte(`
@Component
public class JobSendDigest {
    @Scheduled(cron = "${acme.cron.send-digest}")
    public void run() {}

    @Value("${acme.mail.retries:3}")
    private int retries;
}
`)

	// When
	toks := Extract("src/main/java/acme/JobSendDigest.java", testee)

	// Then
	got := values(toks, KindProperty)
	if !has(got, "acme.cron.send-digest") {
		t.Errorf("placeholder key not recorded: %v", got)
	}
	if !has(got, "acme.mail.retries") {
		t.Errorf("key with default not recorded, or recorded with its default: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want exactly the two keys", got)
	}
	for _, tk := range toks {
		if tk.Value == "acme.cron.send-digest" && tk.Line != 4 {
			t.Errorf("line = %d, want 4", tk.Line)
		}
	}
}

func TestExtractReadsThePropertyKeysOfAPropertiesFile(t *testing.T) {
	// Given: a stage's properties, with a comment, a blank, a placeholder
	// value and a redacted one. Every key is a token; comments are not.
	testee := []byte(`# Cron
acme.cron.send-digest=0 0 * ? * * *

acme.mail.retries = 5
db.password=<redacted>
! old-style comment=x
acme.mail.host: mail.example.invalid
`)

	// When
	toks := Extract("prod/intranet/application.properties", testee)

	// Then
	got := values(toks, KindProperty)
	want := []string{"acme.cron.send-digest", "acme.mail.retries", "db.password", "acme.mail.host"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, tk := range toks {
		if tk.Value == "acme.mail.retries" && tk.Line != 4 {
			t.Errorf("line = %d, want 4", tk.Line)
		}
	}
	if n := len(values(toks, KindRoute)) + len(values(toks, KindDestination)); n != 0 {
		t.Errorf("a properties file yielded %d route or destination tokens", n)
	}
}

func TestExtractDoesNotReadTemplateInterpolationAsAProperty(t *testing.T) {
	// Given: TypeScript with a template literal and a Go file with a shell
	// style placeholder. Neither is a Spring property, and recording one
	// would join a front-end to whatever repository has a "cart.id" key.
	for _, c := range []struct{ path, body string }{
		{"ui/src/cart.ts", "const url = `${base}/carts/${cart.id}/merge`;\nfetch(url);\n"},
		{"cmd/x/main.go", "s := \"${acme.cron.send-digest}\"\n"},
		{"src/Order.kt", "log.info(\"order ${order.id} created\")\n"},
		{"src/Order.scala", "val s = s\"order ${order.id}\"\n"},
		{"README.md", "Set `${acme.cron.send-digest}` to change the schedule.\n"},
		{"values.yaml", "acme.cron.send-digest: 0 0 * ? * * *\n"},
	} {
		if got := values(Extract(c.path, []byte(c.body)), KindProperty); len(got) != 0 {
			t.Errorf("%s: recorded %v as properties", c.path, got)
		}
	}
}

func TestExtractIgnoresAPlaceholderWithoutADotOrDash(t *testing.T) {
	// Given: "${id}" inside a Java string is far more often a message
	// template than a property; a property key has at least one segment.
	testee := []byte(`String s = "${id}"; String t = "${PORT}";`)

	// When
	got := values(Extract("A.java", testee), KindProperty)

	// Then
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

// Outbound links: the fourth kind. A UI repository rarely writes the URL it
// navigates to as one literal — the host comes from a config service, the path
// is concatenated — so what is recorded is the SITE of the navigation, with
// whatever text stood there as the value. The census in internal/ask lands on
// the line and lets the symbol walk resolve the variable.
func TestExtractFindsAnAbsoluteLinkInMarkup(t *testing.T) {
	// Given
	testee := []byte(`<a class="nav" href="https://portal.example.ch/claims" target="_blank">Portal</a>`)

	// When
	got := Extract("app.component.html", testee)

	// Then
	if !has(values(got, KindLink), "https://portal.example.ch/claims") {
		t.Fatalf("the absolute href was not extracted: %+v", got)
	}
}

func TestExtractFindsNavigationSitesInTypeScript(t *testing.T) {
	// Given: every idiom, one per line, none with a literal host.
	testee := []byte("" +
		"window.open(`${environment.portalUrl}/claims/${id}`, '_blank');\n" +
		"window.location.href = this.config.links.customerApp + '/overview';\n" +
		"location.assign(targetUrl);\n" +
		"this.router.navigateByUrl('/schaden/neu');\n" +
		"this.router.navigate(['/schaden', id]);\n")

	// When
	got := Extract("nav.service.ts", testee)

	// Then: each site is recorded with the text that stood there.
	links := values(got, KindLink)
	for _, want := range []string{
		"${environment.portalUrl}/claims/${id}",
		"this.config.links.customerApp + '/overview'",
		"targetUrl",
		"/schaden/neu",
		"['/schaden', id]",
	} {
		if !has(links, want) {
			t.Errorf("navigation site %q was not extracted, got %+v", want, links)
		}
	}
	if len(got) != 5 {
		t.Errorf("want exactly five link sites, got %+v", got)
	}
}

func TestExtractFindsAngularAndReactLinkAttributes(t *testing.T) {
	// Given
	testee := []byte("" +
		`<a [href]="externalLink">Extern</a>` + "\n" +
		`<a [attr.href]="portal.url">Portal</a>` + "\n" +
		`<a routerLink="/uebersicht">Home</a>` + "\n" +
		`<a [routerLink]="['/schaden', id]">Detail</a>` + "\n" +
		`<Link to="/settings">Settings</Link>` + "\n")

	// When
	got := Extract("nav.component.html", testee)

	// Then
	links := values(got, KindLink)
	for _, want := range []string{"externalLink", "portal.url", "/uebersicht", "['/schaden', id]", "/settings"} {
		if !has(links, want) {
			t.Errorf("attribute link %q was not extracted, got %+v", want, links)
		}
	}
}

func TestExtractSkipsLinksThatLeadNowhere(t *testing.T) {
	// Given: anchors, mail, phone, script, empty, the bare root, stylesheets
	// and the base tag — none is an app someone navigates to. A read of
	// window.location is not a navigation either.
	testee := []byte("" +
		`<a href="#top">Top</a>` + "\n" +
		`<a href="mailto:x@example.ch">Mail</a>` + "\n" +
		`<a href="tel:+41">Call</a>` + "\n" +
		`<a href="javascript:void(0)">Nothing</a>` + "\n" +
		`<a href="">Empty</a>` + "\n" +
		`<a href="/">Root</a>` + "\n" +
		`<link rel="stylesheet" href="/styles.css">` + "\n" +
		`<base href="/">` + "\n" +
		`if (window.location.pathname === '/x') {` + "\n")

	// When
	got := Extract("index.html", testee)

	// Then
	if len(got) != 0 {
		t.Fatalf("want no link tokens, got %+v", got)
	}
}

func TestExtractIgnoresAVariableCalledLocationAndAStylesheetHref(t *testing.T) {
	// Given: React's useLocation in every routed component, a field named
	// location, a stylesheet tag inside a one-line head, a stylesheet
	// injected from script, and a data-href attribute.
	testee := []byte("" +
		`const location = useLocation();` + "\n" +
		`this.location = loc;` + "\n" +
		`const location = window.location;` + "\n" +
		`<head><link rel="stylesheet" href="/s.css"></head>` + "\n" +
		`link.href = "/styles.css";` + "\n" +
		`<div data-href="/x">` + "\n")

	// When
	got := Extract("app.tsx", testee)

	// Then
	if len(got) != 0 {
		t.Fatalf("want no link tokens, got %+v", got)
	}
}

func TestExtractIgnoresSVGSpritesAndAssetPaths(t *testing.T) {
	// Given: the icon sprite on nearly every page, in both spellings, and
	// an href to a downloadable file.
	testee := []byte("" +
		`<use xlink:href="assets/icons.svg#close"></use>` + "\n" +
		`<use href="assets/icons.svg#close"/>` + "\n" +
		`<a href="assets/agb.pdf" download>AGB</a>` + "\n" +
		`<a href="https://portal.example.ch/report.pdf">Report</a>` + "\n")

	// When
	got := values(Extract("icon.component.html", testee), KindLink)

	// Then: the sprite is not a site; a document behind a URL still is.
	if len(got) != 1 || got[0] != "https://portal.example.ch/report.pdf" {
		t.Fatalf("got %v, want the report link alone", got)
	}
}

func TestExtractReadsJSXBraces(t *testing.T) {
	// Given: the three shapes React Router links are written in, plus href.
	testee := []byte("" +
		`<Link to={"/x"}>X</Link>` + "\n" +
		`<Link to={to}>Y</Link>` + "\n" +
		`<Link to={{ pathname: "/z" }}>Z</Link>` + "\n" +
		`<a href={url}>W</a>` + "\n")

	// When
	got := values(Extract("nav.jsx", testee), KindLink)

	// Then
	for _, want := range []string{"/x", "to", `{ pathname: "/z" }`, "url"} {
		if !has(got, want) {
			t.Errorf("JSX link %q was not extracted, got %+v", want, got)
		}
	}
}

func TestExtractDoesNotReadMarkupAsRoutes(t *testing.T) {
	// Given: an .html line that the route rules would take for a client call.
	testee := []byte(`<p>Use fetch to request "/api/orders" from the server.</p>`)

	// When
	got := Extract("help.html", testee)

	// Then: markup gets the link branch only, never the route branch.
	if len(values(got, KindRoute)) != 0 {
		t.Fatalf("a route was extracted from markup: %+v", got)
	}
}

func TestExtractRunsBothBranchesOnTypeScript(t *testing.T) {
	// Given: a client call and a navigation in one file.
	testee := []byte("" +
		`const r = await fetch("/api/claims");` + "\n" +
		`window.open("https://portal.example.ch");` + "\n")

	// When
	got := Extract("claims.ts", testee)

	// Then
	if !has(values(got, KindRoute), "/api/claims") {
		t.Errorf("the route branch did not run on .ts: %+v", got)
	}
	if !has(values(got, KindLink), "https://portal.example.ch") {
		t.Errorf("the link branch did not run on .ts: %+v", got)
	}
}
