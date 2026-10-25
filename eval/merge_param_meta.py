#!/usr/bin/env python3
"""生成参数元数据（param_meta.json），供 catalog 构建期合并。

数据来源优先级（2026-09-17 校正）：
  1. **接口文档 PDF 原件**（权威，PyMuPDF 表格提取）——覆盖率 1010 参数/240 接口，
     含参数名/说明/类型/必填/枚举，且请求示例为真实 JSON
  2. OCR markdown（补充，仅在 PDF 缺失或提取失败时回退）
  3. 人工校正表（MANUAL，兜底与订正）

> 为何改用 PDF：OCR markdown 把参数表拍扁成一行（"参数名称 参数说明参数类型说明" 全挤一起），
> 提取覆盖率仅 67/1104（6%）；PDF 有真实文本层与表格结构，覆盖率 1010/1104（91%），
> 并首次拿到完整枚举值（labelList 14 种科技资质等）与 106 个接口的未列出参数。

输出结构（internal/catalog/data/param_meta.json）：
{
  "meta": {"source": "...", "count": N, "stats": {...}},
  "apis": {
    "E_006_0021": {
      "example_request": {...},       # 权威示例（含主体参数，修复目录示例残缺）
      "params": {"<name>": {"desc","type","required","enum"}},
      "doc_path": "定制/信易知/校企/[E_006_0021]校企筛选V2.pdf"
    }
  }
}

用法：
    python3 eval/merge_param_meta.py            # 生成并写入仓库
    python3 eval/merge_param_meta.py --check    # 仅校验（供 CI/定时任务）
"""

import argparse
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
OUT_PATH = os.path.join(REPO, "internal", "catalog", "data", "param_meta.json")

# 文档示例本身可疑/与目录一致但语义不明的接口：不覆盖目录示例
EXEMPT_EXAMPLE = {
    # 文档示例与目录完全相同，无新增价值；保留目录示例避免无谓变更
    "E_007_1221", "E_007_1421",
}

# 人工移除：文档参数表列出、但网关实测**静默忽略**的参数（接受不报错、不参与过滤）。
# 保留会诱导模型照传拿到"看似成功、实际未过滤"的结果——比报错更危险。
MANUAL_REMOVE = {
    # E_007_1441 政策列表V2 的 typeList：文档参数表列名。2026-09-22 apitest 实测：
    # typeList=["申报通知"] 返回混合类型（total=1615，未过滤）；同值传 policyType
    # 全量过滤（total=169，全部申报通知）——生效参数为 policyType（文档请求示例正确，
    # 参数表是 V1 模板残留）。此处移除 typeList，policyType 见 MANUAL。
    "E_007_1441": ["typeList"],
}

