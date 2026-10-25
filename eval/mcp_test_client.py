#!/usr/bin/env python3
"""keeprisk-mcp 轻量 MCP 测试客户端（streamable HTTP）。

用法：
    python3 eval/mcp_test_client.py <tool> '<json args>' [--base URL] [--max-chars N]

示例：
    python3 eval/mcp_test_client.py resolve_entity '{"keyword":"安徽智侒信信息技术有限公司"}'
    python3 eval/mcp_test_client.py keeprisk_query '{"pcode":"院校信息","params":{"entName":"安徽智侒信信息技术有限公司"}}'

说明：
    - 鉴权 token 取自 .env 的 KEEPRISK_AUTH_MAP（JSON 对象，取第一个 key）。
    - 每次运行新建 MCP 会话（initialize → tools/call），用完即弃，不影响他人会话。
    - 默认输出截断到 4000 字符（--max-chars 0 不截断），避免链路测试刷屏。
    - 只读测试工具：不重启服务、不改代码。
"""

import argparse
import json
import os
import sys
import urllib.request

DEFAULT_BASE = "http://127.0.0.1:8080/mcp"
ENV_PATH = os.path.join(os.path.dirname(__file__), "..", ".env")


def load_token(env_path=ENV_PATH, index=0):
    with open(env_path, encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("KEEPRISK_AUTH_MAP"):
                raw = line.split("=", 1)[1].strip().strip("'").strip('"')
                keys = list(json.loads(raw).keys())
                return keys[index % len(keys)]
    raise SystemExit(f"未在 {env_path} 找到 KEEPRISK_AUTH_MAP")


def post(base, token, payload, session=None, timeout=90):
    req = urllib.request.Request(base, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    req.add_header("Authorization", token)
    if session:
        req.add_header("Mcp-Session-Id", session)
    resp = urllib.request.urlopen(req, timeout=timeout)
    sid = resp.headers.get("Mcp-Session-Id") or session
    body = resp.read().decode()
    for line in body.splitlines():  # 兼容 SSE 帧
        if line.startswith("data:"):
            body = line[5:].strip()
            break
    return sid, (json.loads(body) if body.strip() else None)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("tool", nargs="?", default=None, help="工具名；传 --list-tools 枚举全部工具")
    ap.add_argument("args", nargs="?", default="{}")
    ap.add_argument("--base", default=DEFAULT_BASE)
    ap.add_argument("--max-chars", type=int, default=4000)
    ap.add_argument("--token-index", type=int, default=0,
                    help=".env AUTH_MAP 中第几个 token（不同 AppCode 订阅的接口可能不同）")
    ap.add_argument("--list-tools", action="store_true", help="枚举服务端全部工具及描述")
    a = ap.parse_args()

    token = load_token(index=a.token_index)
    sid, init = post(a.base, token, {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-03-26", "capabilities": {},
        "clientInfo": {"name": "mcp-test-client", "version": "0"}}})
    if init and "error" in init:
        print(json.dumps(init["error"], ensure_ascii=False))
        sys.exit(1)
    post(a.base, token, {"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)

    if a.list_tools:
        _, res = post(a.base, token, {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, sid)
        for t in (res.get("result", {}).get("tools") or []):
            desc = (t.get("description") or "").replace("\n", " ")
            if a.max_chars and len(desc) > a.max_chars:
                desc = desc[:a.max_chars] + "…"
            print(f"- {t.get('name')}: {desc}")
        return

    if not a.tool:
        raise SystemExit("请提供工具名或 --list-tools")


    try:
        arguments = json.loads(a.args)
    except json.JSONDecodeError as e:
        raise SystemExit(f"args 不是合法 JSON: {e}")

    sid, res = post(a.base, token, {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
                                    "params": {"name": a.tool, "arguments": arguments}}, sid)
    if res is None:
        print("(empty response)")
        sys.exit(1)
    if "error" in res:
        print("JSONRPC_ERROR: " + json.dumps(res["error"], ensure_ascii=False))
        sys.exit(1)

    out = []
    for c in res["result"].get("content", []):
        if c.get("type") == "text":
            out.append(c["text"])
    text = "\n".join(out)
    if a.max_chars and len(text) > a.max_chars:
        text = text[:a.max_chars] + f"\n…[截断，共 {len(text)} 字符]"
    print(text)


if __name__ == "__main__":
    main()
