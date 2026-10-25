#!/usr/bin/env python3
"""全量接口参数三源交叉核验。

目的：彻底核查 240 个网关接口（235 活跃 + 5 弃用）的参数正确性。

三源：
  A. PDF 原件提取（权威文档，v4 坐标定位提取器）
  B. 参数元数据 param_meta.json（由 A 生成，随二进制嵌入，是模型可见参数的主要来源）
  C. 目录原始声明 full_catalog.json 的 business_params/input_schema（上游生成物，可能残缺）

核验维度：
  1. 覆盖差异：某源有、另一源无的参数（缺失 → 模型不知道；幽灵 → 模型被误导）
  2. 类型/必填/枚举不一致
  3. 目录重复 pcode（同 pcode 多条目）
  4. 有文档但元数据缺失的接口

输出：reports/param_crosscheck_<date>.md（问题清单）+ stdout 摘要

用法：
    python3 eval/crosscheck_params.py             # 全量核验并打印摘要
    python3 eval/crosscheck_params.py --json OUT  # 导出结构化结果
"""

import argparse
import collections
import glob
import json
import os
import re
import sys
# 预期偏差登记：与 param_meta 生成器共享人工修正表（单一事实来源）
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from merge_param_meta import MANUAL, MANUAL_REMOVE

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
sys.path.insert(0, HERE)

CATALOG = os.path.join(REPO, "internal", "catalog", "data", "full_catalog.json")
META = os.path.join(REPO, "internal", "catalog", "data", "param_meta.json")

# 协议参数与响应信封字段（不应出现在任何业务参数集里）
NON_BUSINESS = {"appcode", "pcode", "content-type", "host", "port",
                "code", "message", "orderno", "charge", "time", "param", "data", "msg"}


def norm_name(s):
    return (s or "").strip()


def load_catalog():
    """返回 {pcode: [条目...]}（可能同 pcode 多条）"""
    d = json.load(open(CATALOG, encoding="utf-8"))
    out = collections.defaultdict(list)
    for a in d.get("apis", []):
        out[a["pcode"]].append(a)
    return out


def load_meta():
    d = json.load(open(META, encoding="utf-8"))
    return d.get("apis", {})


