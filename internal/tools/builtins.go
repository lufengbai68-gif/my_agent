package tools

// RegisterBuiltins 当前无用户态工具。
//
// 历史：之前注册 get_current_time。
// 重构后，生成能力（图片/视频）下沉到 internal/llm 包，
// 由主 agent dispatcher 统一调度（见 internal/tools/generate.go）。
//
// 保留本入口为未来扩展点（如网络搜索、RAG 等）。
func RegisterBuiltins(_ *Registry) {
	// no-op
}
