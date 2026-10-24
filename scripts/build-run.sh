#!/usr/bin/env bash
# build-run.sh —— 编译 qqbot 并经 systemd 上线（qqbot-agent.service）。
#
# 用法（在项目根目录执行）：
#   ./scripts/build-run.sh          编译 → systemctl restart（改完代码上线，默认）
#   ./scripts/build-run.sh restart  不编译，直接 systemctl restart
#                                    （改了 .env 只想重载配置、或进程异常想拉起时用）
#   ./scripts/build-run.sh stop     停止服务
#   ./scripts/build-run.sh status   查看运行状态（免 sudo）
#   ./scripts/build-run.sh logs     实时跟踪日志（journalctl，Ctrl+C 只退出跟踪）
#
# 流程细节：
#   1. go build -o qqbot .   —— 编译失败直接退出，不动正在运行的服务（保活优先）
#   2. 若 air 开发模式在跑先停（它占着单实例锁 data/bot.pid，不停掉服务会
#      反复「拒绝重复启动」崩溃循环）
#   3. sudo systemctl restart qqbot-agent —— 对旧进程发 SIGINT 优雅停 → 起新二进制
#   4. 稍等确认服务 active（配置缺失/锁冲突会在启动早期退出），失败贴最近日志
#
# 与其他运行方式的关系：
#   - 服务本体是 scripts/deploy/qqbot.service（本机已装为 qqbot-agent，开机自启）；
#   - 开发热重载用 ./scripts/dev.sh（air），用前先 stop 本服务，用完本脚本 restart 收尾；
#   - 直接 sudo systemctl ... 也可以，本脚本只是把「编译 + 重启 + 确认」收成一条命令。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/qqbot"
UNIT="qqbot-agent.service"

cd "$ROOT"

# stop_air_if_running 重启前停掉 air 开发模式（占单实例锁会让服务起不来）。
# 首选 dev.sh stop（连带 tmp/main 优雅退）；手动起的 air 无 .air.pid，pgrep 兜底。
stop_air_if_running() {
	if { [ -f "$ROOT/.air.pid" ] && kill -0 "$(cat "$ROOT/.air.pid" 2>/dev/null)" 2>/dev/null; } \
		|| pgrep -x air >/dev/null 2>&1; then
		echo "检测到 air 开发模式在运行，先停止..."
		"$ROOT/scripts/dev.sh" stop || true
		pkill -INT -x air 2>/dev/null || true
		pkill -INT -f "$ROOT/tmp/main" 2>/dev/null || true
	fi
}

# restart_service 重启 systemd 服务并确认真的起来了（没 active 就贴日志退出）。
restart_service() {
	stop_air_if_running
	echo "重启 systemd 服务 $UNIT ..."
	sudo systemctl restart "$UNIT"
	sleep 3
	local state; state="$(systemctl is-active "$UNIT")"
	if [ "$state" != "active" ]; then
		echo "服务未进入 active 状态（$state），最近日志：" >&2
		journalctl -u "$UNIT" -n 20 --no-pager >&2 || true
		exit 1
	fi
	echo "$UNIT 已运行 (PID $(systemctl show -p MainPID --value "$UNIT"))"
	echo "跟踪日志：./scripts/build-run.sh logs"
}

cmd_run() {
	echo "编译 qqbot..."
	if ! go build -o "$BIN" .; then
		echo "编译失败，保留正在运行的服务不动" >&2
		exit 1
	fi
	echo "编译完成: $BIN ($(date -r "$BIN" '+%F %T'))"
	restart_service
}

# cmd_restart 不编译直接重启：运行上次编译的二进制。
# 适合只改了 .env 想重载配置、或进程挂了想原样拉起（代码没变就别重编）。
cmd_restart() {
	if [ ! -x "$BIN" ]; then
		echo "未找到已编译的二进制 $BIN，先执行 ./scripts/build-run.sh 编译" >&2
		exit 1
	fi
	echo "跳过编译，直接重启（二进制: $BIN，$(date -r "$BIN" '+%F %T') 编译）"
	restart_service
}

cmd_stop() {
	sudo systemctl stop "$UNIT"
	echo "已停止 $UNIT"
}

cmd_status() {
	systemctl status "$UNIT" --no-pager
}

cmd_logs() {
	journalctl -u "$UNIT" -n 50 -f
}

case "${1:-run}" in
	run|build) cmd_run ;;
	restart) cmd_restart ;;
	stop) cmd_stop ;;
	status) cmd_status ;;
	logs) cmd_logs ;;
	*)
		echo "用法：$0 {run|restart|stop|status|logs}" >&2
		echo "  run（默认）  编译 + systemctl restart（改完代码上线）" >&2
		echo "  restart      不编译，直接 systemctl restart（改 .env 后重载/拉起用）" >&2
		exit 1
		;;
esac
