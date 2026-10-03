package settlement

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestComputeBasicSplit(t *testing.T) {
	in := Input{
		PeriodMonth: "2026-09",
		TotalKwh:    1000,
		GridPrice:   0.4,
		SubsidyRate: 0.05,
		OmFee:       100,
		Tax:         50,
		FundRatio:   20,
		Shareholders: []Share{
			{UserID: 1, Name: "张三", Ratio: 60},
			{UserID: 2, Name: "李四", Ratio: 10},
		},
	}
	res := Compute(in)
	if !approx(res.GrossRevenue, 400) {
		t.Fatalf("gross=%v want 400", res.GrossRevenue)
	}
	if !approx(res.TotalSubsidy, 50) {
		t.Fatalf("subsidy=%v want 50", res.TotalSubsidy)
	}
	if res.ShareholderRatio != 70 || res.PublicRatio != 30 {
		t.Fatalf("ratios got %v/%v", res.ShareholderRatio, res.PublicRatio)
	}
	// 张三分摊: 收入 240 + 补贴 30 - 运维 60 - 税 30 = 180
	var zhang *Item
	for i := range res.Items {
		if res.Items[i].ItemType == "shareholder" && res.Items[i].UserID != nil && *res.Items[i].UserID == 1 {
			zhang = &res.Items[i]
		}
	}
	if zhang == nil {
		t.Fatal("张三明细缺失")
	}
	if !approx(zhang.Amount, 180) {
		t.Fatalf("张三 amount=%v want 180", zhang.Amount)
	}
	if !approx(zhang.OmShare, 60) || !approx(zhang.TaxShare, 30) {
		t.Fatalf("分摊错误 om=%v tax=%v", zhang.OmShare, zhang.TaxShare)
	}
	// 公共: 收入 120 + 补贴 15 - 运维 30 - 税 15 = 90
	pub := res.Items[len(res.Items)-2]
	if !approx(pub.Amount, 90) {
		t.Fatalf("public amount=%v want 90", pub.Amount)
	}
	// 维修基金计提 90*20%=18，公共可支配 72
	if !approx(res.FundAddition, 18) || !approx(res.PublicDistributab, 72) {
		t.Fatalf("fund=%v pubDist=%v", res.FundAddition, res.PublicDistributab)
	}
	// 碳减排
	if !approx(res.CarbonKg, 581) {
		t.Fatalf("carbon=%v want 581", res.CarbonKg)
	}
}

func TestWorkOrderFundAndResponsibility(t *testing.T) {
	// 基金余额 1000；物业责任维修 600，安装商责任 800（赔偿，不冲减住户收益）
	in := Input{
		TotalKwh: 1000, GridPrice: 0.4, SubsidyRate: 0,
		FundRatio:    20,
		FundBalance:  1000,
		Shareholders: []Share{{UserID: 1, Ratio: 100}},
		WorkOrders: []WorkOrder{
			{ID: 1, Title: "漏水", Cost: 600, Responsibility: "property"},
			{ID: 2, Title: "逆变器", Cost: 800, Responsibility: "installer"},
		},
	}
	res := Compute(in)
	if !approx(res.WorkOrderCost, 600) {
		t.Fatalf("woCost=%v want 600（安装商责任不计入）", res.WorkOrderCost)
	}
	// 600 全部由维修基金承担，住户分摊 0
	if !approx(res.FundUsed, 600) || !approx(res.RepairDeduct, 0) {
		t.Fatalf("fundUsed=%v deduct=%v", res.FundUsed, res.RepairDeduct)
	}
	holder := res.Items[0]
	if !approx(holder.Amount, 400) {
		t.Fatalf("holder amount=%v want 400（基金足额支付维修费）", holder.Amount)
	}

	// 基金不足场景
	in.FundBalance = 200
	res = Compute(in)
	if !approx(res.FundUsed, 200) || !approx(res.RepairDeduct, 400) {
		t.Fatalf("fundUsed=%v deduct=%v want 200/400", res.FundUsed, res.RepairDeduct)
	}
	holder = res.Items[0]
	if !approx(holder.Amount, 0) { // 400 收入 - 400 维修分摊
		t.Fatalf("holder amount=%v want 0", holder.Amount)
	}

	// 有赔偿时：property 责任 600，赔偿 300，净支出 300 走基金
	in.Compensation = 300
	in.FundBalance = 1000
	res = Compute(in)
	if !approx(res.RepairDeduct, 0) || !approx(res.FundUsed, 300) {
		t.Fatalf("赔偿后 fundUsed=%v deduct=%v want 300/0", res.FundUsed, res.RepairDeduct)
	}
}

