package main

import (
	"log"
	"net/http"
	"os"

	"gopkg.in/yaml.v3"
)

type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
	TLS      bool   `yaml:"tls"`
}

type Config struct {
	ContainerName       string     `yaml:"container_name"`
	ContainerPasswdPath string     `yaml:"container_passwd_path"`
	HostPasswdPath      string     `yaml:"host_passwd_path"`
	ACLPath             string     `yaml:"acl_path"`
	DeviceMetadataPath  string     `yaml:"device_metadata_path"`
	DeviceConfigPath    string     `yaml:"device_config_path"`
	PowerDBPath         string     `yaml:"power_db_path"`
	PowerCollectEnabled bool       `yaml:"power_collect_enabled"`
	PowerMQTTBroker     string     `yaml:"power_mqtt_broker"`
	ACLPatterns         []string   `yaml:"acl_patterns"`
	AdminUsername       string     `yaml:"admin_username"`
	AdminPassword       string     `yaml:"admin_password"`
	TOTPSecret          string     `yaml:"totp_secret"`
	SkipTOTPOnIntranet  bool       `yaml:"skip_totp_on_intranet"`
	RegisterLevel       int        `yaml:"register_level"`
	GuestPageEnabled    *bool      `yaml:"guest_page_enabled"`
	ContactInfo         string     `yaml:"contact_info"`
	MQTTUri             string     `yaml:"mqtt_uri"`
	MQTTWSUri           string     `yaml:"mqtt_ws_uri"`
	SMTP                SMTPConfig `yaml:"smtp"`
	// Webhook：插口状态翻转时向外部机器人推送（纯出站 HTTP，不影响排插）
	// ThrottleSeconds nil=默认2s；0=关闭节流；>0=窗口内合并
	WebhookEnabled         bool   `yaml:"webhook_enabled"`
	WebhookURL             string `yaml:"webhook_url"`
	WebhookHeaders         string `yaml:"webhook_headers"`
	WebhookBody            string `yaml:"webhook_body"`
	WebhookRetry           int    `yaml:"webhook_retry"`
	WebhookThrottleSeconds *int   `yaml:"webhook_throttle_seconds"`
	ListenAddr             string `yaml:"listen_addr"`
}

// webhookThrottleSec 合并窗口（秒）：未配置默认 2，显式 0 表示关闭节流
func webhookThrottleSec() int {
	if cfg.WebhookThrottleSeconds == nil {
		return 2
	}
	n := *cfg.WebhookThrottleSeconds
	if n < 0 {
		return 0
	}
	if n > 60 {
		return 60
	}
	return n
}

var cfg Config

var configPath string

// guestPageEnabled 访客页面开关（MQTT 用户控制台）。
// 配置未设置时默认开启，兼容旧版 config.yaml。
func guestPageEnabled() bool {
	return cfg.GuestPageEnabled == nil || *cfg.GuestPageEnabled
}

func safeReturnTo(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] != '/' {
		return ""
	}
	if len(raw) > 1 && raw[1] == '/' {
		return ""
	}
	return raw
}

func loadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, &cfg)
}

// persistConfig 将当前运行时配置写回 config.yaml（用于可以被管理员修改的字段）
func persistConfig() error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// syncAdminToMQTT 启动时自动将管理员账号同步到 mosquitto。
// 无论用户是否已存在都执行 mosquitto_passwd -b（对已存在用户为覆盖语义），
// 这样在 config.yaml 改了 admin_password 后重启即可让 passwd 哈希与配置一致，
// 避免"Web 登录用新密码成功、但浏览器拿新密码连 MQTT 一直失败"的问题。
func syncAdminToMQTT() error {
	if cfg.AdminUsername == "" || cfg.AdminPassword == "" {
		log.Println("警告: admin_username 或 admin_password 为空，跳过同步")
		return nil
	}

	existed := userExists(cfg.AdminUsername)
	if existed {
		log.Printf("正在更新管理员账号 %s 的 MQTT 密码（与 config.yaml 对齐）...", cfg.AdminUsername)
	} else {
		log.Printf("正在同步管理员账号 %s 到MQTT服务...", cfg.AdminUsername)
	}

	// 创建/更新管理员MQTT用户
	if err := createMosquittoUser(cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return err
	}

	// 重载mosquitto
	if err := reloadMosquitto(); err != nil {
		return err
	}

	if existed {
		log.Printf("管理员账号 %s 密码已同步", cfg.AdminUsername)
	} else {
		log.Printf("管理员账号 %s 同步成功", cfg.AdminUsername)
	}
	return nil
}

