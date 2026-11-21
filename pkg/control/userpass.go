package control

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// A "userpass" tenant credential keeps its username and password as one stored value, so the
// secret store (which holds a string) and every reader see them together. The prefix is reserved:
// a credential of another kind may not start with it, so a stored value is unambiguous.
const userPassPrefix = "userpass:"

// IsReservedCredentialValue reports whether value could be mistaken for an encoded userpass.
func IsReservedCredentialValue(value string) bool { return strings.HasPrefix(value, userPassPrefix) }

// EncodeUserPass is the stored form of a username and password.
func EncodeUserPass(username, password string) string {
	raw, _ := json.Marshal(map[string]string{"username": username, "password": password})
	return userPassPrefix + string(raw)
}

// DecodeUserPass reads the stored form back; ok is false when value is not one.
func DecodeUserPass(value string) (username, password string, ok bool) {
	rest, found := strings.CutPrefix(value, userPassPrefix)
	if !found {
		return "", "", false
	}
	var parts struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.Unmarshal([]byte(rest), &parts) != nil {
		return "", "", false
	}
	return parts.Username, parts.Password, true
}

// BasicAuthorization is the HTTP Authorization header value for a username and password.
func BasicAuthorization(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

// AuthorizationFromCredential is the Authorization header value a stored credential stands for:
// Basic for a userpass, the value itself otherwise.
func AuthorizationFromCredential(value string) string {
	if username, password, ok := DecodeUserPass(value); ok {
		return BasicAuthorization(username, password)
	}
	return value
}
