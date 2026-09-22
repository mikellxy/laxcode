package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// AssessBashRisk inspects shell syntax before execution. A static check cannot
// predict expansions or the effects of arbitrary programs, so uncertain forms
// also require a human decision.
func AssessBashRisk(command string) (reason string, risky bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Sprintf("无法解析 Bash 命令: %v", err), true
	}
	return assessBashTree(file)
}

func assessBashTree(file *syntax.File) (string, bool) {
	var reason string
	syntax.Walk(file, func(node syntax.Node) bool {
		if reason != "" || node == nil {
			return reason == ""
		}
		switch n := node.(type) {
		case *syntax.Redirect:
			if n.Op == syntax.RdrOut || n.Op == syntax.AppOut || n.Op == syntax.RdrClob {
				reason = "命令包含文件输出重定向"
			}
		case *syntax.CallExpr:
			reason = assessBashCall(n)
		}
		return reason == ""
	})
	return reason, reason != ""
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
