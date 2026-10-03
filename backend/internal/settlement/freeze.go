// 漏水责任确认后的收益冻结释放算法（纯计算，无数据库依赖）。
package settlement

// LiabilitySplit 漏水责任比例（%）：施工方赔付 / 运维基金承担 / 楼栋公共收益支付 / 业主自担
type LiabilitySplit struct {
	InstallerPct float64 `json:"installer_pct"`
	FundPct      float64 `json:"fund_pct"`
	PublicPct    float64 `json:"public_pct"`
	OwnerPct     float64 `json:"owner_pct"`
}

// Total 四项比例合计
func (l LiabilitySplit) Total() float64 {
	return l.InstallerPct + l.FundPct + l.PublicPct + l.OwnerPct
}

// Valid 比例非负且合计为 100%
func (l LiabilitySplit) Valid() bool {
	t := l.Total()
	return t >= 99.9999 && t <= 100.0001 &&
		l.InstallerPct >= 0 && l.FundPct >= 0 && l.PublicPct >= 0 && l.OwnerPct >= 0
}

// FreezeRelease 一笔冻结挂账按责任比例的拆分结果
type FreezeRelease struct {
	HeldAmount      float64 `json:"held_amount"`
	InstallerPart   float64 `json:"installer_part"`    // 施工方赔付部分（释放给住户）
	FundPart        float64 `json:"fund_part"`         // 运维(维修)基金承担（释放给住户）
	PublicPart      float64 `json:"public_part"`       // 楼栋公共收益支付（释放给住户）
	ReleasedToOwner float64 `json:"released_to_owner"` // 上述三项合计，发放给住户
	OwnerBorne      float64 `json:"owner_borne"`       // 业主自担，冲减挂账、不发放
}

// ReleaseFreeze 按责任比例拆分一笔冻结挂账。
// 非业主责任（施工方/运维基金/公共收益）对应的部分释放给住户；业主自担部分不释放。
// 为保证“发放 + 自担 = 原冻结额”严格成立，舍入余差计入业主自担。
func ReleaseFreeze(amount float64, l LiabilitySplit) FreezeRelease {
	out := FreezeRelease{HeldAmount: round2(amount)}
	scale := amount / 100
	out.InstallerPart = round2(l.InstallerPct * scale)
	out.FundPart = round2(l.FundPct * scale)
	out.PublicPart = round2(l.PublicPct * scale)
	out.ReleasedToOwner = round2(out.InstallerPart + out.FundPart + out.PublicPart)
	out.OwnerBorne = round2(amount - out.ReleasedToOwner)
	if out.OwnerBorne < 0 {
		out.OwnerBorne = 0
	}
	return out
}
