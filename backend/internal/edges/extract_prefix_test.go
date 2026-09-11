package edges

import "testing"

// TestExtractComposesAClassLevelPrefixWithTheMethodPath: Spring writes the
// served path in two places. The bare method path is still recorded (the
// flow corpus's clients spell it that way), and so is the path the server
// actually serves, which is what a client spelling the whole path matches.
func TestExtractComposesAClassLevelPrefixWithTheMethodPath(t *testing.T) {
	testee := []byte(`
@RestController
@RequestMapping("/api")
public class ControllerSchadenfall {

    @GetMapping(path = "/schadenfall", produces = MediaType.APPLICATION_JSON_VALUE)
    public Schadenfall get() {}

    @PostMapping(value = "/schadenfall/{uuid}/abschluss")
    public void close() {}
}
`)
	got := values(Extract("ControllerSchadenfall.java", testee), KindRoute)
	for _, want := range []string{"/schadenfall", "/api/schadenfall", "/schadenfall/{uuid}/abschluss", "/api/schadenfall/{uuid}/abschluss"} {
		if !has(got, want) {
			t.Errorf("routes = %v, missing %s", got, want)
		}
	}
	if has(got, "/api") {
		t.Errorf("the bare prefix is a convention every controller shares and must not be a token: %v", got)
	}

	// A class-level path that is NOT a convention stays a route of its own:
	// the front-end calls "/carts" as such.
	carts := values(Extract("CartsController.java", []byte(`
@RestController
@RequestMapping(path = "/carts")
public class CartsController {
    @RequestMapping(value = "/{customerId}/merge", method = RequestMethod.GET)
    public void merge() {}
}
`)), KindRoute)
	for _, want := range []string{"/carts", "/{customerId}/merge", "/carts/{customerId}/merge"} {
		if !has(carts, want) {
			t.Errorf("carts routes = %v, missing %s", carts, want)
		}
	}
}

// TestExtractReadsTheRouteOutOfAGeneratedClientTemplate: an OpenAPI-generated
// Angular client writes `${this.configuration.basePath}/beruf`. The base is
// configuration; the part after it is the route the server declares, and it
// is the only place the UI's route vocabulary exists at all.
func TestExtractReadsTheRouteOutOfAGeneratedClientTemplate(t *testing.T) {
	testee := []byte(`
    return this.httpClient.request<Beruf[]>('get', ` + "`${this.configuration.basePath}/beruf`" + `, options);
    return this.httpClient.request<Betrieb>('get', ` + "`${this.configuration.basePath}/betrieb/${encodeURIComponent(String(partnerid))}/uka`" + `, options);
`)
	got := values(Extract("beruf.service.ts", testee), KindRoute)
	if !has(got, "/beruf") {
		t.Errorf("routes = %v, want /beruf read out of the template", got)
	}
	if !has(got, "/betrieb") {
		t.Errorf("routes = %v, want the constant prefix /betrieb before the first interpolation", got)
	}
	for _, v := range got {
		if len(v) > 0 && (v[0] != '/' || v == "/") {
			t.Errorf("a malformed route got through: %q", v)
		}
	}
}

// TestExtractScopesTheClassLevelPrefixToItsOwnClass: a second controller in
// the same file starts without the first one's prefix, and a comment that
// says "class" after a method-level mapping does not make it class-level.
func TestExtractScopesTheClassLevelPrefixToItsOwnClass(t *testing.T) {
	testee := []byte(`
@RestController
@RequestMapping("/carts")
public class CartsController {
    @GetMapping("/{id}")
    public void get() {}
}

@RestController
public class OrdersController {
    @RequestMapping("/orders")
    // the class above serves carts; this one is a method mapping
    public void list() {}
}
`)
	got := values(Extract("Controllers.java", testee), KindRoute)
	for _, want := range []string{"/carts", "/carts/{id}", "/orders"} {
		if !has(got, want) {
			t.Errorf("routes = %v, missing %s", got, want)
		}
	}
	if has(got, "/carts/orders") {
		t.Errorf("the first class's prefix leaked into the second: %v", got)
	}
}

// TestExtractKeepsTheClassLevelPrefixAcrossANestedClass: a static DTO class
// inside the controller and a code line whose literal mentions a class are
// both indented; neither ends the controller's prefix.
func TestExtractKeepsTheClassLevelPrefixAcrossANestedClass(t *testing.T) {
	testee := []byte(`
@RestController
@RequestMapping("/carts")
public class CartController {
    public static class Dto {}

    @GetMapping("/{id}")
    public Cart get() {
        log.info("resolved class Handler");
        return null;
    }

    @PostMapping("/{id}/items")
    public void add() {}
}
`)
	got := values(Extract("CartController.java", testee), KindRoute)
	for _, want := range []string{"/carts", "/carts/{id}", "/carts/{id}/items"} {
		if !has(got, want) {
			t.Errorf("routes = %v, missing %s", got, want)
		}
	}
}