# 人工校正：OCR 提取错误或缺失，但已由原始文档人工确认的参数
MANUAL = {
    # 政策列表V2：policyType 实测生效（见 MANUAL_REMOVE 注），枚举取自文档参数表
    "E_007_1441": {
        "policyType": {"type": "数组", "required": "选填", "desc": "政策类型，支持多选",
                       "enum": ["法律法规", "申报通知", "财税法规", "立项公示",
                                "政策资讯", "产业规划", "奖励文件"]},
    },
    # 政策详情V2：id 即政策列表返回的 uid（2026-09-22 实测同值往返返回同一条政策；
    # 列表项只有 uid 字段、无 id 字段。参数名写 uid/policyId 会被网关判 101）
    "E_007_1451": {
        "id": {"type": "字符串", "required": "必填",
               "desc": "政策主键id —— 即政策列表返回的 uid（同一 32 位主键值，两处字段名不同）",
               "note": "取政策列表返回的 uid；或用详情响应 dataList 的 id 继续级联。"
                       "勿自行构造（构造的 id 返回 110 无数据，不计费）"},
    },
    "E_006_0021": {
        "schoolName": {"type": "字符串", "required": "选填",
                       "desc": "学校名称，模糊搜索，空格为且、逗号为或"},
        "searchKey": {"type": "对象", "required": "选填",
                      "desc": '搜索关键词对象 {type,text}；type 可用空格组合：1=企业名称【模糊搜索】 2=校友名称【精确搜索】 3=法人名称【精确搜索】 4=学校名称/学校简称【精确搜索】'},
        "labelList": {"type": "数组", "required": "选填", "desc": "科技资质，支持多选",
                      "enum": ["1=高新企业", "2=科技型中小企业", "3=瞪羚企业", "4=独角兽企业",
                               "5=技术创新示范企业", "6=专精特新中小企业", "7=企业技术中心",
                               "8=科技小巨人企业", "9=雏鹰企业", "10=众创空间",
                               "11=专精特新小巨人企业", "12=科技企业孵化器", "13=隐形冠军企业",
                               "14=技术先进型服务企业"]},
        "roundList": {"type": "数组", "required": "选填", "desc": "融资轮次，支持多选",
                      "enum": ["1=天使/种子轮", "2=Pre-A 至 A+轮", "3=Pre-B 至 B+轮",
                               "4=Pre-C 至 C+轮", "5=Pre-D 至 D+轮", "6=E 轮及以上",
                               "7=IPO/新三板", "8=定向增发", "9=收购/并购", "10=战略投融资",
                               "11=股权融资/转让", "12=其他", "13=未融资"]},
        "scaleList": {"type": "数组", "required": "选填", "desc": "企业规模，支持多选",
                      "enum": ["1=大型", "2=中型", "3=小型", "4=微型"]},
        "entStatusList": {"type": "数组", "required": "选填", "desc": "企业状态，支持多选",
                          "enum": ["1=在业/开业/在营/经营", "2=存续", "3=筹建", "4=清算",
                                   "5=迁入", "6=迁出", "7=停业", "8=撤销", "9=吊销",
                                   "10=注销", "11=歇业", "12=责令关闭", "13=除名", "15=其他"]},
        "listedList": {"type": "数组", "required": "选填", "desc": "上市情况，支持多选",
                       "enum": ["0=新三板", "1=H 股", "2=A股", "3=上交所主板",
                                "4=上交所科创板", "5=深交所主板", "6=深交所创业板", "7=北交所"]},
    },
}


# 协议参数（HTTP 请求头/传输层字段）：不是业务参数，提取时必须过滤
PROTOCOL_PARAMS = {
    "pcode", "AppCode", "appcode", "Content", "content", "Type", "type",
    "http", "https", "host", "port", "post", "body", "header", "json",
    "application", "charset", "utf",
}


def load_extracted():
    """PDF 优先（权威），OCR markdown 回退。"""
    sys.path.insert(0, HERE)
    out = {}

    # 1) PDF 主源
    try:
        import extract_param_pdf as pdfex

        pdfs = pdfex.load_pdfs(pdfex.PDF_ROOT)
        for pcode, path in sorted(pdfs.items()):
            params = pdfex.extract_params_from_pdf(path)
            out[pcode] = {
                "example_request": pdfex.extract_example(path),
                "params": params,
                # 必填其一组（"与 X 必填其一"）：合并进目录 required_one_of 的补充来源
                "required_groups": pdfex.extract_required_groups(path),
                "doc_path": os.path.relpath(path, pdfex.PDF_ROOT),
                "source": "pdf",
            }
    except Exception as e:  # PDF 源不可用时不阻断（回退 OCR）
        print(f"WARN: PDF 提取失败，回退 OCR markdown: {e}", file=sys.stderr)

    # 2) OCR markdown 补充：仅填补 PDF 未覆盖的接口
    try:
        import extract_param_docs as mdex

        docs = mdex.load_docs(mdex.DOC_ROOT)
        for pcode, (path, text) in sorted(docs.items()):
            if pcode in out and out[pcode].get("params"):
                continue  # PDF 已有，不覆盖
            pm = {}
            for p in mdex.extract_parameters(text):
                name = p["name"]
                if name in PROTOCOL_PARAMS or name.startswith("dd") or len(name) < 3:
                    continue
                pm[name] = {k: v for k, v in p.items() if k != "name" and v}
            out.setdefault(pcode, {
                "example_request": mdex.extract_example(text),
                "params": pm,
                "doc_path": os.path.relpath(path, mdex.DOC_ROOT),
                "source": "markdown",
            })
    except Exception as e:
        print(f"WARN: OCR markdown 提取失败: {e}", file=sys.stderr)

    return out


