package mail

import (
	"reflect"
	"strings"
	"testing"
)

func TestHTMLBody(t *testing.T) {
	h := htmlBody(Brand{Instance: "Syslog", Company: "Contoso <B.V.>", Logo: []byte{1}, LogoOnLight: true}, "nl", "Onderwerp", "regel")
	for _, want := range []string{`lang="nl"`, "Contoso &lt;B.V.&gt;", `src="cid:logo.png"`, "background:#fff", "Automatisch verzonden door Syslog · Contoso"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q", want)
		}
	}
	if h := htmlBody(Brand{Instance: "Syslog"}, "en", "s", "b"); strings.Contains(h, "cid:") || !strings.Contains(h, "Sent automatically by Syslog") {
		t.Errorf("plain: %s", h)
	}
}

func TestRecipients(t *testing.T) {
	if got := Recipients([]string{"a@x.nl, b@x.nl;c@x.nl"}, "d@x.nl"); !reflect.DeepEqual(got, []string{"a@x.nl", "b@x.nl", "c@x.nl"}) {
		t.Errorf("got %v", got)
	}
	if got := Recipients([]string{""}, "d@x.nl e@x.nl"); !reflect.DeepEqual(got, []string{"d@x.nl", "e@x.nl"}) {
		t.Errorf("default: %v", got)
	}
}
