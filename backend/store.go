package main

import (
	"bufio"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/pbkdf2"
)

// aclMu 串行化 ACL 文件读改写，避免并发注册/追加 MAC 时写坏文件
var aclMu sync.Mutex

// 从passwd文件读取所有用户名
func listUsers() ([]string, error) {
	f, err := os.Open(cfg.HostPasswdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var users []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// mosquitto_passwd 的 base64 哈希会折行：不含 ":" 的行是上一条目的
		// 续行，不是用户名，必须跳过，否则用户列表会出现乱码条目
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) >= 1 && parts[0] != "" {
			users = append(users, parts[0])
		}
	}
	return users, scanner.Err()
}

// 检查用户名是否存在
func userExists(username string) bool {
	users, err := listUsers()
	if err != nil {
		return false
	}
	for _, u := range users {
		if u == username {
			return true
		}
	}
	return false
}

// 从ACL文件解析，检查MAC是否已被使用
func macExists(mac string) bool {
	aclContent, err := os.ReadFile(cfg.ACLPath)
	if err != nil {
		return false
	}

	// 统一去掉分隔符后比较
	clean := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(mac))
	content := strings.ToLower(string(aclContent))
	return strings.Contains(content, clean)
}

// v2SetTopicRule v2 固件(zTC1)通过固定 topic `device/ztc1/set` 接收控制命令
// (固件源码 user_mqtt_client.c 只订阅该 topic, HA 对接的 cmd_t 亦是它),
// 用户 ACL 必须放行, 否则浏览器/HA 的发布会被 mosquitto **静默丢弃**(QoS1 也回 PUBACK)。
// 注意: 该 topic 为全体 v2 设备共享, 授权后任意用户理论上可向其发布带任意 MAC 的命令,
// 属 v2 固件协议固有限制(HA 直连同样如此)。
const v2SetTopicRule = "topic readwrite device/ztc1/set"

// 添加ACL规则
// mac 已由 normalizeMAC 标准化为 AA:BB:CC:DD:EE:FF 格式
func addACLRules(username, mac string) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	return addACLRulesLocked(username, mac)
}

func addACLRulesLocked(username, mac string) error {
	// 4 种 MAC 变体
	macColonUpper := strings.ToUpper(mac)
	macColonLower := strings.ToLower(mac)
	macPlainUpper := strings.ReplaceAll(macColonUpper, ":", "")
	macPlainLower := strings.ReplaceAll(macColonLower, ":", "")

	variants := []string{macColonLower, macColonUpper, macPlainLower, macPlainUpper}

	// 去重（若 pattern 无 %s 则四种结果相同）
	seen := make(map[string]struct{})

	var rules strings.Builder
	rules.WriteString(fmt.Sprintf("\nuser %s\n", username))

	for _, pattern := range cfg.ACLPatterns {
		for _, v := range variants {
			topic := fmt.Sprintf(pattern, v)
			if _, exists := seen[topic]; !exists {
				seen[topic] = struct{}{}
				rules.WriteString(fmt.Sprintf("topic readwrite %s\n", topic))
			}
		}
	}
	// v2 固件的固定命令 topic(见 v2SetTopicRule 注释)
	rules.WriteString(v2SetTopicRule + "\n")

	f, err := os.OpenFile(cfg.ACLPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(rules.String())
	return err
}

// 读取ACL文件内容
func readACL() (string, error) {
	data, err := os.ReadFile(cfg.ACLPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// 写入ACL文件（对外入口，加锁）
func writeACL(content string) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	return writeACLLocked(content)
}

// writeACLLocked 调用方须已持有 aclMu
func writeACLLocked(content string) error {
	return os.WriteFile(cfg.ACLPath, []byte(content), 0644)
}

// 获取用户的ACL规则
func getUserACL(username string) []string {
	data, err := os.ReadFile(cfg.ACLPath)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	inUser := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == fmt.Sprintf("user %s", username) {
			inUser = true
			result = append(result, line)
			continue
		}
		if inUser {
			if strings.HasPrefix(trimmed, "user ") || trimmed == "" {
				if strings.HasPrefix(trimmed, "user ") {
					break
				}
				continue
			}
			result = append(result, line)
		}
	}
	return result
}

// getUserDeviceMACs returns the devices authorized for a specific MQTT user.
// ACL files may contain multiple blocks for the same user and multiple topic
// variants for the same MAC, so results are canonicalized and deduplicated.
func getUserDeviceMACs(username string) ([]string, error) {
	if username == "" {
		return []string{}, nil
	}

	data, err := os.ReadFile(cfg.ACLPath)
	if err != nil {
		return nil, err
	}

	currentUser := ""
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "user" {
			currentUser = fields[1]
			continue
		}
		if currentUser != username || len(fields) < 2 || fields[0] != "topic" {
			continue
		}

		topic := fields[len(fields)-1]
		if mac := deviceMACFromTopic(topic); mac != "" {
			seen[mac] = struct{}{}
		}
	}

	macs := make([]string, 0, len(seen))
	for mac := range seen {
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	return macs, nil
}

func deviceMACFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	var candidate string
	if len(parts) >= 3 && parts[0] == "device" && parts[1] == "ztc1" {
		candidate = parts[2]
	} else if len(parts) >= 3 && parts[0] == "homeassistant" {
		candidate = parts[2]
	} else {
		return ""
	}

	mac := strings.ToLower(strings.NewReplacer(":", "", "-", "").Replace(candidate))
	if len(mac) != 12 {
		return ""
	}
	for _, c := range mac {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return ""
		}
	}
	return mac
}

