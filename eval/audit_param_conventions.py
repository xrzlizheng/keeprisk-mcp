#!/usr/bin/env python3
"""全量网关接口入参约定审计。

用途：系统性地发现 236 个接口的入参格式冲突——而非只修已观察到问题的接口。

审计项：
  1. 区划参数容器格式分类（扁平 / regionList 数组 / region 对象 / 逗号多选 / regionMap）
  2. 分页参数命名一致性（pageNo/pageSize vs page/pageIndex）
  3. 主体字段名分布（entName/companyName/name/text/keyword/chainHis...）
  4. 元数据自洽性（example 键是否在 properties、required_one_of 是否可满足）
  5. 脏写键名（带空格/大小写错乱，如 ' companyname '）
  6. 单例参数（仅 1 个接口使用，可能是未文档化的一次性约定）

用法：
    python3 eval/audit_param_conventions.py            # 全量审计并打印
    python3 eval/audit_param_conventions.py --json     # 输出 JSON（供其他工具消费）

输出即"参数约定地图"，是 paramrules.go 规则的来源；每条规则的判定都要经线上实测。
"""

import argparse
import collections
import json
import os
import sys

CATALOG = os.path.join(os.path.dirname(__file__), "..", "internal", "catalog", "data", "full_catalog.json")

REGION_FLAT = ("province", "city", "county")
REGION_CONTAINERS = ("regionList", "region", "regionMap", "economicRegion", "excludeRegionList")
PAGINATION = ("pageNo", "pageSize", "page", "pageIndex", "limit", "offset", "number")
SUBJECT_HINT = ("entName", "companyName", "name", "text", "keyword", "chainHis", "chainName",
                "personName", "creditCode", "companyId", "id", "secucode")


def load():
    with open(CATALOG, encoding="utf-8") as fh:
        data = json.load(fh)
    return [it for it in data.get("apis", []) if it.get("is_latest", True)]


def params_of(api):
    """接口的全部参数名（schema.properties ∪ business_params ∪ example ∪ required_one_of）"""
    sc = api.get("input_schema") or {}
    names = set((sc.get("properties") or {}).keys())
    names |= {p["name"] for p in (api.get("business_params") or []) if p.get("name")}
    names |= set((api.get("example_request") or {}).keys())
    for g in api.get("required_one_of") or []:
        names |= set(g if isinstance(g, list) else [g])
    return {n.strip() for n in names if n and n.strip()}


def region_convention(api, params):
    """判定接口的区划容器约定（静态，基于声明；实际生效以线上实测为准）"""
    kinds = []
    if "regionList" in params:
        kinds.append("regionList数组")
    if "region" in params:
        kinds.append("region对象")
    if "regionMap" in params:
        kinds.append("regionMap")
    if any(k in params for k in REGION_FLAT):
        kinds.append("扁平province/city/county")
    if "economicRegion" in params:
        kinds.append("economicRegion")
    if not kinds:
        return None
    # 混合声明（同接口既有容器又有扁平）→ 高风险：模型不知该用哪个
    has_container = any(k in params for k in ("regionList", "region", "regionMap"))
    has_flat = any(k in params for k in REGION_FLAT)
    return {"kinds": kinds, "mixed": has_container and has_flat}


def example_consistency(api, params):
    """example 的键是否都在 properties 中（脏写/未声明参数检测）"""
    props = set(((api.get("input_schema") or {}).get("properties") or {}).keys())
    if not props:
        return []
    ex = api.get("example_request") or {}
    return [k for k in ex if k not in props]