func TestPublicOnlyRatio(t *testing.T) {
	res := Compute(Input{TotalKwh: 500, GridPrice: 0.4, FundRatio: 0})
	if res.PublicRatio != 100 {
		t.Fatalf("public ratio=%v want 100", res.PublicRatio)
	}
	if !approx(res.GrossRevenue, 200) || !approx(res.Distributable, 200) {
		t.Fatalf("金额错误 gross=%v dist=%v", res.GrossRevenue, res.Distributable)
	}
}

func TestSharedResponsibility(t *testing.T) {
	// 漏水责任多方分担：施工方赔偿 40%，项目侧（基金/公共/业主）自担 60%
	in := Input{
		TotalKwh: 1000, GridPrice: 0.4,
		FundBalance:  0,
		Shareholders: []Share{{UserID: 1, Ratio: 100}},
		WorkOrders: []WorkOrder{{
			ID: 1, Title: "漏水", Cost: 1000, Responsibility: "shared",
			SelfRatio: 60, ClaimRatio: 40,
		}},
	}
	res := Compute(in)
	if !approx(res.WorkOrderCost, 600) {
		t.Fatalf("自担维修成本应 600, got %v", res.WorkOrderCost)
	}
	if !approx(res.WorkOrderClaim, 400) || !approx(res.Compensation, 400) {
		t.Fatalf("施工方赔偿应 400: claim=%v comp=%v", res.WorkOrderClaim, res.Compensation)
	}
	// 项目侧 600 无基金 → 全额按组件比例分摊
	if !approx(res.Items[0].WoShare, 600) {
		t.Fatalf("维修分摊应 600, got %v", res.Items[0].WoShare)
	}
}

func TestRevenueFreeze(t *testing.T) {
	// 2000kWh×0.4=800 电费 + 100 补贴：住户（70%）应得 630，公共（30%）270
	in := Input{
		TotalKwh: 2000, GridPrice: 0.4, SubsidyRate: 0.05, FundRatio: 20,
		Shareholders: []Share{
			{UserID: 1, Name: "丁", Ratio: 40},
			{UserID: 2, Name: "戊", Ratio: 30},
		},
		Freezes: []FreezeTarget{
			{UserID: 1, Ratio: 100}, // 丁全额冻结
			{UserID: 2, Ratio: 50},  // 戊冻结一半
		},
	}
	res := Compute(in)
	var item1, item2 *Item
	for i := range res.Items {
		if res.Items[i].UserID != nil && *res.Items[i].UserID == 1 {
			item1 = &res.Items[i]
		}
		if res.Items[i].UserID != nil && *res.Items[i].UserID == 2 {
			item2 = &res.Items[i]
		}
	}
	if !approx(item1.Amount, 360) || !approx(item1.Frozen, 360) || item1.Payable != 0 {
		t.Fatalf("丁应冻结 360: %+v", item1)
	}
	if !approx(item2.Amount, 270) || !approx(item2.Frozen, 135) || !approx(item2.Payable, 135) {
		t.Fatalf("戊应冻结 135、实发 135: %+v", item2)
	}
	if !approx(res.FrozenTotal, 495) {
		t.Fatalf("冻结总额应 495, got %v", res.FrozenTotal)
	}
}