func main() {
	configPath = "config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	if err := loadConfig(configPath); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if cfg.DeviceMetadataPath == "" {
		cfg.DeviceMetadataPath = "/data/metadata/device_metadata.json"
	}
	if cfg.DeviceConfigPath == "" {
		cfg.DeviceConfigPath = "/data/metadata/device_config.json"
	}
	if cfg.PowerDBPath == "" {
		cfg.PowerDBPath = "/data/metadata/power_stats.db"
	}
	if cfg.PowerMQTTBroker == "" {
		cfg.PowerMQTTBroker = "tcp://mosquitto:1883"
	}
	if cfg.PowerCollectEnabled {
		var err error
		powerStore, err = NewPowerStore(cfg.PowerDBPath)
		if err != nil {
			log.Printf("警告: 初始化功耗存储失败: %v", err)
		} else {
			broker := cfg.PowerMQTTBroker
			collector := NewPowerCollector(powerStore)
			if err := collector.Start(broker, cfg.AdminUsername, cfg.AdminPassword); err != nil {
				log.Printf("警告: 功耗采集器启动失败: %v", err)
			}
			// V2 设备定时任务调度器(方案A): 与功耗存储共用 fires 表, 独立 MQTT 连接
			scheduler := NewTaskScheduler(powerStore)
			if err := scheduler.Start(broker, cfg.AdminUsername, cfg.AdminPassword); err != nil {
				log.Printf("警告: 定时调度器启动失败: %v", err)
			}
		}
	}

	// 从环境变量加载 MQTT URI（优先级高于配置文件）
	if mqttUri := os.Getenv("MQTT_URI"); mqttUri != "" {
		cfg.MQTTUri = mqttUri
	}
	if mqttWsUri := os.Getenv("MQTT_WS_URI"); mqttWsUri != "" {
		cfg.MQTTWSUri = mqttWsUri
	}

	// 确保ACL文件存在
	if _, err := os.Stat(cfg.ACLPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfg.ACLPath, []byte(""), 0644); err != nil {
			log.Fatalf("创建ACL文件失败: %v", err)
		}
	}

	// 启动时同步管理员账号到MQTT服务
	if err := syncAdminToMQTT(); err != nil {
		log.Printf("警告: 同步管理员账号失败: %v", err)
	}

	mux := http.NewServeMux()

	// API路由
	mux.HandleFunc("/api/register", handleRegister)
	mux.HandleFunc("/api/register/verify", handleVerifyEmail)
	mux.HandleFunc("/api/register/add-mac", handleAddMac)
	mux.HandleFunc("/api/admin/login", handleAdminLogin)
	mux.HandleFunc("/api/admin/totp/setup", adminAuth(handleTOTPSetup))
	mux.HandleFunc("/api/admin/totp/enable", adminAuth(handleTOTPEnable))
	mux.HandleFunc("/api/admin/totp/disable", adminAuth(handleTOTPDisable))
	mux.HandleFunc("/api/admin/session", adminAuth(handleAdminSession))
	mux.HandleFunc("/api/admin/device-metadata/", adminAuth(handleAdminDeviceMetadata))
	mux.HandleFunc("/api/admin/device-config", adminAuth(handleAdminGetDeviceConfig))
	mux.HandleFunc("/api/admin/device-config/", adminAuth(handleAdminPutDeviceConfig))
	mux.HandleFunc("/api/admin/power/query", adminAuth(handlePowerQuery))
	mux.HandleFunc("/api/admin/power/summary", adminAuth(handlePowerSummary))
	mux.HandleFunc("/api/admin/task-logs", adminAuth(handleTaskLogs))
	mux.HandleFunc("/api/mqtt/login", handleMQTTLogin)
	mux.HandleFunc("/api/admin/users", adminAuth(handleUsers))
	mux.HandleFunc("/api/admin/user/", adminAuth(handleUserOps))
	mux.HandleFunc("/api/admin/acl", adminAuth(handleACL))
	mux.HandleFunc("/api/admin/config", adminAuth(handleConfig))
	mux.HandleFunc("/api/admin/webhook/test", adminAuth(handleWebhookTest))
	mux.HandleFunc("/api/register/status", handleRegisterStatus)
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		if returnTo := safeReturnTo(r.URL.Query().Get("return_to")); returnTo != "" {
			http.Redirect(w, r, returnTo, http.StatusFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	// 前端静态文件
	fs := http.FileServer(http.Dir("../frontend/dist"))
	mux.Handle("/", fs)

	log.Printf("服务启动在 %s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}
