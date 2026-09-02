package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type AdminLoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
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

func generateToken(username string) string {
	payload := TokenPayload{
		Username: username,
		Exp:      time.Now().Add(24 * time.Hour).Unix(),
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

	if req.Username != cfg.AdminUsername || req.Password != cfg.AdminPassword {
		jsonError(w, "用户名或密码错误", http.StatusUnauthorized)
		return
	}

	resp, err := adminSessionResp(generateToken(req.Username))
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
