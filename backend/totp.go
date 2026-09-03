package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const totpStep = 30

// generateTOTPSecret 生成 160 位随机密钥（base32 无填充），供 Authenticator 绑定
func generateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// totpCode 计算指定时间步的 6 位动态码
func totpCode(secret string, step int64) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil {
		return "", err
	}
	if len(key) < 10 {
		return "", fmt.Errorf("密钥长度不足")
	}

	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// verifyTOTP 校验动态码，允许前后各一个 30 秒时间窗的时钟偏差，恒定时间比较
func verifyTOTP(secret, code string) bool {
	code = strings.TrimSpace(code)
	if secret == "" || len(code) != 6 {
		return false
	}
	now := time.Now().Unix() / totpStep
	for _, step := range []int64{now - 1, now, now + 1} {
		expect, err := totpCode(secret, step)
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(expect), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// totpURI 生成 otpauth:// 链接，Authenticator 扫码即自动添加账号
func totpURI(secret, username string) string {
	label := url.PathEscape("zTC1:" + username)
	params := url.Values{}
	params.Set("secret", secret)
	params.Set("issuer", "zTC1")
	params.Set("algorithm", "SHA1")
	params.Set("digits", "6")
	params.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s?%s", label, params.Encode())
}
