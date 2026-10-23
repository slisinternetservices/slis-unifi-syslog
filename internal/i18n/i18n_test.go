package i18n

import "testing"

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"nl-NL": "nl", "en_GB": "en", "NL": "nl", "": "", "x": "", "../etc": ""} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestT(t *testing.T) {
	if got := T("nl-BE", "Device silent: %s", "AP"); got != "Apparaat stil: AP" {
		t.Errorf("got %q", got)
	}
	if got := T("ja", "Device silent: %s", "AP"); got != "Device silent: AP" {
		t.Errorf("fallback: got %q", got)
	}
}
