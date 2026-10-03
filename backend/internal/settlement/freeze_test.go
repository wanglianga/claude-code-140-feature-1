package settlement

import "testing"

func TestLiabilitySplitValid(t *testing.T) {
	if (LiabilitySplit{60, 10, 20, 10}).Valid() != true {
		t.Fatal("60/10/20/10 应合法")
	}
	if (LiabilitySplit{60, 10, 20, 5}).Valid() {
		t.Fatal("合计 95 应非法")
	}
	if (LiabilitySplit{-1, 101, 0, 0}).Valid() {
		t.Fatal("负值应非法")
	}
}

func TestReleaseFreeze(t *testing.T) {
	// 302.45 元冻结，施工方 60% / 基金 10% / 公共 20% / 业主自担 10%
	rel := ReleaseFreeze(302.45, LiabilitySplit{60, 10, 20, 10})
	if !approx(rel.InstallerPart, 181.47) {
		t.Fatalf("installer=%v want 181.47", rel.InstallerPart)
	}
	if !approx(rel.FundPart, 30.24) {
		t.Fatalf("fund=%v want 30.24", rel.FundPart)
	}
	if !approx(rel.PublicPart, 60.49) {
		t.Fatalf("public=%v want 60.49", rel.PublicPart)
	}
	// 发放 + 业主自担必须与原冻结额严格相等（舍入余差由业主自担吸收）
	if !approx(rel.ReleasedToOwner+rel.OwnerBorne, 302.45) {
		t.Fatalf("释放%.2f+自担%.2f != 302.45", rel.ReleasedToOwner, rel.OwnerBorne)
	}
	if !approx(rel.ReleasedToOwner, 272.20) || !approx(rel.OwnerBorne, 30.25) {
		t.Fatalf("released=%v borne=%v want 272.20/30.25", rel.ReleasedToOwner, rel.OwnerBorne)
	}

	// 施工方全责：全额赔付发放，业主不自担
	rel = ReleaseFreeze(100, LiabilitySplit{100, 0, 0, 0})
	if !approx(rel.ReleasedToOwner, 100) || rel.OwnerBorne != 0 {
		t.Fatalf("施工方全责应全额发放: %+v", rel)
	}

	// 业主全责：冻结金额全部冲减，不发放
	rel = ReleaseFreeze(88.88, LiabilitySplit{0, 0, 0, 100})
	if rel.ReleasedToOwner != 0 || !approx(rel.OwnerBorne, 88.88) {
		t.Fatalf("业主全责应全额自担: %+v", rel)
	}
}
