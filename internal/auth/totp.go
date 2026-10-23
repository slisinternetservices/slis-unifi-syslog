package auth

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"image/png"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTPEnrollment is a new, not yet confirmed authenticator secret.
type TOTPEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
	QR     string `json:"qr"` // PNG as a data: URL
}

// NewTOTP creates a secret for an authenticator app (RFC 6238, SHA-1,
// 6 digits, 30 s: what every common app supports).
func NewTOTP(issuer, account string) (*TOTPEnrollment, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return nil, err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &TOTPEnrollment{Secret: key.Secret(), URI: key.URL(), QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

const totpPeriod = 30

// CheckTOTP validates a code with one step of clock drift either way and
// returns the matched time step, so callers can refuse a replay.
func CheckTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / totpPeriod
	for _, d := range []int64{0, -1, 1} {
		s := step + d
		want, err := totp.GenerateCodeCustom(secret, time.Unix(s*totpPeriod, 0), totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}
