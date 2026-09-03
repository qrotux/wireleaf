package include

import (
	"net/http"
	"net/url"
	"testing"
)

func TestCtxRequestAccessors(t *testing.T) {
	var c Ctx
	if c.Header("X-A") != "" || c.PathParam("id") != "" || c.QueryValue("q") != "" {
		t.Fatal("nil Request must read as empty")
	}
	c.Request = &Request{
		PathParams: map[string]string{"id": "b1"},
		Query:      url.Values{"q": {"hobbit"}},
		Header:     http.Header{"X-Remote-Addr": {"10.0.0.1"}},
	}
	if got := c.Header("x-remote-addr"); got != "10.0.0.1" {
		t.Errorf("Header = %q", got)
	}
	if got := c.PathParam("id"); got != "b1" {
		t.Errorf("PathParam = %q", got)
	}
	if got := c.QueryValue("q"); got != "hobbit" {
		t.Errorf("QueryValue = %q", got)
	}
}
