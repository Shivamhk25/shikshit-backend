package identity

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Password length limits. Length is what makes a password strong; composition rules ("one digit, one symbol") are not
// used because they push people to predictable patterns (NIST SP 800-63B §5.1.1). Per-institute policy is IAM-10.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 128
)

// ErrWeakPassword is returned by CheckPassword; its message is safe to show to the user.
var ErrWeakPassword = errors.New("Choose a password of 10 to 128 characters that is not your username.")

// CheckPassword reports whether password may be set for the user with this username.
func CheckPassword(password, username string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength || n > MaxPasswordLength {
		return ErrWeakPassword
	}
	if username != "" && strings.EqualFold(strings.TrimSpace(password), strings.TrimSpace(username)) {
		return ErrWeakPassword
	}
	return nil
}
