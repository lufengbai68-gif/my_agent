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
	Sessions SessionConfig           `yaml:"sessions"`
}

// ServerConfig HTTP 服务配置。
type ServerConfig struct {
	Addr              string        `yaml:"addr"`
	Mode              string        `yaml:"mode"` // debug | release | test
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	MaxBodySize       int64         `yaml:"max_body_size"` // bytes
}

// ModelConfig 命名模型实例配置（由 llm.Registry 的工厂解释）。
type ModelConfig struct {
	Type        string   `yaml:"type"` // llm 工厂 key，如 openai
	APIKey      string   `yaml:"api_key"`
	BaseURL     string   `yaml:"base_url"`
	Model       string   `yaml:"model"` // 模型名，如 gpt-4o-mini
	Timeout     time.Duration `yaml:"timeout"`
	Temperature *float32 `yaml:"temperature,omitempty"`
	MaxTokens   *int     `yaml:"max_tokens,omitempty"`
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
		m.APIKey = os.Expand(m.APIKey, getenv)
		m.BaseURL = os.Expand(m.BaseURL, getenv)
		m.Model = os.Expand(m.Model, getenv)
	}
	for _, a := range r.Agents.Definitions {
		a.Model = os.Expand(a.Model, getenv)
		a.Description = os.Expand(a.Description, getenv)
		a.Instruction = os.Expand(a.Instruction, getenv)
	}
	r.Sessions.Store = os.Expand(r.Sessions.Store, getenv)
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
	for name, m := range r.Models {
		if m.Type == "" {
			return fmt.Errorf("models.%s: type is required", name)
		}
		if m.Model == "" {
			return fmt.Errorf("models.%s: model is required", name)
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
