package main

import (
	"encoding/json"
	"net/http"
)

// handleTOTPSetup 生成新的 TOTP 密钥（仅返回，验证首次动态码后才保存）
// POST /api/admin/totp/setup
func handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	if cfg.TOTPSecret != "" {
		jsonError(w, "两步验证已开启，请先关闭后再重新绑定", http.StatusConflict)
		return
	}

	secret, err := generateTOTPSecret()
	if err != nil {
		jsonError(w, "生成密钥失败", http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{
		"secret":      secret,
		"otpauth_uri": totpURI(secret, cfg.AdminUsername),
	})
}

// handleTOTPEnable 校验首次动态码并保存密钥（开启两步验证）
// POST /api/admin/totp/enable  { "secret": "...", "code": "123456" }
func handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	if cfg.TOTPSecret != "" {
		jsonError(w, "两步验证已开启", http.StatusConflict)
		return
	}

	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}

	if !verifyTOTP(req.Secret, req.Code) {
		jsonError(w, "动态验证码错误，请确认手机 Authenticator 后重试", http.StatusUnauthorized)
		return
	}

	cfg.TOTPSecret = req.Secret
	if err := persistConfig(); err != nil {
		cfg.TOTPSecret = ""
		jsonError(w, "保存配置失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{"message": "两步验证已开启"})
}

// handleTOTPDisable 验证当前动态码后关闭两步验证
// POST /api/admin/totp/disable  { "code": "123456" }
func handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}

	if cfg.TOTPSecret == "" {
		jsonError(w, "两步验证未开启", http.StatusConflict)
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "请求格式错误", http.StatusBadRequest)
		return
	}

	if !verifyTOTP(cfg.TOTPSecret, req.Code) {
		jsonError(w, "动态验证码错误", http.StatusUnauthorized)
		return
	}

	cfg.TOTPSecret = ""
	if err := persistConfig(); err != nil {
		jsonError(w, "保存配置失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{"message": "两步验证已关闭"})
}
