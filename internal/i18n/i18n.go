// Package i18n translates server-generated text such as e-mails. Like the web
// interface, keys are the English source strings (here fmt format strings),
// so English needs no entries. Every locales/<code>.json file is embedded and
// picked up automatically.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

//go:embed locales/*.json
var files embed.FS

var dicts = map[string]map[string]string{}

func init() {
	entries, _ := files.ReadDir("locales")
	for _, e := range entries {
		b, err := files.ReadFile("locales/" + e.Name())
		if err != nil {
			continue
		}
		d := map[string]string{}
		if err := json.Unmarshal(b, &d); err != nil {
			panic("i18n: " + e.Name() + ": " + err.Error())
		}
		dicts[strings.TrimSuffix(e.Name(), path.Ext(e.Name()))] = d
	}
}

var tagRe = regexp.MustCompile(`^[a-z]{2,3}$`)

// Normalize turns a language tag such as "nl-NL" or "en_GB" into the base
// code used for settings ("nl", "en"); invalid input gives "".
func Normalize(tag string) string {
	base, _, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"), "-")
	base = strings.ToLower(base)
	if !tagRe.MatchString(base) {
		return ""
	}
	return base
}

// Has reports whether server-side translations exist for lang.
func Has(lang string) bool { _, ok := dicts[lang]; return ok || lang == "en" }

// T translates key into lang and formats it with args like fmt.Sprintf.
func T(lang, key string, args ...any) string {
	s := key
	if v := dicts[Normalize(lang)][key]; v != "" {
		s = v
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Translator binds T to one language.
type Translator func(key string, args ...any) string

func For(lang string) Translator {
	return func(key string, args ...any) string { return T(lang, key, args...) }
}
