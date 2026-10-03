package api_test

import (
	"fmt"
	"testing"
)

// bootstrapLeakBuilding 建一个进入运营、含两户认购（丁40%、戊30%）与当月发电量的楼栋。
func bootstrapLeakBuilding(t *testing.T, c *client, admin string) (bid, inv, idD, idE int64, tD, tE string) {
	registerResident(c, admin, "丁(顶楼)", "13200000001", "7-601")
	registerResident(c, admin, "戊", "13200000002", "7-501")
	registerResident(c, admin, "己(新业主)", "13200000003", "7-401")
	tD, tE = c.login("13200000001", "pwd123"), c.login("13200000002", "pwd123")
	idD, idE = userID(c, admin, "13200000001"), userID(c, admin, "13200000002")
	idF := userID(c, admin, "13200000003")

	st, out := c.do("POST", "/api/buildings", admin, map[string]any{
		"name": "漏水测试楼", "total_capacity_kw": 30, "panel_total": 60,
		"unit_price": 1500, "maintenance_fund_ratio": 20,
	})
	if st != 201 {
		t.Fatalf("create building: %d %v", st, out)
	}
	bid = int64(out["id"].(float64))
	for _, id := range []int64{idD, idE, idF} {
		if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/members", bid), admin,
			map[string]any{"user_id": id}); st != 201 {
			t.Fatalf("add member: %d %v", st, out)
		}
	}
	for _, stage := range []string{"voting"} {
		c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin, map[string]string{"to_stage": stage})
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/votes", bid), admin, map[string]string{"title": "立项"})
	voteID := int64(out["id"].(float64))
	tF := c.login("13200000003", "pwd123")
	for _, tk := range []string{tD, tE, tF} {
		c.do("POST", fmt.Sprintf("/api/votes/%d/choose", voteID), tk, map[string]string{"choice": "agree"})
	}
	if st, out = c.do("POST", fmt.Sprintf("/api/votes/%d/close", voteID), admin, nil); st != 200 || out["result"] != "approved" {
		t.Fatalf("表决应通过: %d %v", st, out)
	}
	c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin, map[string]string{"to_stage": "subscribing"})
	for _, x := range []struct {
		tok   string
		ratio float64
	}{{tD, 40}, {tE, 30}} {
		st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/subscribe", bid), x.tok,
			map[string]any{"type": "shareholder", "ratio": x.ratio})
		sid := int64(out["id"].(float64))
		c.do("POST", fmt.Sprintf("/api/subscriptions/%d/pay", sid), x.tok, nil)
	}
	for _, stage := range []string{"construction", "grid_acceptance", "operating"} {
		c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin, map[string]string{"to_stage": stage})
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/inverters", bid), admin,
		map[string]any{"name": "1#", "capacity_kw": 30})
	inv = int64(out["id"].(float64))
	c.do("POST", fmt.Sprintf("/api/buildings/%d/generation", bid), admin, map[string]any{
		"period_month": "2026-10", "inverter_id": inv, "kwh": 2000,
	})
	return bid, inv, idD, idE, tD, tE
}

