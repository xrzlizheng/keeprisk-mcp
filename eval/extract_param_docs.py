#!/usr/bin/env python3
"""从接口原始文档（markdown，OCR 产物）提取参数元数据。

背景（2026-09-17）：此前参数结论靠"试探式实测"得出，导致多次误判
（searchKey 传字符串 500 就写"不可用"，实际它是对象；type=1 语义不符却当校友圈入口）。
原始文档（/data/home2/zli/接口文档pdf/markdown，242 篇）才是权威来源——
它包含目录完全没有的信息：**未列出的参数**（schoolName/labelList 等）、
**完整枚举值**（type 4 种、entStatusList 15 种）、**参数类型与必填性**。

本脚本按 pcode 提取：
  - parameters: [{name, type, required, desc, enum}]（来自参数表的 OCR 文本）
  - example_request: 请求示例 JSON（可靠）
  - response_fields: data 节点字段（模型理解响应结构用）

用法：
    python3 eval/extract_param_docs.py                 # 打印统计
    python3 eval/extract_param_docs.py --out FILE      # 输出 JSON 到文件
    python3 eval/extract_param_docs.py --pcode E_006_0021  # 查看单接口提取结果

输出供 `internal/catalog` 构建期合并（见 merge_param_meta.py）。
"""

import argparse
import collections
import glob
import json
import os
import re
import sys

DOC_ROOT = os.environ.get("KEEPRISK_DOC_ROOT", "/data/home2/zli/接口文档pdf/markdown")

# 参数类型关键词（OCR 文本中出现的类型标注）
TYPES = ("字符串", "数组", "对象", "数字", "Json", "json", "boolean", "Boolean")
REQ_MARKS = ("必填", "选填")

# 参数名：驼峰标识符（排除明显非参数的词）
PARAM_RE = re.compile(
    r"\b([a-z][a-zA-Z0-9_]{1,30})\s+"           # 参数名
    r"([^\n。]{0,40}?)\s*"                       # 说明（短）
    r"(字符串|数组|对象|数字|Json)\s*"             # 类型
    r"(必填|选填)"                                # 必填性
)

# 枚举：形如 "1. 高新企业" / "1 高新企业" / "## 2. 存续"
ENUM_RE = re.compile(r"(?:^|\n|\s)(\d{1,3})\s*[.、]\s*([^\n#|]{1,24})")


def load_docs(root):
    """返回 {pcode: (path, text)}"""
    out = {}
    for path in glob.glob(os.path.join(root, "**", "*.md"), recursive=True):
        m = re.search(r"\[([EP]_\d+_\d+)\]([^/\\]*)\.md$", path)
        if not m:
            continue
        out[m.group(1)] = (path, open(path, encoding="utf-8", errors="replace").read())
    return out


def extract_example(text):
    """提取「请求示例」后的 JSON（最可靠的信息源）"""
    m = re.search(r"###\s*请求示例\s*```json\s*(\{.*?\})\s*```", text, re.S)
    if not m:
        return None
    try:
        return json.loads(m.group(1))
    except json.JSONDecodeError:
        return None


def extract_param_section(text):
    """截取「接口请求参数」到「请求示例/接口响应信息」之间的参数说明段"""
    m = re.search(r"接口请求参数(.*?)(?:###\s*请求示例|###\s*接口响应信息|$)", text, re.S)
    return m.group(1) if m else ""


