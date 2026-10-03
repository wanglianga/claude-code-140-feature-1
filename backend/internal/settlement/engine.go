// Package settlement 实现光伏发电月度收益结算引擎（纯计算，无数据库依赖，便于单元测试）。
package settlement

import "math"

// 华东电网平均 CO2 排放因子 (kg/kWh)，用于碳减排估算
const CarbonFactorKgPerKwh = 0.5810

// Share 已认购住户的组件份额
type Share struct {
	UserID int64   `json:"user_id"`
	Name   string  `json:"name"`
	Ratio  float64 `json:"ratio"` // 占整楼组件比例 %
}

// WorkOrder 当期维修工单（仅 property/owner 责任的费用由项目承担；installer/insurance 走赔偿）
type WorkOrder struct {
	ID             int64   `json:"id"`
	Title          string  `json:"title"`
	Cost           float64 `json:"cost"`
	Responsibility string  `json:"responsibility"`
	// shared（多方按比例分担）时使用：
	// SelfRatio  = 项目侧承担比例（物业/维修基金/公共收益/业主）合计 %
	// ClaimRatio = 对方赔偿比例（施工方/保险）合计 %，两者之和应为 100
	SelfRatio  float64 `json:"self_ratio"`
	ClaimRatio float64 `json:"claim_ratio"`
}

// FreezeTarget 当期被暂时冻结的分配对象：UserID=0 表示公共部分；Ratio 为冻结其当期收益的百分比。
type FreezeTarget struct {
	UserID int64   `json:"user_id"`
	Ratio  float64 `json:"ratio"`
}

// Input 月度结算输入
type Input struct {
	BuildingID   int64
	PeriodMonth  string         `json:"period_month"`
	TotalKwh     float64        `json:"total_kwh"`
	GridPrice    float64        `json:"grid_price"`   // 上网电价 元/kWh
	SubsidyRate  float64        `json:"subsidy_rate"` // 度电补贴 元/kWh
	OmFee        float64        `json:"om_fee"`       // 当期运维费(元)
	Tax          float64        `json:"tax"`          // 当期税费(元)
	FundRatio    float64        `json:"fund_ratio"`   // 公共净收益划入维修基金比例 %
	FundBalance  float64        `json:"fund_balance"` // 维修基金当前余额
	Compensation float64        `json:"compensation"` // 安装商/保险当期赔偿
	Shareholders []Share        `json:"shareholders"`
	WorkOrders   []WorkOrder    `json:"work_orders"`
	Freezes      []FreezeTarget `json:"freezes"`
}

// Item 结算明细行
type Item struct {
	UserID   *int64  `json:"user_id"`
	ItemType string  `json:"item_type"` // shareholder / public / maintenance_fund
	Name     string  `json:"name"`
	Ratio    float64 `json:"ratio"`
	Kwh      float64 `json:"kwh"`
	Revenue  float64 `json:"revenue"`
	Subsidy  float64 `json:"subsidy"`
	OmShare  float64 `json:"om_share"`
	TaxShare float64 `json:"tax_share"`
	WoShare  float64 `json:"wo_share"`
	Amount   float64 `json:"amount"`  // 应分得金额（冻结前）
	Frozen   float64 `json:"frozen"`  // 因漏水投诉暂时冻结的金额
	Payable  float64 `json:"payable"` // 本期实际入账金额
	Memo     string  `json:"memo"`
}