func TestLeakFreezeLifecycle(t *testing.T) {
	c := setup(t)
	admin := c.login("13000000000", "admin123")
	bid, _, idD, idE, tD, tE := bootstrapLeakBuilding(t, c, admin)

	// 1) 顶楼住户此前已签署屋顶使用同意书
	if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/consents", bid), admin, map[string]any{
		"user_id": idD, "unit_no": "7-601", "signed_at": "2026-02-20",
		"document_no": "RC-77", "note": "同意支架穿层",
	}); st != 201 {
		t.Fatalf("登记同意书: %d %v", st, out)
	}

	// 2) 雨后顶楼漏水投诉：平台自动识别此前已签署同意
	st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/work-orders", bid), tD, map[string]any{
		"period_month": "2026-10", "title": "新装支架位置雨后渗水", "wo_type": "leak",
		"description": "顶板灯线周边滴漏",
	})
	if st != 201 || out["resident_consent_status"] != "signed" {
		t.Fatalf("漏水工单应识别已签署同意: %d %v", st, out)
	}
	woID := int64(out["id"].(float64))

	// 3) 关联施工照片、屋顶防水验收、雨量记录、现场照片
	for _, a := range []map[string]any{
		{"kind": "onsite_photo", "title": "顶板渗水照片", "detail": "湿渍0.4㎡", "recorded_at": "2026-10-02"},
		{"kind": "construction_photo", "title": "支架穿层施工照片", "detail": "化学锚栓穿层", "recorded_at": "2026-06-15"},
		{"kind": "waterproof_acceptance", "title": "防水验收单", "detail": "四方验收合格，质保5年", "recorded_at": "2026-06-18"},
		{"kind": "rainfall", "title": "10月1日雨量", "detail": "日降雨86.2mm", "recorded_at": "2026-10-01"},
	} {
		if st, out := c.do("POST", fmt.Sprintf("/api/work-orders/%d/attachments", woID), admin, a); st != 201 {
			t.Fatalf("关联证据 %v: %d %v", a["kind"], st, out)
		}
	}
	st, out = c.do("GET", fmt.Sprintf("/api/work-orders/%d/attachments", woID), admin, nil)
	if len(out["items"].([]any)) != 4 {
		t.Fatalf("应有 4 条证据: %v", out)
	}

	// 4) 责任一时无法确认：临时止水、住户安抚、第三方检测
	for _, a := range []map[string]any{
		{"action": "temporary_stoppage", "note": "东侧支架附加层临时覆盖止水"},
		{"action": "reassure", "note": "上门安抚，告知检测与赔付安排"},
		{"action": "third_party_test", "org": "市房屋安全检测中心", "note": "红外+淋水复检"},
	} {
		if st, out := c.do("POST", fmt.Sprintf("/api/work-orders/%d/leak-action", woID), admin, a); st != 200 {
			t.Fatalf("处置动作 %v: %d %v", a["action"], st, out)
		}
	}
	st, wos := c.do("GET", fmt.Sprintf("/api/buildings/%d/work-orders", bid), admin, nil)
	var wo map[string]any
	for _, it := range wos["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == woID {
			wo = m
		}
	}
	if wo["temporary_stoppage"] != true || wo["reassured"] != true || wo["third_party_tested"] != true {
		t.Fatalf("处置动作未落库: %v", wo)
	}
	if int64(wo["attachment_count"].(float64)) != 4 {
		t.Fatalf("工单证据计数应为4: %v", wo["attachment_count"])
	}

	// 5) 维修期间暂时冻结相关组件收益：丁100%、戊50%
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/freezes", bid), admin, map[string]any{
		"work_order_id": woID, "period_month": "2026-10", "estimated_cost": 2000,
		"note": "责任未明前冻结",
		"targets": []map[string]any{
			{"user_id": idD, "ratio": 100},
			{"user_id": idE, "ratio": 50},
		},
	})
	if st != 201 {
		t.Fatalf("创建冻结单: %d %v", st, out)
	}
	fzID := int64(out["id"].(float64))

	// 待生效冻结可取消（另建一单再取消）
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/work-orders", bid), tE, map[string]any{
		"period_month": "2026-11", "title": "另一处疑似渗水", "wo_type": "leak",
	})
	wo2 := int64(out["id"].(float64))
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/freezes", bid), admin, map[string]any{
		"work_order_id": wo2, "period_month": "2026-11",
		"targets": []map[string]any{{"user_id": idE, "ratio": 100}},
	})
	fz2 := int64(out["id"].(float64))
	if st, _ = c.do("POST", fmt.Sprintf("/api/freezes/%d/cancel", fz2), admin, nil); st != 200 {
		t.Fatalf("取消待生效冻结应 200, got %d", st)
	}
	if st, _ = c.do("POST", fmt.Sprintf("/api/freezes/%d/release", fzID), admin, nil); st != 409 {
		t.Fatalf("冻结单尚未生效（结算未定稿）时释放应 409, got %d", st)
	}

	// 6) 10 月结算：冻结额计算
	// 2000kWh×0.4=800 电费，补贴 100；丁40%=360（全冻），戊30%=270（冻50%=135），公共30%=270（不冻）
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/settlements", bid), admin, map[string]any{
		"period_month": "2026-10", "grid_price": 0.4, "subsidy_per_kwh": 0.05,
	})
	if st != 200 {
		t.Fatalf("结算生成: %d %v", st, out)
	}
	settle := out["settlement"].(map[string]any)
	if settle["frozen_total"].(float64) != 495 {
		t.Fatalf("冻结总额应 495（360+135），got %v", settle["frozen_total"])
	}
	spID := int64(out["id"].(float64))

	// 7) 定稿：丁只入账 0，戊入账 135；冻结单转为冻结中
	if st, out = c.do("POST", fmt.Sprintf("/api/settlements/%d/finalize", spID), admin, nil); st != 200 {
		t.Fatalf("定稿: %d %v", st, out)
	}
	st, me := c.do("GET", "/api/me", tD, nil)
	if bal, _ := me["balance"].(string); !eqMoney(bal, 0) {
		t.Fatalf("丁收益应全额冻结，余额=%v", me["balance"])
	}
	st, me = c.do("GET", "/api/me", tE, nil)
	if bal, _ := me["balance"].(string); !eqMoney(bal, 135) {
		t.Fatalf("戊应入账未冻结部分 135，余额=%v", me["balance"])
	}
	st, fr := c.do("GET", fmt.Sprintf("/api/buildings/%d/freezes", bid), admin, nil)
	var active map[string]any
	for _, it := range fr["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == fzID {
			active = m
		}
	}
	if active["status"] != "active" {
		t.Fatalf("冻结单应在结算定稿后生效: %v", active["status"])
	}
	frozenByName := map[string]float64{}
	for _, t := range active["targets"].([]any) {
		m := t.(map[string]any)
		frozenByName[m["name"].(string)] = m["frozen_amount"].(float64)
	}
	if frozenByName["丁(顶楼)"] != 360 || frozenByName["戊"] != 135 {
		t.Fatalf("冻结持有额错误: %v", frozenByName)
	}

	// 8) 冻结未释放前禁止房屋转让
	st, subs := c.do("GET", fmt.Sprintf("/api/buildings/%d/subscriptions", bid), admin, nil)
	subD := int64(0)
	for _, it := range subs["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["user_id"].(float64)) == idD {
			subD = int64(m["id"].(float64))
		}
	}
	idF := userID(c, admin, "13200000003")
	if st, _ = c.do("POST", fmt.Sprintf("/api/buildings/%d/transfers", bid), tD, map[string]any{
		"subscription_id": subD, "to_user_id": idF, "rights_transfer": true,
	}); st != 409 {
		t.Fatalf("冻结未释放时转让应 409, got %d", st)
	}

	// 9) 责任确认：施工方60% / 运维基金40%（基金只有54，不足144由公共收益承担）
	st, out = c.do("POST", fmt.Sprintf("/api/freezes/%d/release", fzID), admin, map[string]any{
		"note": "检测确认安装密封缺陷为主因",
		"allocation": []map[string]any{
			{"bearer": "installer", "ratio": 60},
			{"bearer": "fund", "ratio": 40},
		},
	})
	if st != 200 {
		t.Fatalf("按责任比例释放: %d %v", st, out)
	}
	if out["installer"].(float64) != 297 || out["fund"].(float64) != 54 || out["fund_shortfall"].(float64) != 144 {
		t.Fatalf("释放金额错误: %v", out)
	}
	// 丁拿回全部360，戊拿回剩余135
	st, me = c.do("GET", "/api/me", tD, nil)
	if bal, _ := me["balance"].(string); !eqMoney(bal, 360) {
		t.Fatalf("丁释放后余额应 360, got %v", me["balance"])
	}
	st, me = c.do("GET", "/api/me", tE, nil)
	if bal, _ := me["balance"].(string); !eqMoney(bal, 270) {
		t.Fatalf("戊释放后余额应 270, got %v", me["balance"])
	}
	// 公共账户：收入 900 - 维修 495 + 赔偿 297 = 702；维修基金 54 计提 - 54 动用 = 0
	st, lg := c.do("GET", fmt.Sprintf("/api/buildings/%d/ledger", bid), admin, nil)
	if lg["public_balance"].(float64) != 63702 {
		t.Fatalf("公共账户余额应 63702(认购款63000+运营净额702), got %v", lg["public_balance"])
	}
	if lg["maintenance_fund_balance"].(float64) != 0 {
		t.Fatalf("维修基金应耗尽为 0, got %v", lg["maintenance_fund_balance"])
	}

	// 10) 工单责任回写 shared，处理结论写入后续巡检计划
	st, wos = c.do("GET", fmt.Sprintf("/api/buildings/%d/work-orders", bid), admin, nil)
	for _, it := range wos["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == woID {
			if m["responsibility"] != "shared" {
				t.Fatalf("工单责任应回写 shared, got %v", m["responsibility"])
			}
		}
	}
	st, ins := c.do("GET", fmt.Sprintf("/api/buildings/%d/roof-inspections", bid), admin, nil)
	if len(ins["items"].([]any)) != 1 {
		t.Fatalf("应自动生成 1 条后续屋顶巡检, got %v", ins["items"])
	}
	inspID := int64(ins["items"].([]any)[0].(map[string]any)["id"].(float64))
	if st, _ = c.do("POST", fmt.Sprintf("/api/roof-inspections/%d/complete", inspID), admin,
		map[string]string{"result_note": "穿层封堵完好，雨后无复漏"}); st != 200 {
		t.Fatalf("登记巡检结果应 200, got %d", st)
	}

	// 11) 冻结释放后允许转让
	if st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/transfers", bid), tD, map[string]any{
		"subscription_id": subD, "to_user_id": idF, "rights_transfer": true,
	}); st != 201 {
		t.Fatalf("释放后应允许发起转让: %d %v", st, out)
	}
	trID := int64(out["id"].(float64))
	if st, out = c.do("POST", fmt.Sprintf("/api/transfers/%d/confirm", trID), admin, nil); st != 200 {
		t.Fatalf("物业确认转让: %d %v", st, out)
	}
}

func eqMoney(s string, want float64) bool {
	var got float64
	if _, err := fmt.Sscanf(s, "%f", &got); err != nil {
		return false
	}
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < 0.005
}
