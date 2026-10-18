package tools

const CloudBashEnvironment = "Bash 直接运行在当前用户的 OpenShell Linux 沙箱内，项目工作目录为 /workplace/{project_name}，HOME 为 /workplace/agenthome，TMPDIR 是用户临时目录。每次调用启动独立 shell；环境变量和 cd 不跨调用保留，文件会保留。调用结束会清理该调用的后台子进程；启动服务、测试和结束服务应在同一次 Bash 调用完成。不要修改会话数据库或平台配置。Skills 脚本生成的文件和缓存应写入项目目录或 TMPDIR。"
const CloudBashOfflineHint = "网络访问由 OpenShell 策略控制。遇到策略拒绝、下载失败或缺少依赖时，优先使用镜像内工具和现有依赖；不要绕过沙箱或反复更换地址规避策略。明确说明无法完成的验证，不能把模拟结果当作真实测试通过。"
