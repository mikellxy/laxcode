// 沙箱外文件访问的 human-in-the-loop 确认：read_file / write_file /
// edit_file 的目标不在工作目录（及配置的读写根）之内时，不再直接失败，而是
// 发起一次人工确认；批准后按确认过的目标放行一次，拒绝或无前端时的语义见
// ToolConfirmation.Grants / Mandatory 与 reactservice.executeToolCall。
package tools

import "fmt"

// FileAccessConfirmationKind 是 read/write/edit 访问沙箱外路径的确认类别，
// 前端据此展示专用标题。
const FileAccessConfirmationKind = "file_path"

// confirmFileAccess 构造沙箱外文件访问的人工确认。授权目标是解析后的目标
// 路径（Clean，与 Execute 的放行匹配口径一致）；文案另展示 EvalSymlinks
// 解析后的真实路径，防止 symlink 伪装目标。
func confirmFileAccess(action, requested, real string) *ToolConfirmation {
	return &ToolConfirmation{
		Kind:   FileAccessConfirmationKind,
		Grants: []string{requested},
		Content: fmt.Sprintf(
			"%s 目标 %q 位于工作目录沙箱之外。\n实际路径：%s\n输入 yes 允许本次访问；其他输入取消。",
			action, requested, real),
	}
}
