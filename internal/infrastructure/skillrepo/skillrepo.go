// Package skillrepo 是 prompt.SkillSource 端口的文件系统实现：扫描用户主目录下
// ${HOME}/.laxcode/skills/ 恰好一层子目录中的 SKILL.md，把原文交给领域层
// 解析校验。路径布局取自 infrastructure/layout，本包只负责「发现」，
// 不做任何 frontmatter 解析或字段校验——那是 domain/prompt 的职责。
package skillrepo

import (
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

// skillFileName 是技能定义文件的精确名称（大小写敏感）。
const skillFileName = "SKILL.md"

// Source 绑定用户主目录；List 的 workDir 参数为领域端口的兼容参数，技能不再
// 随项目工作目录变化，主 Agent 与子 Agent 始终看到同一份全局技能。
type Source struct {
	HomeDir  string
	UserRoot string
}

var _ prompt.SkillSource = (*Source)(nil)

// List 返回 ${HOME}/.laxcode/skills/ 下恰好一层子目录中的 SKILL.md 原文，
// 每条带上其所在目录名（技能身份校验需要它）。
//
// 以下情形一律静默忽略，既不报错也不产生警告（规范要求发现层面的缺失对
// 用户不可见）：skills 目录不存在或不可读、skills 根下的散置文件、嵌套更深
// 层级里的 SKILL.md（不递归）、技能目录内文件名大小写不符、读取失败。
func (s Source) List(_ string) []prompt.SkillFile {
	return s.listRooted(layout.SkillsRoot(s.HomeDir))
}

func NewWithRoot(homeDir, userRoot string) *Source {
	return &Source{HomeDir: homeDir, UserRoot: userRoot}
}
func (s Source) listRooted(skillsRoot string) []prompt.SkillFile {
	root, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		return nil
	}
	defer root.Close()
	rel, err := filepath.Rel(s.UserRoot, skillsRoot)
	if err != nil {
		return nil
	}
	if err := root.MkdirAll(rel, 0700); err != nil {
		return nil
	}
	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(rel))
	if err != nil {
		return nil
	}
	var files []prompt.SkillFile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(rel, entry.Name())
		children, err := fs.ReadDir(root.FS(), filepath.ToSlash(dir))
		if err != nil {
			continue
		}
		for _, child := range children {
			if child.Name() != skillFileName || !child.Type().IsRegular() {
				continue
			}
			content, err := root.ReadFile(filepath.Join(dir, skillFileName))
			if err == nil {
				files = append(files, prompt.SkillFile{DirName: entry.Name(), Content: string(content)})
			}
			break
		}
	}
	return files
}
