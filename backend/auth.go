package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// isPrivateIP 判断 IP 是否属于内网（RFC 1918 + loopback）
func isPrivateIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil {
		return false
	}
	privateRanges := []struct{ start, end net.IP }{
		{net.ParseIP("10.0.0.0"), net.ParseIP("10.255.255.255")},
		{net.ParseIP("172.16.0.0"), net.ParseIP("172.31.255.255")},
		{net.ParseIP("192.168.0.0"), net.ParseIP("192.168.255.255")},
		{net.ParseIP("127.0.0.0"), net.ParseIP("127.255.255.255")},
	}
	for _, r := range privateRanges {
		if bytes.Compare(ip, r.start.To4()) >= 0 && bytes.Compare(ip, r.end.To4()) <= 0 {
			return true
		}
	}
	return false
}

// getClientIP 获取客户端真实 IP。
// 优先使用 nginx 以覆盖方式设置的 X-Real-IP（$remote_addr，不可被客户端伪造）；
// 不能信任 X-Forwarded-For 首值：nginx 使用 $proxy_add_x_forwarded_for（追加语义），
// 客户端自带的伪造值会排在最前。
func getClientIP(r *http.Request) net.IP {
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if parsed := net.ParseIP(xri); parsed != nil {
			return parsed
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return net.ParseIP(host)
}

type AdminLoginReq struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	TOTPCode    string `json:"totp_code"`
	SessionDays int    `json:"session_days"`
}

type AdminLoginResp struct {
	Token        string        `json:"token"`
	MQTTUsername string        `json:"mqtt_username"`
	MQTTPassword string        `json:"mqtt_password"`
	MQTTWSURI    string        `json:"mqtt_ws_uri"`
	Devices      []AdminDevice `json:"devices"`
}

type MQTTLoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type MQTTLoginResp struct {
	Username string   `json:"username"`
	Devices  []string `json:"devices"`
}

type TokenPayload struct {
	Username string `json:"username"`
	Exp      int64  `json:"exp"`
}

// generateToken 签发管理员会话 token，有效期由登录时选择（白名单校验，天）
func generateToken(username string, sessionDays int) string {
	payload := TokenPayload{
		Username: username,
		Exp:      time.Now().Add(time.Duration(sessionDays) * 24 * time.Hour).Unix(),
	}
	data, _ := json.Marshal(payload)
	encoded := hex.EncodeToString(data)

	mac := hmac.New(sha256.New, []byte(cfg.AdminPassword))
	mac.Write(data)
	sig := hex.EncodeToString(mac.Sum(nil))

	return encoded + "." + sig
}

func validateToken(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}

	data, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(cfg.AdminPassword))
	mac.Write(data)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedSig)) {
		return false
	}

	var payload TokenPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	return payload.Username == cfg.AdminUsername && time.Now().Unix() < payload.Exp
}

func adminSessionResp(token string) (AdminLoginResp, error) {
	macs, err := getUserDeviceMACs(cfg.AdminUsername)
	if err != nil {
		return AdminLoginResp{}, err
	}
	devices, err := getAdminDevicesWithMetadata(macs)
	if err != nil {
		return AdminLoginResp{}, err
	}

	return AdminLoginResp{
		Token:        token,
		MQTTUsername: cfg.AdminUsername,
		MQTTPassword: cfg.AdminPassword,
		MQTTWSURI:    cfg.MQTTWSUri,
		Devices:      devices,
	}, nil
}

func adminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			jsonError(w, "未授权", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if !validateToken(token) {
			jsonError(w, "令牌无效或已过期", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	var req AdminLoginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}

	// 登录有效期白名单（天），缺失或非法值回落默认 7 天
	switch req.SessionDays {
	case 1, 7, 30, 180, 365:
	default:
		req.SessionDays = 7
	}

	if req.Username != cfg.AdminUsername || req.Password != cfg.AdminPassword {
		jsonError(w, "用户名或密码错误", http.StatusUnauthorized)
		return
	}

	// 两步验证：密钥已配置则必须携带有效动态码（防暴力，见 nginx 限速）
	// 内网跳过：skip_totp_on_intranet=true 且请求来自内网时免验证
	if cfg.TOTPSecret != "" {
		skip := false
		if cfg.SkipTOTPOnIntranet {
			clientIP := getClientIP(r)
			skip = isPrivateIP(clientIP)
			if skip {
				log.Printf("[AUTH] 内网跳过 2FA: IP=%s", clientIP)
			}
		}
		if !skip && !verifyTOTP(cfg.TOTPSecret, req.TOTPCode) {
			jsonError(w, "动态验证码错误或已过期", http.StatusUnauthorized)
			return
		}
	}

	resp, err := adminSessionResp(generateToken(req.Username, req.SessionDays))
	if err != nil {
		jsonError(w, "读取管理员关联设备失败", http.StatusInternalServerError)
		return
	}

	jsonResp(w, resp)
}

func handleAdminSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	resp, err := adminSessionResp(token)
	if err != nil {
		jsonError(w, "读取管理员关联设备失败", http.StatusInternalServerError)
		return
	}

	jsonResp(w, resp)
}

func handleMQTTLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	var req MQTTLoginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}

	// 访客页面关闭时，普通 MQTT 用户无法通过前端登录；
	// 管理员控制台同样走此接口校验，必须放行管理员（仍需密码验证）
	if !guestPageEnabled() && req.Username != cfg.AdminUsername {
		jsonError(w, "访客功能已关闭，请使用管理员账号登录", http.StatusForbidden)
		return
	}

	if !checkPasswdFile(cfg.HostPasswdPath, req.Username, req.Password) {
		jsonError(w, "MQTT 用户名或密码错误", http.StatusUnauthorized)
		return
	}

	devices, err := getUserDeviceMACs(req.Username)
	if err != nil {
		jsonError(w, "读取账号关联设备失败", http.StatusInternalServerError)
		return
	}

	jsonResp(w, MQTTLoginResp{Username: req.Username, Devices: devices})
}
