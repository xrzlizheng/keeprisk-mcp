package catalog

import (
	"bytes"
	"os"
	"testing"
)

// 根目录 data/full_catalog.json（评测 harness 读取的镜像副本）必须与**运行时有效目录**
// 语义一致——即"以服务真实行为为准"（2026-09-22 定标）。
//
// 背景：根目录副本与内嵌副本曾长期漂移（225/235 条目参数/input_schema 不同，双向差异），
// harness 按旧副本传参、服务按新目录校验，两边看到的接口不一致。现约定：根目录副本由
// `go run ./cmd/catalog-dump -o data/full_catalog.json` 从内嵌目录导出，本测试守护一致性，
// 任何一侧被手改或目录重建后忘记同步都会在此红灯。
func TestRootCatalogMatchesEffective(t *testing.T) {
	raw, err := os.ReadFile("../../data/full_catalog.json")
	if err != nil {
		t.Fatalf("读取根目录目录副本: %v", err)
	}
	rootCat, err := LoadJSON(raw)
	if err != nil {
		t.Fatalf("解析根目录副本: %v", err)
	}
	embedded, err := Default()
	if err != nil {
		t.Fatalf("加载内嵌目录: %v", err)
	}
	rootFP, err := rootCat.EffectiveFingerprint()
	if err != nil {
		t.Fatalf("根目录副本指纹: %v", err)
	}
	embFP, err := embedded.EffectiveFingerprint()
	if err != nil {
		t.Fatalf("内嵌目录指纹: %v", err)
	}
	if !bytes.Equal(rootFP, embFP) {
		t.Fatalf("根目录 data/full_catalog.json 与运行时有效目录不一致（%d vs %d 字节）——"+
			"请以运行时为准再生成：go run ./cmd/catalog-dump -o data/full_catalog.json",
			len(rootFP), len(embFP))
	}
}
