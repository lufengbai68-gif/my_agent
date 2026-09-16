// Package config 加载并校验服务配置（yaml + ${ENV} 环境变量展开）。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Root 是配置文件的顶层结构。
type Root struct {
	Server   ServerConfig            `yaml:"server"`
	Models   map[string]*ModelConfig `yaml:"models"`
	Agents   AgentsConfig            `yaml:"agents"`
	Tools    map[string]*ToolConfig  `yaml:"tools"`
	Sessions SessionConfig           `yaml:"sessions"`
	Uploads  UploadsConfig           `yaml:"uploads"`
	Logging  LoggingConfig           `yaml:"logging"`
}

// ToolConfig 单个工具的配置。
//
// Type 路由到 tools.Registry 的工厂（与 models.*.type 同模式）；
// 其余字段透传给具体工厂（不同工具字段集合不同）。
type ToolConfig struct {
	Type   string         `yaml:"type"`         // 路由 key
	Params map[string]any `yaml:",inline"`      // 透传给工厂的配置
}

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	Addr              string        `yaml:"addr"`
	Mode              string        `yaml:"mode"` // debug | release | test
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	MaxBodySize       int64         `yaml:"max_body_size"` // bytes
}

// ModelConfig 模型实例配置。语义分两层：
//
//   - Type: 能力类别（chat | image | video | audio）
//   - ModelType: 用户视角的能力分类：chat | image | video | audio
//
// 按 ModelType 决定走 chat factory 还是 generation factory。
//
// ChatOnly=true 表示该模型仅作为隐藏的对话后端（用于主 agent 路由），
// 不会暴露给前端 GET /v1/models。
//
// Type + Provider 联合路由：Type 表能力类别（chat/image/video/audio），
// Provider 表具体实现协议（openai / seedance / ark / ...）。
// 这种拆分避免 type 字段同时承载双重语义（如"seedance_video"）。
type ModelConfig struct {
	Type         string        `yaml:"type"`           // 能力类别：chat | image | video | audio
	Provider     string        `yaml:"provider"`       // 协议供应商：openai | ark | ...
	ModelType    string        `yaml:"model_type"`     // 兼容旧字段；与 Type 一致时可省略
	ChatOnly     bool          `yaml:"chat_only"`      // 前端是否可见
	Label        string        `yaml:"label"`          // 前端展示名
	Capabilities Capabilities  `yaml:"capabilities"`   // 能力标签
	APIKey       string        `yaml:"api_key"`
	BaseURL      string        `yaml:"base_url"`
	Model        string        `yaml:"model"`          // 模型名 / 接入点 ID
	Timeout      time.Duration `yaml:"timeout"`
	Temperature  *float32      `yaml:"temperature,omitempty"`
	MaxTokens    *int          `yaml:"max_tokens,omitempty"`

	// 生成模型专用
	PollInterval time.Duration `yaml:"poll_interval,omitempty"` // 视频轮询间隔
	MaxWait      time.Duration `yaml:"max_wait,omitempty"`      // 视频生成总超时
}

// Capabilities 多模态能力标签。
type Capabilities struct {
	Text  bool `yaml:"text"`
	Image bool `yaml:"image"`
	Audio bool `yaml:"audio"`
	Video bool `yaml:"video"`
	File  bool `yaml:"file"`
}

// AgentsConfig agent 注册配置。
type AgentsConfig struct {
	Default     string                  `yaml:"default"` // 空 agent 名时的兜底
	Definitions map[string]*AgentConfig `yaml:"definitions"`
}

// AgentConfig 单个 agent 定义。yaml key 即 agent 名。
type AgentConfig struct {
	Model         string `yaml:"model"` // 引用 models.<key>
	Description   string `yaml:"description"`
	Instruction   string `yaml:"instruction"`
	MaxIterations int    `yaml:"max_iterations"`
}

// SessionConfig 会话存储配置。
type SessionConfig struct {
	Store       string `yaml:"store"` // memory | redis(V4)
	MaxMessages int    `yaml:"max_messages"`
}