def extract_parameters(text):
    """从参数段提取参数名/类型/必填/说明 + 枚举值。

    关键：OCR 把参数与枚举挤在一起，枚举窗口必须严格限定在
    "本参数位置 → 下一参数位置"之间，否则会把后续参数的枚举误挂到前面。
    """
    section = extract_param_section(text)
    if not section:
        return []
    # 剔除 markdown 标题与表格分隔，降低噪音；但保留正文顺序用于定位
    cleaned = re.sub(r"^#{1,6}\s*.*$", "\n", section, flags=re.M)
    cleaned = re.sub(r"^\|.*$", "\n", cleaned, flags=re.M)
    cleaned = re.sub(r"\n{2,}", "\n", cleaned)

    hits = []
    for m in PARAM_RE.finditer(cleaned):
        name, desc, typ, req = m.group(1), m.group(2).strip(), m.group(3), m.group(4)
        if name in ("http", "json", "https", "post", "host", "port", "body", "data",
                    "header", "AppCode", "Content", "Type"):
            continue
        hits.append({"name": name, "desc": desc[:60], "type": typ,
                     "required": req, "pos": m.start(), "end": m.end()})
    if not hits:
        return []

    # 去重（同名保留首次出现），并按位置排序以便切窗口
    seen, ordered = set(), []
    for h in sorted(hits, key=lambda x: x["pos"]):
        if h["name"] in seen:
            continue
        seen.add(h["name"])
        ordered.append(h)

    for i, h in enumerate(ordered):
        stop = ordered[i + 1]["pos"] if i + 1 < len(ordered) else len(cleaned)
        window = cleaned[h["end"]:stop]
        h["enum"] = extract_enum_in(window)
    for h in ordered:
        h.pop("pos", None)
        h.pop("end", None)
    return ordered


def extract_enum_in(window):
    """在给定窗口内提取编号枚举值（1=xxx 形式）"""
    vals = []
    for m in ENUM_RE.finditer("\n" + window):
        num, label = m.group(1), m.group(2).strip()
        label = re.sub(r"(字符串|数组|对象|数字)?\s*(选填|必填).*$", "", label).strip()
        if not label or len(label) > 24:
            continue
        vals.append(f"{num}={label}")
        if len(vals) >= 20:
            break
    return vals


def extract_response_fields(text):
    """提取 data 节点字段（模型判断响应结构用）"""
    m = re.search(r"data\s*数据信息节点说明(.*?)(?:###|$)", text, re.S)
    if not m:
        return []
    body = m.group(1)
    fields = re.findall(r"\b([a-z][a-zA-Z0-9_]{2,30})\s+([\u4e00-\u9fa5][^\s,，。]{1,20})", body)
    out, seen = [], set()
    for name, desc in fields:
        if name in seen or name in ("json", "http", "pcode"):
            continue
        seen.add(name)
        out.append({"name": name, "desc": desc[:30]})
        if len(out) >= 40:
            break
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=DOC_ROOT)
    ap.add_argument("--out")
    ap.add_argument("--pcode")
    args = ap.parse_args()

    if not os.path.isdir(args.root):
        raise SystemExit(f"文档目录不存在: {args.root}（可用 KEEPRISK_DOC_ROOT 覆盖）")

    docs = load_docs(args.root)
    print(f"扫描到 {len(docs)} 篇接口文档（{args.root}）", file=sys.stderr)

    meta = {}
    stat = collections.Counter()
    for pcode, (path, text) in sorted(docs.items()):
        params = extract_parameters(text)
        for p in params:
            if p.get("enum"):
                stat["params_with_enum"] += 1
        ex = extract_example(text)
        meta[pcode] = {
            "pcode": pcode,
            "doc_path": os.path.relpath(path, args.root),
            "parameters": params,
            "example_request_doc": ex,
            "response_fields": extract_response_fields(text),
        }
        stat["docs"] += 1
        stat["params"] += len(params)
        if ex:
            stat["with_example"] += 1
        if params:
            stat["with_params"] += 1

    if args.pcode:
        m = meta.get(args.pcode)
        print(json.dumps(m, ensure_ascii=False, indent=2) if m else f"未找到 {args.pcode}")
        return

    print(f"提取统计: {json.dumps(stat, ensure_ascii=False)}", file=sys.stderr)
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            json.dump(meta, fh, ensure_ascii=False, indent=1)
        print(f"已写出 {args.out}", file=sys.stderr)
    else:
        # 抽样展示
        for pc in list(meta)[:2]:
            print(json.dumps(meta[pc], ensure_ascii=False, indent=1)[:1200])


if __name__ == "__main__":
    main()