def dirty_keys(names):
    """带首尾空格或全小写驼峰混排的脏写键"""
    out = []
    for n in names:
        if n != n.strip():
            out.append((n, "首尾空格"))
        elif n != n.strip() and n.lower() in {x.lower() for x in names}:
            out.append((n, "大小写重复"))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--json", action="store_true", help="以 JSON 输出")
    args = ap.parse_args()

    apis = load()
    report = {
        "total_apis": len(apis),
        "region_conventions": collections.defaultdict(list),
        "region_mixed": [],
        "pagination_odd": [],
        "example_unregistered": [],
        "dirty_keys": [],
        "singleton_params": [],
    }

    freq = collections.Counter()
    lazy = 0

    for api in apis:
        ps = params_of(api)
        lazy += 1
        freq.update(ps)

        rc = region_convention(api, ps)
        if rc:
            key = "+".join(rc["kinds"])
            report["region_conventions"][key].append(api["pcode"])
            if rc["mixed"]:
                report["region_mixed"].append({"pcode": api["pcode"], "title": api["title"], "kinds": rc["kinds"]})

        # 分页：声明了 page/pageIndex 而无 pageNo（命名不一致）
        if any(k in ps for k in ("page", "pageIndex")) and "pageNo" not in ps:
            report["pagination_odd"].append({"pcode": api["pcode"], "title": api["title"],
                                             "params": sorted(ps & set(PAGINATION))})

        # example 引用了未声明的参数
        unreg = example_consistency(api, ps)
        if unreg:
            report["example_unregistered"].append({"pcode": api["pcode"], "title": api["title"], "keys": unreg})

        # 脏写键
        for name, why in dirty_keys(ps):
            report["dirty_keys"].append({"pcode": api["pcode"], "key": name, "why": why})

    # 单例参数（仅 1 个接口使用）——通常是未文档化的一次性约定
    for name, n in freq.most_common():
        if n == 1:
            report["singleton_params"].append(name)
    report["singleton_params"] = report["singleton_params"][:40]
    report["param_frequency_top"] = freq.most_common(40)
    report["region_conventions"] = dict(report["region_conventions"])

    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return

    print(f"最新版接口数: {report['total_apis']}\n")
    print("=" * 74)
    print("1. 区划容器约定分布（静态声明）")
    print("=" * 74)
    for kind, pcodes in sorted(report["region_conventions"].items(), key=lambda x: -len(x[1])):
        print(f"  {kind:42s} {len(pcodes):3d} 个")
    print()
    print(f"  ⚠️ 混合声明（同时含容器与扁平 → 模型不知用哪个）: {len(report['region_mixed'])} 个")
    for m in report["region_mixed"][:10]:
        print(f"     {m['pcode']} {m['title']}: {m['kinds']}")

    print()
    print("=" * 74)
    print("2. 分页命名不一致（声明 page/pageIndex 而无 pageNo）")
    print("=" * 74)
    if not report["pagination_odd"]:
        print("  无（分页命名统一为 pageNo/pageSize）")
    for p in report["pagination_odd"][:15]:
        print(f"  {p['pcode']} {p['title']}: {p['params']}")

    print()
    print("=" * 74)
    print("3. example 引用未声明参数（模型照抄即错）")
    print("=" * 74)
    print(f"  共 {len(report['example_unregistered'])} 个")
    for e in report["example_unregistered"][:15]:
        print(f"  {e['pcode']} {e['title']}: {e['keys']}")

    print()
    print("=" * 74)
    print("4. 脏写键名（首尾空格/大小写错乱）")
    print("=" * 74)
    print(f"  共 {len(report['dirty_keys'])} 处")
    for d in report["dirty_keys"][:10]:
        print(f"  {d['pcode']}: {d['key']!r} ({d['why']})")

    print()
    print("=" * 74)
    print("5. 单例参数（仅 1 个接口使用，可能是一次性约定）")
    print("=" * 74)
    print(f"  {len(report['singleton_params'])} 个: {report['singleton_params'][:20]}")

    print()
    print("=" * 74)
    print("6. 参数频次 Top 20（跨接口通用性）")
    print("=" * 74)
    for name, n in report["param_frequency_top"][:20]:
        print(f"  {n:4d}  {name}")


if __name__ == "__main__":
    main()
