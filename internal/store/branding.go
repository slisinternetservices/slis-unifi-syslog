package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// Branding is the company identity shown in the menu, on the sign-in page and
// in e-mails. The images themselves are stored as separate settings so the
// small JSON document stays cheap to read on every request.
type Branding struct {
	CompanyName string `json:"company_name"`
	// Version changes whenever an image changes; clients use it to bust caches.
	Version string          `json:"version"`
	Assets  map[string]bool `json:"assets"` // which of BrandingAssets are uploaded
}

// BrandingAsset is an uploaded image.
type BrandingAsset struct {
	Hash string `json:"hash"` // first field: Branding() reads it from the JSON prefix
	Type string `json:"type"` // MIME type
	Data []byte `json:"data"`
}

// BrandingAssets are the image slots: logo for light and dark mode, favicon.
var BrandingAssets = []string{"logo_light", "logo_dark", "favicon"}

const MaxBrandingAsset = 512 << 10

func validAsset(name string) bool {
	for _, a := range BrandingAssets {
		if a == name {
			return true
		}
	}
	return false
}

func (m *Meta) Branding() Branding {
	b := Branding{CompanyName: "SLIS Internet Services"}
	m.GetSetting("branding", &b)
	b.Assets = map[string]bool{}
	h := sha256.New()
	for _, a := range BrandingAssets {
		var hash string
		if m.DB.QueryRow(`SELECT substr(value, 1, 64) FROM settings WHERE key=?`, "branding."+a).Scan(&hash) == nil {
			b.Assets[a] = true
			h.Write([]byte(a + hash))
		}
	}
	if len(b.Assets) > 0 {
		b.Version = hex.EncodeToString(h.Sum(nil))[:12]
	}
	return b
}

func (m *Meta) SaveBranding(b Branding) error {
	return m.SetSetting("branding", Branding{CompanyName: strings.TrimSpace(b.CompanyName)})
}

func (m *Meta) BrandingAsset(name string) (*BrandingAsset, error) {
	if !validAsset(name) {
		return nil, ErrNotFound
	}
	var a BrandingAsset
	if err := m.GetSetting("branding."+name, &a); err != nil || len(a.Data) == 0 {
		return nil, ErrNotFound
	}
	return &a, nil
}

// SaveBrandingAsset validates and stores an image. PNG and SVG are accepted
// for every slot, ICO only for the favicon.
func (m *Meta) SaveBrandingAsset(name string, data []byte) (string, error) {
	if !validAsset(name) {
		return "", ErrNotFound
	}
	if len(data) == 0 {
		return "", errors.New("the file is empty")
	}
	if len(data) > MaxBrandingAsset {
		return "", errors.New("the image may be at most 512 KB")
	}
	typ := sniffImage(data)
	if typ == "" || (typ == "image/x-icon" && name != "favicon") {
		return "", errors.New("unsupported image: use PNG or SVG")
	}
	sum := sha256.Sum256(data)
	return typ, m.SetSetting("branding."+name, BrandingAsset{Hash: hex.EncodeToString(sum[:8]), Type: typ, Data: data})
}

func (m *Meta) DeleteBrandingAsset(name string) error {
	if !validAsset(name) {
		return ErrNotFound
	}
	_, err := m.DB.Exec(`DELETE FROM settings WHERE key=?`, "branding."+name)
	return err
}

func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return "image/x-icon"
	}
	head := strings.ToLower(string(b[:min(len(b), 1024)]))
	head = strings.TrimPrefix(strings.TrimSpace(head), "\ufeff")
	if (strings.HasPrefix(head, "<?xml") || strings.HasPrefix(head, "<svg") || strings.HasPrefix(head, "<!--")) && strings.Contains(strings.ToLower(string(b)), "<svg") {
		return "image/svg+xml"
	}
	return ""
}
