#!/usr/bin/env bash
# build-run.sh —— 编译 qqbot 并以后台常驻进程运行（不需要 sudo 的“手动 systemd”）。
#
# 用法（在项目根目录执行）：
#   ./scripts/build-run.sh          编译 → 停旧实例（若有）→ 后台启动新编译的 qqbot
#   ./scripts/build-run.sh stop     只停止 qqbot 常驻进程
#   ./scripts/build-run.sh status   查看运行状态
#   ./scripts/build-run.sh logs     实时跟踪日志（Ctrl+C 只退出跟踪）
#
# 流程细节：
#   1. go build -o qqbot .   —— 编译失败直接退出，不动正在运行的旧实例（保活优先）
#   2. 停旧实例：若 air 在跑（开发模式）先 ./dev.sh stop（连带 tmp/main）；
#      再停旧的 qqbot 常驻进程（SIGINT 优雅停，超时强杀）
#   3. setsid 后台启动 ./qqbot，日志追加到 logs/qqbot.log，PID 记到 .qqbot.pid
#
# 与其他运行方式的关系：
#   - 开发热重载用 ./scripts/dev.sh（air）；
#   - 真正的开机自启用 scripts/deploy/qqbot.service（systemd）；
#   - 本脚本适合“改完代码，一条命令换新版本继续跑”。

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/qqbot"
PIDFILE="$ROOT/.qqbot.pid"
LOGFILE="$ROOT/logs/qqbot.log"
STOP_WAIT_SEC=10  # 优雅停止的最长等待

cd "$ROOT"

is_running() {
	[ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE" 2>/dev/null)" 2>/dev/null
}

# stop_qqbot 停掉旧的 qqbot 常驻进程：pid 文件优先，兜底 pgrep 按 BIN 全路径匹配。
stop_qqbot() {
	local pids=""
	if [ -f "$PIDFILE" ]; then
		local pid; pid="$(cat "$PIDFILE" 2>/dev/null || true)"
		[ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && pids="$pid"
	fi
	# 兜底：pid 文件丢了但进程还在（比如手动 nohup 起的）
	local extra; extra="$(pgrep -f "$BIN" 2>/dev/null || true)"
	[ -n "$extra" ] && pids="$(printf '%s\n%s\n' "$pids" "$extra" | sort -u | grep -v '^$' || true)"

	if [ -z "$pids" ]; then
		echo "没有正在运行的 qqbot 常驻进程"
		rm -f "$PIDFILE"
		return 0
	fi

	echo "停止 qqbot (PID $(echo $pids | tr '\n' ' '))..."
	local p
	for p in $pids; do kill -s INT "$p" 2>/dev/null || true; done

	local i alive=1
	for ((i = 0; i < STOP_WAIT_SEC * 5; i++)); do
		alive=0
		for p in $pids; do kill -0 "$p" 2>/dev/null && alive=1; done
		[ "$alive" -eq 0 ] && break
		sleep 0.2
	done
	if [ "$alive" -eq 1 ]; then
		echo "仍未退出，强制结束..."
		for p in $pids; do kill -9 "$p" 2>/dev/null || true; done
	fi
	rm -f "$PIDFILE"
	echo "已停止"
}

# stop_air 若开发模式的 air 在跑，一并停掉（否则旧 bot 会占住 WS/单实例锁，新进程起不来）。
# 两条路都查：dev.sh 起的有 .air.pid；手动起的 air 用 pgrep 按进程名兜底。
stop_air() {
	if [ -f "$ROOT/.air.pid" ] && kill -0 "$(cat "$ROOT/.air.pid" 2>/dev/null)" 2>/dev/null; then
		echo "检测到 air 开发模式在运行，先停止..."
		"$ROOT/scripts/dev.sh" stop
	elif pgrep -x air >/dev/null 2>&1; then
		echo "检测到手动启动的 air（无 .air.pid），逐个停止..."
		local p
		for p in $(pgrep -x air); do kill -s INT "$p" 2>/dev/null || true; done
		local i
		for ((i = 0; i < 30; i++)); do
			pgrep -x air >/dev/null 2>&1 || break
			sleep 0.2
		done
		pgrep -x air >/dev/null 2>&1 && pkill -9 -x air 2>/dev/null || true
	fi
	# 遗留的 air bot 子进程（tmp/main）：SIGINT 优雅退，超时强杀
	if pgrep -f "$ROOT/tmp/main" >/dev/null 2>&1; then
		echo "清理遗留的 tmp/main..."
		pkill -INT -f "$ROOT/tmp/main" 2>/dev/null || true
		local j
		for ((j = 0; j < 30; j++)); do
			pgrep -f "$ROOT/tmp/main" >/dev/null 2>&1 || break
			sleep 0.2
		done
		pkill -9 -f "$ROOT/tmp/main" 2>/dev/null || true
	fi
}

cmd_run() {
	# 1. 编译：失败即退出，正在运行的旧实例不受影响
	echo "编译 qqbot..."
	if ! go build -o "$BIN" .; then
		echo "编译失败，保留正在运行的旧实例不动" >&2
		exit 1
	fi
	echo "编译完成: $BIN"

	# 2. 停旧实例（air 开发模式 + 旧常驻进程）
	stop_air
	stop_qqbot

	# 3. 后台启动（setsid 自成会话，脚本退出/终端关闭都不影响）
	mkdir -p "$ROOT/logs"
	echo "后台启动 qqbot..."
	setsid "$BIN" >> "$LOGFILE" 2>&1 &
	echo $! > "$PIDFILE"

	# 4. 稍等确认进程没立刻退出（如配置缺失、单实例锁冲突会在启动早期退出）
	sleep 2
	if ! is_running; then
		echo "启动失败（进程已退出），最近的日志：" >&2
		tail -n 20 "$LOGFILE" >&2 || true
		rm -f "$PIDFILE"
		exit 1
	fi
	echo "qqbot 已后台运行 (PID $(cat "$PIDFILE"))，日志：$LOGFILE"
	echo "跟踪日志：./scripts/build-run.sh logs"
}

cmd_stop() {
	stop_qqbot
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

case "${1:-run}" in
	run) cmd_run ;;
	stop) cmd_stop ;;
	status) cmd_status ;;
	logs) cmd_logs ;;
	*)
		echo "用法：$0 {run|stop|status|logs}（无参数默认 run：编译+停旧+后台启动）" >&2
		exit 1
		;;
esac
