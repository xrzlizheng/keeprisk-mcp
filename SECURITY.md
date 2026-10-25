# 安全策略

## 凭证管理（最重要）

**本仓库不包含、也不接受任何真实凭证。** 包括但不限于：

- 入站鉴权 token（`KEEPRISK_AUTH_MAP` 中的 `kpm_*` 值）
- 上游网关 AppCode
- 任何 API Key、密码、私钥

所有凭证由管理员统一生成并发放。如需开通，请联系：**https://github.com/xrzlizheng**

### 如果你不小心提交了凭证

1. **先在网关侧吊销/轮换该凭证** —— 历史提交中的凭证一律按已泄露处理，删除提交不足以补救；
2. 再联系管理员确认轮换完成；
3. 如需清理 git 历史，可使用 `git filter-repo`，并强制推送。

### 代码内的测试凭证

涉及真实网关的回归测试（如 `TestLiveJudicialDeepDiveChain`）一律从环境变量读取凭证
（`KEEPRISK_LIVE_TOKEN` + `KEEPRISK_AUTH_MAP`），未设置时自动跳过。请勿把真实 token
以任何形式写回测试文件。

## 报告漏洞

请通过 GitHub 私密渠道报告安全问题（Security Advisory 或直接联系维护者），
不要公开提交包含漏洞细节的 Issue。