// 删除用户的ACL规则
func removeUserACL(username string) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	data, err := os.ReadFile(cfg.ACLPath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	inUser := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == fmt.Sprintf("user %s", username) {
			inUser = true
			continue
		}
		if inUser {
			if strings.HasPrefix(trimmed, "user ") {
				inUser = false
				result = append(result, line)
			} else if trimmed == "" {
				// skip empty lines in user block
				continue
			}
			// skip topic lines belonging to this user
			continue
		}
		result = append(result, line)
	}

	return writeACLLocked(strings.Join(result, "\n"))
}

type UserInfo struct {
	Username string   `json:"username"`
	ACLRules []string `json:"acl_rules"`
	Devices  []string `json:"devices"`
}

func getUserInfoList() ([]UserInfo, error) {
	users, err := listUsers()
	if err != nil {
		return nil, err
	}

	var result []UserInfo
	for _, u := range users {
		rules := getUserACL(u)
		devices, err := getUserDeviceMACs(u)
		if err != nil {
			return nil, err
		}
		result = append(result, UserInfo{
			Username: u,
			ACLRules: rules,
			Devices:  devices,
		})
	}
	return result, nil
}

// checkPasswdFile 从 mosquitto passwd 文件读取哈希并验证密码
// mosquitto 2.x 默认使用 sha512-pbkdf2，格式: $7$<iterations>$<b64_salt>$<b64_key>
// 注意：mosquitto_passwd 生成的 base64 会换行，同一条记录可能跨多行
func checkPasswdFile(passwdPath, username, password string) bool {
	data, err := os.ReadFile(passwdPath)
	if err != nil {
		return false
	}

	// mosquitto passwd 中只有含 ":" 的行是新条目起始行，
	// 不含 ":" 的行是上一条目的 base64 续行，需拼回去
	lines := strings.Split(string(data), "\n")
	var entries []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, ":") {
			entries = append(entries, line)
		} else if len(entries) > 0 {
			entries[len(entries)-1] += line
		}
	}

	for _, entry := range entries {
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 || parts[0] != username {
			continue
		}
		return verifyMosquittoHash(password, parts[1])
	}
	return false
}

