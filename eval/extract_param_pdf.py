#!/usr/bin/env python3
"""从接口文档 **PDF 原件** 提取参数表（权威、结构化）。

为什么用 PDF 而不是 markdown：markdown 是 OCR 产物，参数表被拍扁成一行
（"参数名称 参数说明参数类型说明" 全挤在一起），导致提取覆盖率仅 6%。
PDF 有**真实文本层**且保留表格结构，PyMuPDF 可直接还原 4 列：
    参数名称 | 参数说明 | 参数类型 | 说明
覆盖率可达 ~100%（详见 --stats）。

用法：
    python3 eval/extract_param_pdf.py --stats                 # 覆盖率统计
    python3 eval/extract_param_pdf.py --pcode E_006_0021      # 单接口详情
    python3 eval/extract_param_pdf.py --out /tmp/pdf_params.json  # 全量导出

产出结构（每 pcode）：
{
  "params": {"<name>": {"desc","type","required","enum":[...],"nested":bool}},
  "example_request": {...},          # 从 PDF 文本提取的请求示例（JSON）
  "doc_path": "定制/信易知/校企/[E_006_0021]校企筛选V2.pdf"
}
"""

import argparse
import collections
import glob
import json
import os
import re
import sys

import fitz  # PyMuPDF

PDF_ROOT = os.environ.get("KEEPRISK_PDF_ROOT", "/data/home2/zli/接口文档pdf")

# 协议参数（HTTP 头），非业务参数
PROTOCOL = {"appcode", "pcode", "content-type", "content type", "pcode ", "host", "port"}

# 响应信封字段（响应信息表内容，非业务参数）：即便表格被拆散误判，也二次拦截
RESPONSE_ENVELOPE = {"code", "message", "orderno", "charge", "time", "param", "data", "msg"}

# 表格表头识别
HEADER_KEYS = ("参数名称", "参数说明")

# 说明列里的枚举："1.企业名称【模糊搜索】" / "1、高新企业"
ENUM_RE = re.compile(r"(\d{1,3})\s*[.、]\s*([^\d\n]{1,30}?)(?=\s*\d{1,3}\s*[.、]|$)")


def norm(s):
    return re.sub(r"\s+", " ", (s or "").replace("\n", " ")).strip()


def is_section_row(cells):
    """分节行：如「协议参数（此参数放置在 header 请求头中）」「业务参数（JSON 请求，放置在 body 中）」"""
    joined = norm(" ".join(cells))
    if not joined:
        return True
    if "协议参数" in joined or "业务参数" in joined:
        return True
    return all(not norm(c) for c in cells[1:])


def classify_required(desc_col, note_col):
    """判定必填性。

    ⚠️ 两个顺序陷阱（均经交叉验证发现并修正）：
      1. 文档存在"非必填"这类否定式，若先匹配"必填"会把 84 条误判为必填；
      2. 文档存在"选填，X 和 Y **必填一个**"（必填其一/必填其中之一）这类**条件必填组**，
         若按"含必填即必填"判定，会让 95 个参数同时显示"必填；选填…"（自相矛盾），
         且本地校验偏严、提示失真。这类参数**单个是可选的**，仅"至少传一个"。
    """
    text = f"{desc_col} {note_col}"
    # 1. 否定式优先
    if "非必填" in text or "不必填" in text or "不是必填" in text:
        return "选填"
    # 2. 条件必填组（必填其一/必填一个/必填其中之一/必填其一者）→ 单参数属选填，
    #    组语义由 required_one_of（catalog）或文档说明承担
    for marker in ("必填其中之一", "必填其一", "必填一个", "必填其一者", "至少填一个", "至少传一个"):
        if marker in text:
            return "选填"
    if "必填" in text:
        return "必填"
    if "选填" in text:
        return "选填"
    return ""


def extract_enum(note_col):
    """从说明列提取枚举值（1=xxx 形式）"""
    note = norm(note_col)
    vals = []
    for m in ENUM_RE.finditer(note):
        num, label = m.group(1), m.group(2).strip()
        if not label or len(label) > 26:
            continue
        vals.append(f"{num}={label}")
    return vals


