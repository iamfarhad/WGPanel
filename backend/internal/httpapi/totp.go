package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; HMAC-SHA1 is what authenticator apps universally support
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod = 30
	totpDigits = 6
	// recoveryCodeCount codes are issued at enrolment and on regeneration.
	recoveryCodeCount = 10
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

func totpCodeAt(secret string, step int64) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1000000), nil
}

// verifyTOTP checks code against the current step +-1 (clock skew) and returns the
// matching step so the caller can reject replays. Constant-time compare.
func verifyTOTP(secret, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	matched := int64(0)
	ok := false
	for _, d := range []int64{0, -1, 1} {
		want, err := totpCodeAt(secret, cur+d)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && !ok {
			matched, ok = cur+d, true
		}
	}
	return matched, ok
}

func totpURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// newRecoveryCodes returns human-typeable codes ("xxxxx-xxxxx", 50 random bits of
// lowercase base32) and their storage hashes.
func newRecoveryCodes() (codes, hashes []string, err error) {
	for i := 0; i < recoveryCodeCount; i++ {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		s := strings.ToLower(b32.EncodeToString(b))[:10]
		code := s[:5] + "-" + s[5:]
		codes = append(codes, code)
		hashes = append(hashes, hashRecoveryCode(code))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// hashRecoveryCode hashes the normalised code; with 50+ bits of entropy a plain
// SHA-256 is sufficient (there is nothing to brute-force offline in a feasible time
// per code, and online guesses are rate-limited by the login lockout).
func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}
