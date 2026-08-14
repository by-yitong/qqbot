#!/usr/bin/env bash
# dev.sh —— 优雅地在后台 运行 / 停止 / 重启 air（及其 bot 子进程）
#
# 用法（在项目根目录执行）：
#   ./scripts/dev.sh start     后台启动 air（热重载开发模式）
#   ./scripts/dev.sh stop      优雅停止（给 air 与 bot 整个会话发 SIGINT，bot 干净断开）
#   ./scripts/dev.sh restart   先停再启
#   ./scripts/dev.sh status    查看是否在运行
#   ./scripts/dev.sh logs      实时跟踪日志（Ctrl+C 只退出跟踪，不影响 air）
#
# 原理：用 setsid 让 air 自成会话组长；停止时枚举「整个会话」内进程逐个发 SIGINT，
#       air 与 bot 都会优雅退出，不会有遗留的 bot 进程。

set -euo pipefail

# 脚本在 scripts/ 下，ROOT 指向项目根（.air.pid / logs / .air.toml 都在那里）
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AIR="$(go env GOPATH)/bin/air"
PIDFILE="$ROOT/.air.pid"
LOGFILE="$ROOT/logs/air.log"
WAIT_SEC=12  # 优雅停止的最长等待

cd "$ROOT"

is_running() {
	[ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE" 2>/dev/null)" 2>/dev/null
}

cmd_start() {
	if is_running; then
		echo "air 已在运行 (PID $(cat "$PIDFILE"))，如需重启请用 ./dev.sh restart"
		return 0
	fi
	if [ ! -x "$AIR" ]; then
		echo "未找到 air：$AIR" >&2
		echo "请先安装：go install github.com/air-verse/air@latest" >&2
		return 1
	fi
	mkdir -p "$ROOT/logs"
	echo "启动 air..."
	# setsid：air 成为新会话/进程组的组长，PGID == 其 PID
	setsid "$AIR" > "$LOGFILE" 2>&1 &
	echo $! > "$PIDFILE"
	sleep 1
	if is_running; then
		echo "air 已后台运行 (PID $(cat "$PIDFILE"))，日志：$LOGFILE"
		echo "跟踪日志：./dev.sh logs"
	else
		echo "air 启动失败，查看日志：$LOGFILE" >&2
		tail -n 20 "$LOGFILE" >&2 || true
		rm -f "$PIDFILE"
		return 1
	fi
}

cmd_stop() {
	if ! is_running; then
		echo "air 未运行"
		rm -f "$PIDFILE"
		return 0
	fi
	local pid; pid="$(cat "$PIDFILE")"
	echo "优雅停止 air 会话 (SID $pid)..."

	# air 由 setsid 启动、是会话组长(SID==PID)。向「会话内」所有进程（air + bot）
	# 逐个发 SIGINT，这样 bot 也会优雅退出（打印「bot 已停止」），而不是被强杀。
	local members; members="$(session_members "$pid")"
	[ -n "$members" ] || members="$pid"
	local m
	for m in $members; do kill -s INT "$m" 2>/dev/null || true; done

	# 轮询等待 air 主进程退出
	local i
	for ((i = 0; i < WAIT_SEC * 5; i++)); do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.2
	done

	# 仍未退出则强杀整个会话
	if kill -0 "$pid" 2>/dev/null; then
		echo "仍未退出，强制结束会话..."
		members="$(session_members "$pid")"; [ -n "$members" ] || members="$pid"
		for m in $members; do kill -9 "$m" 2>/dev/null || true; done
	fi
	rm -f "$PIDFILE"

	# 兜底：清理可能遗留的 bot 进程（tmp/main）
	local botp="tmp/mai""n"
	if pgrep -f "$botp" >/dev/null 2>&1; then
		pkill -9 -f "$botp" 2>/dev/null || true
	fi
	echo "已停止"
}

# session_members <sid>：列出某会话内的所有 PID（air 是会话组长，含其 bot 子进程）
session_members() {
	ps -eo pid=,sid= 2>/dev/null | awk -v s="$1" '$2==s {print $1}'
}

cmd_status() {
	if is_running; then
		echo "运行中 (PID $(cat "$PIDFILE"))，日志：$LOGFILE"
	else
		echo "未运行"
	fi
}

cmd_logs() {
	tail -n 50 -f "$LOGFILE"
}

case "${1:-}" in
	start) cmd_start ;;
	stop) cmd_stop ;;
	restart) cmd_stop; cmd_start ;;
	status) cmd_status ;;
	logs) cmd_logs ;;
	*)
		echo "用法：$0 {start|stop|restart|status|logs}" >&2
		exit 1
		;;
esac