// verifyMosquittoHash 验证 mosquitto passwd 哈希
// 支持 $7$ (sha512-pbkdf2, mosquitto 2.x 默认格式)
// 格式: $7$<iterations>$<base64_salt>$<base64_key>
func verifyMosquittoHash(password, storedHash string) bool {
	if !strings.HasPrefix(storedHash, "$7$") {
		return false
	}
	// 分割 "" / "7" / iterations / salt_b64 / key_b64
	fields := strings.Split(storedHash, "$")
	if len(fields) != 5 {
		return false
	}
	iterations, err := strconv.Atoi(fields[2])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(fields[3])
	if err != nil {
		return false
	}
	storedKey, err := base64.StdEncoding.DecodeString(fields[4])
	if err != nil {
		return false
	}
	dk := pbkdf2.Key([]byte(password), salt, iterations, len(storedKey), sha512.New)
	return bytes.Equal(dk, storedKey)
}

// appendACLRules 为已有用户在 ACL 文件中追加规则（不重复添加 user 行）
func appendACLRules(username, mac string) error {
	aclMu.Lock()
	defer aclMu.Unlock()
	macColonUpper := strings.ToUpper(mac)
	macColonLower := strings.ToLower(mac)
	macPlainUpper := strings.ReplaceAll(macColonUpper, ":", "")
	macPlainLower := strings.ReplaceAll(macColonLower, ":", "")
	variants := []string{macColonLower, macColonUpper, macPlainLower, macPlainUpper}

	seen := make(map[string]struct{})
	var rules strings.Builder
	for _, pattern := range cfg.ACLPatterns {
		for _, v := range variants {
			topic := fmt.Sprintf(pattern, v)
			if _, exists := seen[topic]; !exists {
				seen[topic] = struct{}{}
				rules.WriteString(fmt.Sprintf("topic readwrite %s\n", topic))
			}
		}
	}

	// 找到用户块末尾，插入新规则；若用户块不存在则追加
	data, err := os.ReadFile(cfg.ACLPath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")

	// 生成待插入的 topic 规则（已按 MAC 变体去重），并确保 v2 固件的固定命令 topic 在列
	var newTopics []string
	for _, topic := range strings.Split(rules.String(), "\n") {
		topic = strings.TrimSpace(topic)
		if topic != "" {
			newTopics = append(newTopics, topic)
		}
	}
	newTopics = append(newTopics, v2SetTopicRule)

	// 查找该用户最后一个块
	userLine := fmt.Sprintf("user %s", username)
	lastUserBlock := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == userLine {
			lastUserBlock = i
		}
	}

	// 若无用户块，则在文件末尾全新追加（须包含 v2 固定命令 topic，见 v2SetTopicRule）
	if lastUserBlock < 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("\nuser %s\n", username))
		b.WriteString(rules.String())
		// rules 可能已以换行结尾；保证 v2 规则独占一行
		if !strings.HasSuffix(rules.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(v2SetTopicRule + "\n")
		return writeACLLocked(string(data) + b.String())
	}

	// 在用户块内追加：找到块的结束位置（下一个 user 前的最后一个 topic 行）
	insertAt := lastUserBlock + 1
	for insertAt < len(lines) {
		trimmed := strings.TrimSpace(lines[insertAt])
		if trimmed == "" || strings.HasPrefix(trimmed, "user ") {
			break
		}
		insertAt++
	}

	// 去重：跳过已存在的 topic 规则，避免重复
	var toInsert []string
	for _, nt := range newTopics {
		dup := false
		for i := lastUserBlock + 1; i < insertAt; i++ {
			if strings.TrimSpace(lines[i]) == nt {
				dup = true
				break
			}
		}
		if !dup {
			toInsert = append(toInsert, nt)
		}
	}
	if len(toInsert) == 0 {
		return nil
	}

	// 插入新规则
	result := make([]string, 0, len(lines)+len(toInsert))
	result = append(result, lines[:insertAt]...)
	result = append(result, toInsert...)
	result = append(result, lines[insertAt:]...)

	return writeACLLocked(strings.Join(result, "\n"))
}