def extract_params_from_pdf(path):
    """返回 {name: {desc,type,required,note,enum}}。

    识别策略（2026-09-17 四次校正；前三版均被交叉验证证伪）：
      v1 "≥4 列即请求表"     → 响应字段混入（E_011_0251 混入 7 个信封字段）
      v2 "表头含参数类型"     → 跨页续表整批丢弃（E_007_1131 47→2 个参数）
      v3 "章节状态机（行级）" → 未处理"标记在表外的正文里"，读到响应区（E_007_1131 涨到 169 个）
      v4（本版）**坐标定位**：用 PyMuPDF 的 search_for 取"业务参数"起始标记与
         结束标记（请求示例/接口响应信息/data 节点说明）在页面上的 **y 坐标**，
         只提取该 y 区间内的表格行。坐标不依赖 OCR 文字版式，是唯一可靠判据。
    """
    params = {}
    last_enum_key = None
    try:
        doc = fitz.open(path)
    except Exception:
        return params

    START_MARKERS = ("业务参数",)
    END_MARKERS = ("请求示例", "接口响应信息", "返回示例",
                   "data 数据信息节点说明", "data1 数据信息节点说明", "data2 数据信息节点说明")

    def marker_ys(page, markers):
        ys = []
        for m in markers:
            try:
                for r in page.search_for(m):
                    ys.append(r.y0)
            except Exception:
                continue
        return ys

    in_business = False
    closed = False  # 遇到结束标记后永久关闭（2026-09-17 三次校正：曾因"跨页状态不复位"
    # 让第 8-11 页的响应字段（如 secumarket）继续混入——结束标记在第 7 页、
    # 后续页无任何标记，in_business 仍为 true 导致污染）
    for page in doc:
        if closed:
            break
        start_ys = marker_ys(page, START_MARKERS)
        end_ys = marker_ys(page, END_MARKERS)
        if start_ys:
            in_business = True
        y_lo = min(start_ys) if start_ys else None
        y_hi = min(end_ys) if end_ys else None
        # 同页同时出现起止标记时，结束标记通常在上方（响应区）；取更靠下的起点
        if y_lo is not None and y_hi is not None and y_hi < y_lo:
            y_lo = None
        # 本页只有结束标记（无起始标记）→ 请求参数区到此结束，后续页不再收集
        if y_hi is not None and y_lo is None:
            closed = True
        if not in_business:
            continue
        try:
            tabs = page.find_tables()
        except Exception:
            tabs = None
        tables = list(tabs.tables) if tabs else []
        for t in tables:
            try:
                tbbox = t.bbox
                rows = t.extract()
            except Exception:
                continue
            if not rows:
                continue
            # 表整体在起始标记上方（协议区）→ 跳过
            if y_lo is not None and tbbox[3] <= y_lo:
                continue
            # 表整体在结束标记下方（响应区）→ 跳过
            if y_hi is not None and tbbox[1] >= y_hi:
                continue
            # 行级坐标：定位每行相对于起止标记的位置（PyMuPDF 的 row bbox 需按比例估算）
            n_rows = len(rows)
            row_h = (tbbox[3] - tbbox[1]) / n_rows if n_rows else 0
            for ri, cells in enumerate(rows):
                row_top = tbbox[1] + ri * row_h
                row_bottom = row_top + row_h
                # 行在业务参数起始标记之前 → 跳过
                if y_lo is not None and row_bottom <= y_lo:
                    continue
                # 行在结束标记之后 → 跳过（并停止本页后续行）
                if y_hi is not None and row_top >= y_hi:
                    break
                if len(cells) < 2:
                    continue
                joined = norm(" ".join(str(c or "") for c in cells))
                if any(m in joined for m in START_MARKERS):
                    in_business = True
                    continue
                if any(m in joined for m in END_MARKERS):
                    break
                if not in_business:
                    continue
                if "协议参数" in joined:
                    continue
                name = norm(cells[0])
                # 续行（首列为空）：枚举值接在上一参数后（跨页/跨行枚举）
                if not name:
                    if last_enum_key:
                        enum = extract_enum(joined)
                        if enum:
                            prev = params.get(last_enum_key, {})
                            have = set(prev.get("enum") or [])
                            merged = list(prev.get("enum") or [])
                            for v in enum:
                                if v not in have:
                                    merged.append(v)
                            prev["enum"] = merged
                            params[last_enum_key] = prev
                    continue
                if name in ("参数名称",):
                    continue
                key = name.strip()
                if key.lower() in PROTOCOL or key.lower() in RESPONSE_ENVELOPE:
                    continue
                desc = norm(cells[1]) if len(cells) > 1 else ""
                typ = ""
                note = ""
                for c in cells[2:]:
                    cc = norm(c)
                    if not cc:
                        continue
                    if cc in ("字符串", "数组", "对象", "数字", "Json", "json") and not typ:
                        typ = cc
                    else:
                        note = (note + " " + cc).strip()
                if not typ:
                    m = re.search(r"(字符串|数组|对象|数字)", joined)
                    if m:
                        typ = m.group(1)
                if not re.match(r"^[A-Za-z][A-Za-z0-9_]*$", key):
                    continue  # 非参数名（说明文字误入）
                enum = extract_enum(note) or extract_enum(joined)
                # ⚠️ 2026-09-17 四处校正：**不做"缺类型/必填标记就丢弃"的启发式**——
                # 该守卫曾误杀 58 个 PDF 真实请求参数/18 接口（如 E_007_1331 的 pageSize：
                # 文档行内容为 ['pageSize','每页条数','默认10 条']，无类型列无必填标记）。
                # 请求区判据以**坐标定位**为准（进入本分支即已在业务参数区），
                # 响应区由 y 坐标 + 结束标记双重排除，无需再靠行内标记猜测。
                entry = {
                    "desc": desc,
                    "type": typ,
                    "required": classify_required(desc, note),
                }
                if note:
                    entry["note"] = note
                enum = extract_enum(note) or extract_enum(joined)
                if enum:
                    entry["enum"] = enum
                    last_enum_key = key
                prev = params.get(key)
                if prev:
                    for k, v in entry.items():
                        if v and not prev.get(k):
                            prev[k] = v
                else:
                    params[key] = entry
    return params


