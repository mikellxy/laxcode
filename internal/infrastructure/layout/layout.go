// Package layout 是 LaxCode 磁盘布局的单一真源（single source of truth）。
//
// 在此之前，".laxcode"、"sessions"、"skills"、"log/tracing.log" 这些相对路径
// 片段散落在组合根（cmd/agentasm）、领域提示词（domain/prompt）与基础设施
// 适配器（infrastructure/config）中被各自硬编码：同一条布局规则有三份副本，
// 改一处即静默失配。本包把它们收口为一组纯函数，任何需要落盘位置的调用方
// 一律经此拼装。
//
// 所有持久化数据统一位于用户主目录 ${home}/.laxcode；项目 workdir 只作为
// Agent 工具沙箱，不再决定会话、技能或追踪数据的位置。
//
// 仍有两处“非代码”的布局副本无法由本包约束，改动布局时须同步：
//   - prompt.RenderSkillIndex 向模型描述技能定义文件的相对路径
//   - scripts/clean_session.sh 清理会话目录
package layout

import "path/filepath"

const (
	// RootDirName 是 LaxCode 的用户级数据根目录名。
	RootDirName = ".laxcode"

	// SessionDirName 是会话数据目录名，其下每个会话一个子目录。
	SessionDirName = "sessions"

	// SkillsDirName 是技能定义目录名，其下恰好一层子目录各含一个 SKILL.md。
	SkillsDirName = "skills"

	// settingsFileName 是用户主目录数据根下的配置文件名。
	settingsFileName = "settings.json"

	// sessionDBFileName 是全局 SQLite 会话数据库文件名。
	sessionDBFileName = "sessions.db"

	// knowledgeBaseDirName 与 knowledgeBaseFileName 定位项目内的向量知识库。
	knowledgeBaseDirName  = "kb"
	knowledgeBaseFileName = "kb.sqlite"

	// tracingLogDirName 与 tracingLogFileName 定位会话级 trace 日志。
	tracingLogDirName  = "log"
	tracingLogFileName = "tracing.log"

	// sessionHistoryFileName 是会话不可变原始消息的 JSONL 冷备文件名。
	sessionHistoryFileName = "history.jsonl"
)

// Root 返回用户级数据根 ${homeDir}/.laxcode。
func Root(homeDir string) string {
	return filepath.Join(homeDir, RootDirName)
}

// SessionDB 返回全局会话数据库 ${homeDir}/.laxcode/sessions/sessions.db。
func SessionDB(homeDir string) string {
	return filepath.Join(SessionRoot(homeDir), sessionDBFileName)
}

// KnowledgeBaseDB 返回当前工作目录的知识库 ${workDir}/kb/kb.sqlite。
func KnowledgeBaseDB(workDir string) string {
	return filepath.Join(workDir, knowledgeBaseDirName, knowledgeBaseFileName)
}

// SessionRoot 返回会话文件根 ${homeDir}/.laxcode/sessions；history 冷备、
// artifact、trace 与 Plan Mode 文件仍按 session 分目录保存在这里。
func SessionRoot(homeDir string) string {
	return filepath.Join(Root(homeDir), SessionDirName)
}

// SessionDir 返回单个会话的数据目录 ${homeDir}/.laxcode/sessions/${sessID}，
// Plan Mode 的规划文件（plan.md / design.md 等）即落在这里。
func SessionDir(homeDir, sessID string) string {
	return filepath.Join(SessionRoot(homeDir), sessID)
}

// SessionHistory 返回会话不可变原始消息的 JSONL 冷备路径
// ${homeDir}/.laxcode/sessions/${sessID}/history.jsonl。
func SessionHistory(homeDir, sessID string) string {
	return filepath.Join(SessionDir(homeDir, sessID), sessionHistoryFileName)
}

// TracingLog 返回会话级 trace 日志文件
// ${homeDir}/.laxcode/sessions/${sessID}/log/tracing.log。
func TracingLog(homeDir, sessID string) string {
	return filepath.Join(SessionDir(homeDir, sessID), tracingLogDirName, tracingLogFileName)
}

// SkillsRoot 返回全局技能目录 ${homeDir}/.laxcode/skills，
// 其发现规则见 infrastructure/skillrepo。
func SkillsRoot(homeDir string) string {
	return filepath.Join(Root(homeDir), SkillsDirName)
}

// UserSettings 返回用户级配置文件 ${homeDir}/.laxcode/settings.json。
// 与工作目录布局共用 RootDirName 但根不同，故单列一个函数而非复用 Root。
func UserSettings(homeDir string) string {
	return filepath.Join(homeDir, RootDirName, settingsFileName)
}
