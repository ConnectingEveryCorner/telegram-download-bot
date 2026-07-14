package desktop

var messages = map[string]map[string]string{
	"zh": {
		"language": "语言", "zh": "简体中文", "en": "English", "confirm": "确认", "ok": "确定", "error": "错误",
		"checking": "正在检查登录状态…", "login": "登录", "logout": "退出登录",
		"link_placeholder": "粘贴 Telegram 消息链接，例如 https://telegram.me/...", "add_download": "添加下载",
		"task_hint": "任务会按设置的并发数自动执行；点击下载中的任务可以取消", "task": "任务",
		"download": "下载", "settings": "设置", "about": "关于",
		"cancel_task": "取消任务", "cancel_task_confirm": "是否取消此任务？", "remove_task": "移除任务", "remove_task_confirm": "是否从本地记录中移除此任务？运行中的任务会先取消", "remove_all": "移除全部", "remove_all_confirm": "是否移除全部本地任务记录？运行中的任务会先取消",
		"login_in_progress": "登录正在进行中", "logout_confirm": "退出会删除此电脑上保存的 Telegram 登录状态，正在下载的任务不会自动停止，是否继续？",
		"not_logged_in": "未登录，请先登录 Telegram 账号", "settings_save": "保存设置",
		"invalid_tasks": "同时任务数必须是大于 0 的整数", "invalid_threads": "单任务下载线程数必须是大于 0 的整数", "invalid_pool": "连接池大小必须是大于 0 的整数", "choose_dir": "请选择下载目录",
		"settings_saved": "设置已保存；新启动的任务将使用新配置", "settings_saved_title": "保存成功", "download_dir": "下载目录", "browse_dir": "选择目录", "proxy": "代理地址", "api_id": "Telegram API ID", "api_hash": "Telegram API Hash", "api_note_prefix": "请前往 ", "api_note_suffix": " 创建应用并填写 api_id 与 api_hash", "api_required": "请先在设置中填写 Telegram API ID 和 API Hash", "invalid_api_id": "Telegram API ID 必须是大于 0 的整数", "concurrent_tasks": "同时任务数", "download_threads": "单任务下载线程数", "pool_size": "DC 连接池大小", "settings_note": "代理、并发和线程数会在新任务开始时生效",
		"version": "当前版本：", "remote_version": "远程版本：", "checking_version": "正在检查", "version_check_failed": "检查失败", "download_latest": "前往 Release 下载最新版本", "website": "官网：ceckit.com", "about_text": "基于 tdl 构建的 Telegram 桌面下载工具", "license": "开源协议：AGPL-3.0", "interrupted": "应用关闭时任务未完成",
		"invalid_link": "请输入有效的 Telegram 消息链接", "state.waiting": "等待中", "state.running": "下载中", "state.done": "已完成", "state.failed": "失败", "state.canceled": "已取消", "saved": "已保存到下载目录",
		"status_failed": "登录状态检查失败：", "logged_in": "已登录（全局仅使用一个 Telegram 账号）", "login_title": "登录 Telegram", "login_start": "开始登录", "cancel": "取消", "phone": "手机号", "phone_placeholder": "+8613800000000", "phone_required": "请输入手机号", "logging_in": "正在登录…", "login_failed": "登录失败：", "code": "验证码", "code_prompt": "请输入 Telegram 发送的验证码", "password": "两步验证密码", "password_prompt": "请输入两步验证密码", "continue": "继续",
		"signup_unsupported": "不支持通过桌面端注册 Telegram 账号", "tos_unsupported": "不支持通过桌面端接受 Telegram 服务条款",
	},
	"en": {
		"language": "Language", "zh": "简体中文", "en": "English", "confirm": "Confirm", "ok": "OK", "error": "Error",
		"checking": "Checking sign-in status…", "login": "Sign in", "logout": "Sign out",
		"link_placeholder": "Paste a Telegram message link, e.g. https://telegram.me/...", "add_download": "Add download",
		"task_hint": "Tasks run automatically using the configured concurrency. Select a running task to cancel it.", "task": "Task",
		"download": "Downloads", "settings": "Settings", "about": "About",
		"cancel_task": "Cancel task", "cancel_task_confirm": "Cancel this task?", "remove_task": "Remove task", "remove_task_confirm": "Remove this task from local history? A running task will be canceled first", "remove_all": "Remove all", "remove_all_confirm": "Remove all local task history? Running tasks will be canceled first",
		"login_in_progress": "Sign-in is already in progress", "logout_confirm": "This removes the locally saved Telegram session. Active downloads will not stop automatically. Continue?",
		"not_logged_in": "Not signed in. Sign in to a Telegram account first.", "settings_save": "Save settings",
		"invalid_tasks": "Concurrent tasks must be a positive integer", "invalid_threads": "Download threads must be a positive integer", "invalid_pool": "Pool size must be a positive integer", "choose_dir": "Choose a download directory",
		"settings_saved": "Settings saved; newly started tasks will use the new configuration", "settings_saved_title": "Settings saved", "download_dir": "Download directory", "browse_dir": "Choose folder", "proxy": "Proxy address", "api_id": "Telegram API ID", "api_hash": "Telegram API Hash", "api_note_prefix": "Create an application at ", "api_note_suffix": " and enter its api_id and api_hash", "api_required": "Set the Telegram API ID and API Hash in Settings first", "invalid_api_id": "Telegram API ID must be a positive integer", "concurrent_tasks": "Concurrent tasks", "download_threads": "Download threads per task", "pool_size": "DC pool size", "settings_note": "Proxy, concurrency, and thread changes apply to newly started tasks",
		"version": "Current version: ", "remote_version": "Remote version: ", "checking_version": "Checking", "version_check_failed": "Check failed", "download_latest": "Download the latest release", "website": "Website: ceckit.com", "about_text": "A Telegram desktop downloader built on tdl.", "license": "License: AGPL-3.0", "interrupted": "Task was unfinished when the app closed",
		"invalid_link": "Enter a valid Telegram message link", "state.waiting": "Waiting", "state.running": "Downloading", "state.done": "Completed", "state.failed": "Failed", "state.canceled": "Canceled", "saved": "Saved to the download directory",
		"status_failed": "Unable to check sign-in status: ", "logged_in": "Signed in (one Telegram account is used globally)", "login_title": "Sign in to Telegram", "login_start": "Start sign-in", "cancel": "Cancel", "phone": "Phone number", "phone_placeholder": "+8613800000000", "phone_required": "Enter a phone number", "logging_in": "Signing in…", "login_failed": "Sign-in failed: ", "code": "Verification code", "code_prompt": "Enter the code sent by Telegram", "password": "Two-step verification password", "password_prompt": "Enter your two-step verification password", "continue": "Continue",
		"signup_unsupported": "Telegram account sign-up is not supported in the desktop app", "tos_unsupported": "Accepting Telegram terms is not supported in the desktop app",
	},
}

func (d *Desktop) tr(key string) string {
	lang := d.settings.Language
	if lang == "" {
		lang = "zh"
	}
	if value := messages[lang][key]; value != "" {
		return value
	}
	return messages["zh"][key]
}
