package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// AssessBashRisk inspects shell syntax before execution. A static check cannot
// predict expansions or the effects of arbitrary programs, so uncertain forms
// also require a human decision. workDir is the base directory for judging
// whether file redirect targets stay within the permitted scope.
func AssessBashRisk(command, workDir string) (reason string, risky bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Sprintf("无法解析 Bash 命令: %v", err), true
	}
	return assessBashTree(file, workDir)
}

func assessBashTree(file *syntax.File, workDir string) (string, bool) {
	var reason string
	syntax.Walk(file, func(node syntax.Node) bool {
		if reason != "" || node == nil {
			return reason == ""
		}
		switch n := node.(type) {
		case *syntax.Redirect:
			reason = assessBashRedirect(n, workDir)
		case *syntax.CallExpr:
			reason = assessBashCall(n)
		}
		return reason == ""
	})
	return reason, reason != ""
}

// assessBashRedirect 放行 fd 复制/关闭（2>&1、>&-）与输入重定向（<、<<、<<<）；
// 输出到文件的重定向（>、>>、>|、<>、>&file、&>）只放行 workdir、/tmp 与
// /dev/null 内的目标，其余（含动态目标、~ 展开、.. 与符号链接逃逸）需人工确认。
func assessBashRedirect(n *syntax.Redirect, workDir string) string {
	switch n.Op {
	case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return ""
	case syntax.DplIn, syntax.DplOut:
		if target, ok := staticBashWord(n.Word); ok && bashIsFdRef(target) {
			return ""
		}
	}
	target, ok := staticBashWord(n.Word)
	if !ok {
		return "重定向目标含动态展开，无法确认写入位置"
	}
	if target == "" {
		return "重定向目标为空，无法确认写入位置"
	}
	if !bashRedirectTargetAllowed(target, workDir) {
		return "命令重定向写入受限范围之外的文件: " + target
	}
	return ""
}

func bashIsFdRef(target string) bool {
	if target == "-" {
		return true
	}
	digits := true
	for _, r := range target {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	return digits && target != ""
}

// bashRedirectTargetAllowed 判断输出重定向目标是否落在放行范围：
// /dev/null、/tmp 内或 workdir 内。相对路径按 workdir 解析；目标经 ..
// 或符号链接指向范围之外时判为不允许。workDir 为空时相对路径一律不允许。
func bashRedirectTargetAllowed(target, workDir string) bool {
	if target == "/dev/null" {
		return true
	}
	if strings.HasPrefix(target, "~") {
		return false
	}
	if !filepath.IsAbs(target) {
		if workDir == "" {
			return false
		}
		target = filepath.Join(workDir, target)
	}
	target = resolveBashRedirectPath(filepath.Clean(target))
	if bashPathWithin(target, "/tmp") {
		return true
	}
	return workDir != "" && bashPathWithin(target, workDir)
}

// resolveBashRedirectPath 尽力解析路径中的符号链接：目标已存在时整体解析
// （含末段链接，防止 workdir 内链接指向受限范围之外）；尚未存在时解析到
// 最深存在的父目录。
func resolveBashRedirectPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir, base := filepath.Split(path)
	if resolved, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(resolved, base)
	}
	return path
}

func bashPathWithin(path, dir string) bool {
	root := filepath.Clean(dir)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func assessBashCall(call *syntax.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	name, ok := staticBashWord(call.Args[0])
	if !ok || name == "" {
		return "命令名含动态展开，无法可靠检查"
	}
	name = filepath.Base(name)
	args := make([]string, 0, len(call.Args)-1)
	dynamicArg := false
	for _, word := range call.Args[1:] {
		value, ok := staticBashWord(word)
		if !ok {
			dynamicArg = true
			value = ""
		}
		args = append(args, value)
	}
	switch name {
	case "sudo", "doas":
		return "命令使用提权工具 " + name
	case "rm", "rmdir", "dd", "shred", "truncate", "shutdown", "reboot", "poweroff", "halt", "kill", "pkill", "killall":
		return "命令包含高风险操作 " + name
	case "eval", "source", ".":
		return "命令会执行动态或外部脚本 " + name
	case "env", "command", "builtin", "nice", "nohup", "time", "xargs":
		return "命令通过包装器执行其他程序 " + name
	case "bash", "sh", "zsh":
		for i, arg := range args {
			if arg == "-c" || arg == "-lc" || arg == "-ec" {
				if i+1 >= len(args) || args[i+1] == "" {
					return "内层 shell 脚本无法可靠检查"
				}
				return "命令启动内层 shell 执行脚本"
			}
		}
		return "命令启动外部 shell 脚本或从标准输入执行"
	case "git":
		if dynamicArg {
			return "git 参数含动态展开，无法可靠检查"
		}
		if containsBashArg(args, "clean") {
			return "git clean 可能删除未跟踪文件"
		}
		if containsBashArg(args, "reset") && containsBashArg(args, "--hard") {
			return "git reset --hard 会丢弃工作区改动"
		}
		if containsBashArg(args, "restore") || (containsBashArg(args, "checkout") && containsBashArg(args, "--")) {
			return "git 操作可能丢弃工作区改动"
		}
		if containsBashArg(args, "push") && (hasBashArgPrefix(args, "--force") || containsBashArg(args, "-f")) {
			return "git push --force 会覆盖远端历史"
		}
	case "find":
		if dynamicArg {
			return "find 参数含动态展开，无法可靠检查"
		}
		if containsBashArg(args, "-delete") || containsBashArg(args, "-exec") {
			return "find 包含删除或执行操作"
		}
	case "chmod", "chown", "chgrp":
		if containsBashArg(args, "-R") || containsBashArg(args, "--recursive") {
			return "命令递归修改文件权限或所有者"
		}
	case "sed":
		if hasBashArgPrefix(args, "-i") || containsBashArg(args, "--in-place") {
			return "sed 原地修改文件"
		}
	}
	if strings.HasPrefix(name, "mkfs") {
		return "命令包含文件系统格式化操作"
	}
	return ""
}

func staticBashWord(word *syntax.Word) (string, bool) {
	var out strings.Builder
	var appendParts func([]syntax.WordPart) bool
	appendParts = func(parts []syntax.WordPart) bool {
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.Lit:
				out.WriteString(p.Value)
			case *syntax.SglQuoted:
				out.WriteString(p.Value)
			case *syntax.DblQuoted:
				if !appendParts(p.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	ok := appendParts(word.Parts)
	return out.String(), ok
}

func containsBashArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func hasBashArgPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}