// Load 读取 yaml 配置文件，展开字符串字段中的 ${VAR}/$VAR，
// 并做一致性校验（模型引用、默认 agent、store 合法性）。
func Load(path string) (*Root, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var raw Root
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	expandConfig(&raw)

	if err := raw.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &raw, nil
}

// expandConfig 递归展开所有字符串配置字段中的环境变量引用。
func expandConfig(r *Root) {
	r.Server.Addr = os.Expand(r.Server.Addr, getenv)
	r.Server.Mode = os.Expand(r.Server.Mode, getenv)
	for _, m := range r.Models {
		m.Type = os.Expand(m.Type, getenv)
		m.Provider = os.Expand(m.Provider, getenv)
		m.ModelType = os.Expand(m.ModelType, getenv)
		m.Label = os.Expand(m.Label, getenv)
		m.APIKey = os.Expand(m.APIKey, getenv)
		m.BaseURL = os.Expand(m.BaseURL, getenv)
		m.Model = os.Expand(m.Model, getenv)
		// ModelType 与 Type 同步（兼容旧字段）
		if m.ModelType == "" {
			m.ModelType = m.Type
		}
	}
	for _, a := range r.Agents.Definitions {
		a.Model = os.Expand(a.Model, getenv)
		a.Description = os.Expand(a.Description, getenv)
		a.Instruction = os.Expand(a.Instruction, getenv)
	}
	r.Sessions.Store = os.Expand(r.Sessions.Store, getenv)

	// tool 段的 Params 内部字符串也展开环境变量（递归到任意深度）
	for _, t := range r.Tools {
		expandMap(t.Params)
	}

	// uploads
	r.Uploads.Store = os.Expand(r.Uploads.Store, getenv)
	r.Uploads.ArkOSS.Bucket = os.Expand(r.Uploads.ArkOSS.Bucket, getenv)
	r.Uploads.ArkOSS.Region = os.Expand(r.Uploads.ArkOSS.Region, getenv)
	r.Uploads.ArkOSS.Endpoint = os.Expand(r.Uploads.ArkOSS.Endpoint, getenv)
	r.Uploads.ArkOSS.AccessKey = os.Expand(r.Uploads.ArkOSS.AccessKey, getenv)
	r.Uploads.ArkOSS.SecretKey = os.Expand(r.Uploads.ArkOSS.SecretKey, getenv)
	r.Uploads.ArkOSS.DirPrefix = os.Expand(r.Uploads.ArkOSS.DirPrefix, getenv)
}

func expandMap(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			m[k] = os.Expand(val, getenv)
		case map[string]any:
			expandMap(val)
		}
	}
}

// getenv 支持 ${VAR} 与 ${VAR:-default} 两种形式；
// 普通 $VAR 未设置时返回空串（os.Expand 会跳过空值结果）。
func getenv(name string) string {
	// ${VAR:-default} 形式：os.Expand 会把 "VAR:-default" 整体作为 name 传入
	if i := strings.Index(name, ":-"); i >= 0 {
		if v, ok := os.LookupEnv(name[:i]); ok {
			return v
		}
		return name[i+2:]
	}
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return ""
}