def extract_example(path):
    """从 PDF 文本提取「请求示例」后的 JSON。

    PDF 版式：`请求示例：\n{\n"searchKey": {...}...\n}\n接口响应信息：`
    注意是全角冒号「：」；且 JSON 内可能含换行与中文引号。
    """
    try:
        doc = fitz.open(path)
    except Exception:
        return None
    text = "\n".join(page.get_text() for page in doc)
    m = re.search(r"请求示例[：:]\s*(\{.*?\})\s*(?:\n|接口响应信息|响应信息|$)", text, re.S)
    if not m:
        return None
    cand = m.group(1)
    for attempt in (cand,
                    cand.replace("\u201c", '"').replace("\u201d", '"'),
                    re.sub(r"[\u4e00-\u9fff]", "", cand)):
        try:
            return json.loads(attempt)
        except json.JSONDecodeError:
            continue
    # 括号配平截取第一个完整 JSON 对象
    depth = 0
    for i, ch in enumerate(cand):
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                try:
                    return json.loads(cand[: i + 1])
                except json.JSONDecodeError:
                    return None
    return None


def load_pdfs(root):
    out = {}
    for path in glob.glob(os.path.join(root, "**", "*.pdf"), recursive=True):
        m = re.search(r"\[([EP]_\d+_\d+)\]", os.path.basename(path))
        if m:
            out[m.group(1)] = path
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=PDF_ROOT)
    ap.add_argument("--out")
    ap.add_argument("--pcode")
    ap.add_argument("--stats", action="store_true")
    args = ap.parse_args()

    pdfs = load_pdfs(args.root)
    print(f"扫描到 {len(pdfs)} 份 PDF（{args.root}）", file=sys.stderr)

    if args.pcode:
        p = pdfs.get(args.pcode)
        if not p:
            raise SystemExit(f"未找到 {args.pcode} 的 PDF")
        print(json.dumps({
            "pdf": p,
            "params": extract_params_from_pdf(p),
            "example": extract_example(p),
        }, ensure_ascii=False, indent=2))
        return

    meta, stat = {}, collections.Counter()
    for pcode, path in sorted(pdfs.items()):
        params = extract_params_from_pdf(path)
        ex = extract_example(path)
        meta[pcode] = {"params": params, "example_request": ex,
                       "doc_path": os.path.relpath(path, args.root)}
        stat["docs"] += 1
        stat["params"] += len(params)
        stat["with_params"] += 1 if params else 0
        stat["with_example"] += 1 if ex else 0
        stat["with_enum"] += sum(1 for v in params.values() if v.get("enum"))
        stat["with_required"] += sum(1 for v in params.values() if v.get("required"))
        stat["with_type"] += sum(1 for v in params.values() if v.get("type"))

    if args.stats or not args.out:
        print("提取统计:", json.dumps(stat, ensure_ascii=False, indent=1))
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            json.dump(meta, fh, ensure_ascii=False, indent=1)
        print(f"已写出 {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()


# ───────────── 必填组（required_one_of）提取 ─────────────
# 文档措辞："选填，与 companyId 必填其一" / "必填，与 name 必填其一"（同表内两两配对）
# 依据（2026-09-17 全量核验）：42 个接口有此类组，其中 9 个目录未声明，
# 导致空参打到网关拿 101（本地校验无法拦截）。
_GROUP_RE = re.compile(r"与\s*([A-Za-z][A-Za-z0-9_]*)\s*必填其一?")


def extract_required_groups(path):
    """从 PDF 请求参数表提取"必填其一"组，返回 [[f1, f2], ...]。

    用**表格结构**（而非文本行）配对：在同一张请求参数表内，
    若参数 A 的说明含"与 B 必填其一"，则 {A, B} 成组——A 来自首列、B 来自该说明。
    """
    try:
        doc = fitz.open(path)
    except Exception:
        return []
    groups, seen = [], set()
    END_MARKERS = ("请求示例", "接口响应信息", "返回示例", "data 数据信息节点说明")
    for page in doc:
        if any(m in page.get_text() for m in END_MARKERS):
            break
        try:
            tabs = page.find_tables()
        except Exception:
            continue
        for t in tabs.tables:
            try:
                rows = t.extract()
            except Exception:
                continue
            for cells in rows:
                if len(cells) < 2:
                    continue
                cur = norm(cells[0])
                if not re.match(r"^[A-Za-z][A-Za-z0-9_]*$", cur):
                    continue
                joined = norm(" ".join(str(c or "") for c in cells[1:]))
                for m in _GROUP_RE.finditer(joined):
                    other = m.group(1)
                    if other == cur:
                        continue
                    key = tuple(sorted({cur, other}))
                    if key in seen:
                        continue
                    seen.add(key)
                    groups.append([cur, other])
    return groups
