// catalog-dump 导出运行时有效目录（服务实际加载的形态），用于再生成
// 仓库根目录 data/full_catalog.json（评测 harness 读取的镜像副本）。
//
// 用法：
//
//	go run ./cmd/catalog-dump -o data/full_catalog.json   # 再生成
//	go run ./cmd/catalog-dump                             # 输出到 stdout
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/xrzlizheng/keeprisk-mcp/internal/catalog"
)

func main() {
	out := flag.String("o", "", "输出文件路径（缺省 stdout）")
	flag.Parse()
	c, err := catalog.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载内嵌目录失败:", err)
		os.Exit(1)
	}
	b, err := c.EffectiveJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "导出失败:", err)
		os.Exit(1)
	}
	if *out == "" {
		_, _ = os.Stdout.Write(b)
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写入失败:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "已写入 %s（%d 接口）\n", *out, c.LenLatest())
}