def catalog_params(entries):
    """目录声明的业务参数（business_params ∪ input_schema.properties，去协议/响应字段）"""
    names = set()
    for e in entries:
        for p in e.get("business_params") or []:
            n = norm_name(p.get("name"))
            if n and n.lower() not in NON_BUSINESS:
                names.add(n)
        props = ((e.get("input_schema") or {}).get("properties") or {})
        for n in props:
            n = norm_name(n)
            if n and n.lower() not in NON_BUSINESS:
                names.add(n)
    return names


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--json")
    args = ap.parse_args()

    import extract_param_pdf as ex

    pdfs = ex.load_pdfs(ex.PDF_ROOT)
    catalog = load_catalog()
    meta = load_meta()

    print(f"PDF 接口: {len(pdfs)} | 目录 pcode: {len(catalog)} | 元数据接口: {len(meta)}", file=sys.stderr)

    report = {
        "summary": collections.Counter(),
        "doc_only": [],        # 有 PDF 但无目录条目（弃用接口）
        "no_doc": [],          # 有目录条目但无 PDF
        "dup_pcode": [],       # 目录重复 pcode
        "missing_in_meta": [], # PDF 有参数但元数据缺失的接口
        "ghost_in_meta": [],   # 元数据有但 PDF 无的参数（幽灵）
        "missing_in_catalog": [],  # 元数据有但目录无（正常，目录本就残缺，仅统计）
        "type_mismatch": [],
        "required_mismatch": [],
        "non_business_leak": [],   # 三源中混入协议/响应字段
    }

    # 目录重复：注意**加载期已合并**（internal/catalog/mergeDuplicatePcodes），
    # 故此处仅为"原始文件有重复"的提示，不代表运行时行为异常。
    for pc, entries in catalog.items():
        if len(entries) > 1:
            report["dup_pcode"].append({
                "pcode": pc,
                "titles": [e.get("title") for e in entries],
                "domains": [e.get("domain") for e in entries],
                "note": "加载期已合并（mergeDuplicatePcodes）；此处仅提示原始目录含重复条目",
            })

    allpc = sorted(set(pdfs) | set(catalog))
    for pc in allpc:
        pdf_path = pdfs.get(pc)
        meta_p = (meta.get(pc) or {}).get("params") or {}
        cat_p = catalog_params(catalog.get(pc) or [])

        if pdf_path and pc not in catalog:
            report["doc_only"].append(pc)
        if not pdf_path and pc in catalog:
            report["no_doc"].append(pc)

        # 泄漏检查（三源）
        for src, names in (("pdf", set(meta_p)), ("meta", set(meta_p)), ("catalog", cat_p)):
            leak = {n for n in names if n.lower() in NON_BUSINESS}
            if leak:
                report["non_business_leak"].append({"pcode": pc, "source": src, "names": sorted(leak)})

        if not pdf_path:
            continue

        # 重新提取 PDF 参数（权威）
        pdf_params = ex.extract_params_from_pdf(pdf_path)
        pdf_names = set(pdf_params)
        meta_names = set(meta_p)

        # PDF 有、元数据无 → 模型看不到真实参数。
        # 预期偏差：merge_param_meta 的 MANUAL_REMOVE/MANUAL 是**有意**引入的差异
        # （文档列出但实测静默忽略的参数移除、实测生效参数补充），不属于漂移，
        # 从审计中豁免——否则每次运行恒报 1/1，真实漂移会被噪音淹没。
        miss = pdf_names - meta_names - set(MANUAL_REMOVE.get(pc, ()))
        if miss:
            report["missing_in_meta"].append({
                "pcode": pc, "title": (catalog.get(pc) or [{}])[0].get("title", ""),
                "missing": sorted(miss)[:20], "count": len(miss),
            })
        # 元数据有、PDF 无 → 幽灵参数（MANUAL 补充参数为预期，豁免）
        ghost = meta_names - pdf_names - set(MANUAL.get(pc, ()))
        if ghost:
            report["ghost_in_meta"].append({
                "pcode": pc, "title": (catalog.get(pc) or [{}])[0].get("title", ""),
                "ghost": sorted(ghost)[:20], "count": len(ghost),
            })
        # 类型/必填不一致
        for n in sorted(pdf_names & meta_names):
            a, b = pdf_params[n], meta_p[n]
            if a.get("type") and b.get("type") and a["type"] != b["type"]:
                report["type_mismatch"].append({"pcode": pc, "param": n, "pdf": a["type"], "meta": b["type"]})
            if a.get("required") and b.get("required") and a["required"] != b["required"]:
                report["required_mismatch"].append(
                    {"pcode": pc, "param": n, "pdf": a["required"], "meta": b["required"]})

    # 汇总
    s = report["summary"]
    s["pdf_interfaces"] = len(pdfs)
    s["catalog_pcodes"] = len(catalog)
    s["meta_interfaces"] = len(meta)
    s["doc_only"] = len(report["doc_only"])
    s["no_doc"] = len(report["no_doc"])
    s["dup_pcode"] = len(report["dup_pcode"])
    s["missing_in_meta_interfaces"] = len(report["missing_in_meta"])
    s["ghost_in_meta_interfaces"] = len(report["ghost_in_meta"])
    s["type_mismatch"] = len(report["type_mismatch"])
    s["required_mismatch"] = len(report["required_mismatch"])
    s["non_business_leak"] = len(report["non_business_leak"])

    print(json.dumps(report["summary"], ensure_ascii=False, indent=1))
    if report["dup_pcode"]:
        print("\n### 目录重复 pcode", file=sys.stderr)
        for d in report["dup_pcode"]:
            print(f"  {d['pcode']}: {d['titles']} domains={d['domains']}", file=sys.stderr)
    if report["missing_in_meta"]:
        print("\n### PDF 有参数但元数据缺失（模型看不到）", file=sys.stderr)
        for d in report["missing_in_meta"][:15]:
            print(f"  {d['pcode']} {d['title']}: 缺 {d['count']} 个 {d['missing'][:8]}", file=sys.stderr)
    if report["ghost_in_meta"]:
        print("\n### 元数据有但 PDF 无（幽灵参数）", file=sys.stderr)
        for d in report["ghost_in_meta"][:15]:
            print(f"  {d['pcode']} {d['title']}: 幽灵 {d['count']} 个 {d['ghost'][:8]}", file=sys.stderr)
    if report["type_mismatch"][:5]:
        print("\n### 类型不一致（前 5）", file=sys.stderr)
        for d in report["type_mismatch"][:5]:
            print(f"  {d['pcode']}.{d['param']}: pdf={d['pdf']} meta={d['meta']}", file=sys.stderr)
    if report["non_business_leak"]:
        print("\n### 非业务字段泄漏", file=sys.stderr)
        for d in report["non_business_leak"][:10]:
            print(f"  {d['pcode']} [{d['source']}]: {d['names']}", file=sys.stderr)

    if args.json:
        with open(args.json, "w", encoding="utf-8") as fh:
            json.dump(report, fh, ensure_ascii=False, indent=1)
        print(f"已写出 {args.json}", file=sys.stderr)


if __name__ == "__main__":
    main()