func (r *Root) validate() error {
	if len(r.Models) == 0 {
		return fmt.Errorf("at least one model is required")
	}
	// tools 段可选：未声明则不挂任何工具
	if r.Tools == nil {
		r.Tools = map[string]*ToolConfig{}
	}
	for name, m := range r.Models {
		if m.Type == "" {
			return fmt.Errorf("models.%s: type is required", name)
		}
		if m.Model == "" {
			return fmt.Errorf("models.%s: model is required", name)
		}
		if m.Type == "" {
			return fmt.Errorf("models.%s: type is required (chat|image|video|audio)", name)
		}
		if m.Provider == "" {
			return fmt.Errorf("models.%s: provider is required (openai|ark|...)", name)
		}
		switch m.Type {
		case "chat", "image", "video", "audio":
			// OK
		default:
			return fmt.Errorf("models.%s: invalid type %q (supported: chat|image|video|audio)", name, m.Type)
		}
		// chat 类需要 api_key + base_url
		if m.Type == "chat" {
			if m.APIKey == "" {
				return fmt.Errorf("models.%s: api_key is required for chat models", name)
			}
			if m.BaseURL == "" {
				return fmt.Errorf("models.%s: base_url is required for chat models", name)
			}
		}
		// 生成类需要 api_key + base_url + model
		if m.Type != "chat" {
			if m.APIKey == "" {
				return fmt.Errorf("models.%s: api_key is required for generation models", name)
			}
			if m.BaseURL == "" {
				return fmt.Errorf("models.%s: base_url is required for generation models", name)
			}
		}
	}
	if len(r.Agents.Definitions) == 0 {
		return fmt.Errorf("at least one agent definition is required")
	}
	for name, a := range r.Agents.Definitions {
		if a.Model == "" {
			return fmt.Errorf("agents.definitions.%s: model is required", name)
		}
		if _, ok := r.Models[a.Model]; !ok {
			return fmt.Errorf("agents.definitions.%s: unknown model %q", name, a.Model)
		}
	}
	if r.Agents.Default == "" {
		return fmt.Errorf("agents.default is required")
	}
	if _, ok := r.Agents.Definitions[r.Agents.Default]; !ok {
		return fmt.Errorf("agents.default %q not found in agents.definitions", r.Agents.Default)
	}
	switch r.Sessions.Store {
	case "", "memory":
		r.Sessions.Store = "memory"
	default:
		return fmt.Errorf("sessions.store %q is not supported (supported: memory)", r.Sessions.Store)
	}

	// logging 默认值
	if r.Logging.Level == "" {
		r.Logging.Level = "info"
	}
	if r.Logging.Format == "" {
		r.Logging.Format = "json"
	}
	if len(r.Logging.Redact) == 0 {
		r.Logging.Redact = []string{"api_key", "authorization", "x-api-key"}
	}

	// uploads 默认值
	if r.Uploads.TTL == 0 {
		r.Uploads.TTL = 24 * time.Hour
	}
	// r.Uploads.Store == "" 表示未启用 uploads, 不强行填默认值;
	// main.go 会按 "未配置" 跳过整段初始化, 也就不要求 AK/SK 必填。
	//
	// PresignTTL 仅在用户实际选 ark_oss 时给默认值。
	if r.Uploads.Store == "ark_oss" && r.Uploads.ArkOSS.PresignTTL == 0 {
		r.Uploads.ArkOSS.PresignTTL = time.Hour
	}

	// server 默认值
	if r.Server.Addr == "" {
		r.Server.Addr = ":8080"
	}
	if r.Server.Mode == "" {
		r.Server.Mode = "release"
	}
	if r.Server.MaxBodySize <= 0 {
		r.Server.MaxBodySize = 32 << 20
	}
	return nil
}

// UploadsConfig 上传配置。把客户端文件存到对象存储,返回可被上游生成 API 直接 fetch 的 URL。
//
// 当前只实现 ark_oss: 直接走火山对象存储,URL 是 ark-cdn 域名,免公网暴露本机。
type UploadsConfig struct {
	Store   string         `yaml:"store"`   // 当前只支持 "ark_oss"
	ArkOSS  ArkOSSConfig   `yaml:"ark_oss"`
	MaxSize int64          `yaml:"max_size"`          // 全局上限(字节),0 = 不限
	TTL     time.Duration  `yaml:"ttl"`               // 元数据保留时长, 0 = 默认24h
	AllowedMime []string    `yaml:"allowed_mime"`      // MIME 白名单, nil = 全部放行
	Domains []string       `yaml:"domains"`           // reference_images 允许的 URL 域
}

// LoggingConfig 日志配置。
//
// Level: debug | info | warn | error
// Format: json (生产) | text (开发)
// Redact: 命中字段名(大小写不敏感)的 attr 值会被打码成 ***REDACTED***
type LoggingConfig struct {
	Level  string   `yaml:"level"`
	Format string   `yaml:"format"`
	Redact []string `yaml:"redact"`
}

// ArkOSSConfig 火山引擎对象存储配置。
type ArkOSSConfig struct {
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	DirPrefix string `yaml:"dir_prefix"`
	PresignTTL time.Duration `yaml:"presign_ttl"`  // 预签名 URL 有效期,默认 1h
}