// Result 结算结果
type Result struct {
	PeriodMonth       string  `json:"period_month"`
	TotalKwh          float64 `json:"total_kwh"`
	GrossRevenue      float64 `json:"gross_revenue"`
	TotalSubsidy      float64 `json:"total_subsidy"`
	OmFee             float64 `json:"om_fee"`
	Tax               float64 `json:"tax"`
	WorkOrderCost     float64 `json:"work_order_cost"`  // 项目侧承担（property/owner/shared 自担部分）的维修费用合计
	WorkOrderClaim    float64 `json:"work_order_claim"` // shared 责任中由施工方/保险赔偿的部分
	Compensation      float64 `json:"compensation"`
	FundUsed          float64 `json:"fund_used"`     // 维修基金动用金额
	RepairDeduct      float64 `json:"repair_deduct"` // 维修净支出（按组件比例分摊）
	ShareholderRatio  float64 `json:"shareholder_ratio"`
	PublicRatio       float64 `json:"public_ratio"`
	FundRatio         float64 `json:"fund_ratio"`
	FundAddition      float64 `json:"fund_addition"` // 本期公共收益计提维修基金
	PublicDistributab float64 `json:"public_distributable"`
	Distributable     float64 `json:"distributable"` // 当期可分配净额合计（冻结前）
	FrozenTotal       float64 `json:"frozen_total"`  // 因漏水投诉暂时冻结的收益合计
	PayableTotal      float64 `json:"payable_total"` // 本期实际入账合计
	CarbonKg          float64 `json:"carbon_kg"`
	Items             []Item  `json:"items"`
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// Compute 根据输入生成完整月度结算方案。
//
// 分摊规则：
//  1. 发电总收入 = 发电量×上网电价；补贴 = 发电量×度电补贴；
//  2. 运维费、税费按组件比例在认购住户与公共部分之间分摊；
//  3. 维修费用中 property/owner 责任部分先由维修基金支付，基金不足部分按组件比例分摊；
//     installer/insurance 责任由对方赔偿，不冲减住户收益（赔偿计入公共账）；
//  4. 公共部分净收益按 FundRatio 计提维修基金，其余为公共可支配收益。
func Compute(in Input) Result {
	res := Result{
		PeriodMonth:  in.PeriodMonth,
		TotalKwh:     round2(in.TotalKwh),
		OmFee:        round2(in.OmFee),
		Tax:          round2(in.Tax),
		FundRatio:    in.FundRatio,
		Compensation: round2(in.Compensation),
	}
	res.GrossRevenue = round2(in.TotalKwh * in.GridPrice)
	res.TotalSubsidy = round2(in.TotalKwh * in.SubsidyRate)
	res.CarbonKg = round2(in.TotalKwh * CarbonFactorKgPerKwh)

	var sRatio float64
	for _, s := range in.Shareholders {
		sRatio += s.Ratio
	}
	res.ShareholderRatio = round2(sRatio)
	res.PublicRatio = round2(100 - sRatio)

	var woCost, woClaim float64
	for _, w := range in.WorkOrders {
		switch w.Responsibility {
		case "property", "owner":
			woCost += w.Cost
		case "shared":
			// 多方按比例分担：自担部分进维修费用，对方（施工方/保险）部分计赔偿
			self, claim := w.SelfRatio, w.ClaimRatio
			if self == 0 && claim == 0 {
				self = 100
			}
			if self+claim > 0 {
				woCost += w.Cost * self / (self + claim)
				woClaim += w.Cost * claim / (self + claim)
			} else {
				woCost += w.Cost
			}
		}
	}
	res.WorkOrderCost = round2(woCost)
	res.WorkOrderClaim = round2(woClaim)
	res.Compensation = round2(in.Compensation + woClaim)
	repairGross := math.Max(0, woCost-in.Compensation)
	res.FundUsed = round2(math.Min(in.FundBalance, repairGross))
	res.RepairDeduct = round2(repairGross - res.FundUsed)

	// 按比例为任意一方（住户或公共）生成分摊
	party := func(ratio float64) Item {
		r := ratio / 100
		return Item{
			Ratio:    round2(ratio),
			Kwh:      round2(in.TotalKwh * r),
			Revenue:  round2(res.GrossRevenue * r),
			Subsidy:  round2(res.TotalSubsidy * r),
			OmShare:  round2(res.OmFee * r),
			TaxShare: round2(res.Tax * r),
			WoShare:  round2(res.RepairDeduct * r),
		}
	}

	for _, s := range in.Shareholders {
		it := party(s.Ratio)
		uid := s.UserID
		it.UserID = &uid
		it.ItemType = "shareholder"
		it.Name = s.Name
		it.Amount = round2(it.Revenue + it.Subsidy - it.OmShare - it.TaxShare - it.WoShare)
		res.Items = append(res.Items, it)
	}

	pub := party(res.PublicRatio)
	pub.ItemType = "public"
	pub.Name = "公共账户"
	pub.Amount = round2(pub.Revenue + pub.Subsidy - pub.OmShare - pub.TaxShare - pub.WoShare)
	res.Items = append(res.Items, pub)

	if pub.Amount > 0 && in.FundRatio > 0 {
		res.FundAddition = round2(pub.Amount * in.FundRatio / 100)
	}
	fund := Item{
		ItemType: "maintenance_fund",
		Name:     "维修基金（公共收益计提/动用）",
		Amount:   round2(res.FundAddition - res.FundUsed),
		Memo:     "正数为计提，负数为动用",
	}
	res.Items = append(res.Items, fund)

	res.PublicDistributab = round2(pub.Amount - res.FundAddition)

	// 漏水投诉处置期间，相关组件/公共部分收益按冻结单比例暂时冻结，定稿时只入账未冻结部分。
	// 冻结额不超过该对象当期可分配现金（住户为正收益、公共为计提基金后的可支配额）。
	freezeRatioOf := func(uid int64) float64 {
		var r0 float64
		for _, f := range in.Freezes {
			if f.UserID == uid {
				r0 += f.Ratio
			}
		}
		if r0 > 100 {
			r0 = 100
		}
		return r0
	}
	for i := range res.Items {
		it := &res.Items[i]
		var ratio, capAmt float64
		switch it.ItemType {
		case "shareholder":
			if it.UserID != nil {
				ratio = freezeRatioOf(*it.UserID)
			}
			capAmt = math.Max(0, it.Amount)
		case "public":
			ratio = freezeRatioOf(0)
			capAmt = math.Max(0, res.PublicDistributab)
		default:
			continue
		}
		if ratio > 0 {
			it.Frozen = round2(math.Min(capAmt, it.Amount*ratio/100))
			if it.Frozen < 0 {
				it.Frozen = 0
			}
		}
		it.Payable = round2(it.Amount - it.Frozen)
	}
	// 公共部分冻结后，可支配额相应扣减
	var publicFrozen float64
	for i := range res.Items {
		if res.Items[i].ItemType == "public" {
			publicFrozen = res.Items[i].Frozen
		}
	}
	res.PublicDistributab = round2(res.PublicDistributab - publicFrozen)

	var dist, frozen, payable float64
	for _, it := range res.Items {
		if it.ItemType == "shareholder" {
			dist += it.Amount
			frozen += it.Frozen
			payable += it.Payable
		}
	}
	dist += res.PublicDistributab + publicFrozen
	res.Distributable = round2(dist)
	res.FrozenTotal = round2(frozen + publicFrozen)
	res.PayableTotal = round2(payable + res.PublicDistributab)
	return res
}