def build():
    extracted = load_extracted()
    apis = {}
    for pcode, v in extracted.items():
        ex_example = None if pcode in EXEMPT_EXAMPLE else v.get("example_request")
        params = dict(v.get("params") or {})
        params.update(MANUAL.get(pcode, {}))  # 人工校正优先
        for k in MANUAL_REMOVE.get(pcode, ()):  # 实测静默忽略的参数：移除防误导
            params.pop(k, None)
        entry = {}
        if ex_example:
            entry["example_request"] = ex_example
        if params:
            entry["params"] = params
        if v.get("doc_path"):
            entry["doc_path"] = v["doc_path"]
        if v.get("source"):
            entry["source"] = v["source"]
        if v.get("required_groups"):
            entry["required_groups"] = v["required_groups"]
        if entry:
            apis[pcode] = entry

    from_pdf = sum(1 for v in apis.values() if v.get("source") == "pdf")
    stats = {
        "apis": len(apis),
        "from_pdf": from_pdf,
        "from_markdown": len(apis) - from_pdf,
        "with_example": sum(1 for v in apis.values() if v.get("example_request")),
        "with_params": sum(1 for v in apis.values() if v.get("params")),
        "params_total": sum(len(v.get("params") or {}) for v in apis.values()),
        "params_with_enum": sum(
            1 for v in apis.values() for p in (v.get("params") or {}).values() if p.get("enum")),
        "params_with_required": sum(
            1 for v in apis.values() for p in (v.get("params") or {}).values() if p.get("required")),
        "apis_with_required_groups": sum(1 for v in apis.values() if v.get("required_groups")),
    }
    return {
        "meta": {
            "source": "接口文档 PDF 原件（PyMuPDF 表格提取）；OCR markdown 作回退",
            "pdf_root": os.environ.get("KEEPRISK_PDF_ROOT", "/data/home2/zli/接口文档pdf"),
            "generator": "eval/merge_param_meta.py + eval/extract_param_pdf.py",
            "count": len(apis),
            "stats": stats,
            "note": "example_request 为文档权威示例（含主体参数，修复目录示例残缺）；"
                    "params 为 PDF 参数表提取（含类型/必填/枚举），补充目录未列出的参数",
        },
        "apis": apis,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="仅校验不写盘")
    args = ap.parse_args()

    data = build()
    stats = {
        "apis": len(data["apis"]),
        "with_example": sum(1 for v in data["apis"].values() if v.get("example_request")),
        "with_params": sum(1 for v in data["apis"].values() if v.get("params")),
        "params_total": sum(len(v.get("params") or {}) for v in data["apis"].values()),
    }
    print(f"参数元数据: {json.dumps(stats, ensure_ascii=False)}", file=sys.stderr)

    if args.check:
        if not os.path.exists(OUT_PATH):
            print("FAIL: param_meta.json 不存在，请运行 python3 eval/merge_param_meta.py", file=sys.stderr)
            sys.exit(1)
        print("OK: 已存在 param_meta.json", file=sys.stderr)
        return

    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    with open(OUT_PATH, "w", encoding="utf-8") as fh:
        json.dump(data, fh, ensure_ascii=False, indent=1)
    print(f"已写入 {OUT_PATH}", file=sys.stderr)


if __name__ == "__main__":
    main()
