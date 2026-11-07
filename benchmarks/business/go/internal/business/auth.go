package business

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	maximumTokenBytes = 256
	bearerPrefix      = "Bearer "
	tokenPartCount    = 4
)

func authenticate(header, secret string, now time.Time, limits Dataset) (Principal, error) {
	var principal Principal
	if len(header) <= len(bearerPrefix) || len(header) > maximumTokenBytes ||
		!strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return principal, unauthorized()
	}
	parts := strings.Split(header[len(bearerPrefix):], ".")
	if len(parts) != tokenPartCount || len(parts[3]) != sha256.Size*2 {
		return principal, unauthorized()
	}
	claims := make([]int64, tokenPartCount-1)
	for index := range claims {
		value, err := strconv.ParseInt(parts[index], 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != parts[index] {
			return principal, unauthorized()
		}
		claims[index] = value
	}
	if claims[0] > limits.TenantCount || claims[2] <= now.Unix() ||
		(claims[1]-1)/limits.UsersPerTenant+1 != claims[0] {
		return principal, unauthorized()
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join(parts[:tokenPartCount-1], ".")))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[tokenPartCount-1])) {
		return principal, unauthorized()
	}
	return Principal{TenantID: claims[0], UserID: claims[1]}, nil
}
