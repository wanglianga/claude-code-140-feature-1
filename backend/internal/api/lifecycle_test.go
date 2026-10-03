package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"pvplatform/internal/api"
	"pvplatform/internal/auth"
	"pvplatform/internal/db"
)

// adminURL 指向用于创建/删除测试库的维护连接
const adminURL = "postgres://pvapp@127.0.0.1:5433/pvplatform?sslmode=disable"
const testURL = "postgres://pvapp@127.0.0.1:5433/pvplatform_itest?sslmode=disable"

type client struct {
	t      *testing.T
	srv    *httptest.Server
	tokens map[string]string
}

func (c *client) do(method, path, token string, body any) (int, map[string]any) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out
}

func (c *client) login(phone, pwd string) string {
	st, out := c.do("POST", "/api/auth/login", "", map[string]string{"phone": phone, "password": pwd})
	if st != 200 {
		c.t.Fatalf("login %s: %d %v", phone, st, out)
	}
	return out["token"].(string)
}

func setup(t *testing.T) *client {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("测试数据库不可用，跳过集成测试: %v", err)
	}
	defer admin.Close()
	if err := admin.Ping(ctx); err != nil {
		t.Skipf("测试数据库不可用，跳过集成测试: %v", err)
	}
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS pvplatform_itest WITH (FORCE)`); err != nil {
		t.Skipf("无法重建测试库: %v", err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE pvplatform_itest`); err != nil {
		t.Skipf("无法创建测试库: %v", err)
	}

	pool, err := pgxpool.New(ctx, testURL)
	if err != nil {
		t.Skipf("连接测试库失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 直接建一个管理员
	hash, _ := auth.HashPassword("admin123")
	if _, err := pool.Exec(ctx,
		`INSERT INTO users(name,phone,role,password_hash) VALUES ('管理员','13000000000','admin',$1)`, hash); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.NewServer(pool, "test-secret").Router())
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv, tokens: map[string]string{}}
}

func registerResident(c *client, adminToken, name, phone, unit string) {
	st, out := c.do("POST", "/api/auth/register", adminToken,
		map[string]string{"name": name, "phone": phone, "unit_no": unit, "password": "pwd123", "role": "resident"})
	if st != 201 {
		c.t.Fatalf("register %s: %d %v", phone, st, out)
	}
}

func userID(c *client, adminToken, phone string) int64 {
	st, out := c.do("GET", "/api/users?q="+phone, adminToken, nil)
	if st != 200 {
		c.t.Fatalf("users: %d", st)
	}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["phone"] == phone {
			return int64(m["id"].(float64))
		}
	}
	c.t.Fatalf("user %s not found", phone)
	return 0
}

func TestFullLifecycle(t *testing.T) {
	c := setup(t)
	admin := c.login("13000000000", "admin123")

	// 三户居民
	registerResident(c, admin, "甲", "13100000001", "3-601")
	registerResident(c, admin, "乙", "13100000002", "3-501")
	registerResident(c, admin, "丙(新业主)", "13100000003", "3-401")
	tA, tB := c.login("13100000001", "pwd123"), c.login("13100000002", "pwd123")
	idA, idB, idC := userID(c, admin, "13100000001"), userID(c, admin, "13100000002"), userID(c, admin, "13100000003")

	// 建档
	st, out := c.do("POST", "/api/buildings", admin, map[string]any{
		"name": "测试楼", "total_capacity_kw": 30, "panel_total": 60,
		"unit_price": 1500, "maintenance_fund_ratio": 20,
	})
	if st != 201 {
		t.Fatalf("create building: %d %v", st, out)
	}
	bid := int64(out["id"].(float64))
	for _, id := range []int64{idA, idB, idC} {
		if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/members", bid), admin,
			map[string]any{"user_id": id}); st != 201 {
			t.Fatalf("add member %d: %d %v", id, st, out)
		}
	}

	// 进入表决 → 全员同意 → 关表通过 → 进入认购
	if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
		map[string]string{"to_stage": "voting"}); st != 200 {
		t.Fatalf("to voting: %d %v", st, out)
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/votes", bid), admin,
		map[string]string{"title": "立项表决"})
	if st != 201 {
		t.Fatalf("create vote: %d", st)
	}
	voteID := int64(out["id"].(float64))
	// 第一次仅甲 1 票同意（1/3 < 2/3）→ rejected
	if st, out := c.do("POST", fmt.Sprintf("/api/votes/%d/choose", voteID), tA,
		map[string]string{"choice": "agree"}); st != 200 {
		t.Fatalf("vote: %d %v", st, out)
	}
	if st, out := c.do("POST", fmt.Sprintf("/api/votes/%d/choose", voteID), tB,
		map[string]string{"choice": "disagree"}); st != 200 {
		t.Fatalf("vote: %d %v", st, out)
	}
	st, out = c.do("POST", fmt.Sprintf("/api/votes/%d/close", voteID), admin, nil)
	if st != 200 || out["result"] != "rejected" {
		t.Fatalf("第一次表决应为 rejected, got %v", out)
	}
	// 沟通后重新表决，3 户全部同意 → approved
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/votes", bid), admin,
		map[string]string{"title": "立项表决(二次)"})
	voteID = int64(out["id"].(float64))
	tC := c.login("13100000003", "pwd123")
	for _, tk := range []string{tA, tB, tC} {
		c.do("POST", fmt.Sprintf("/api/votes/%d/choose", voteID), tk, map[string]string{"choice": "agree"})
	}
	st, out = c.do("POST", fmt.Sprintf("/api/votes/%d/close", voteID), admin, nil)
	if out["result"] != "approved" {
		t.Fatalf("二次表决应 approved, got %v", out)
	}
	if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
		map[string]string{"to_stage": "subscribing"}); st != 200 {
		t.Fatalf("to subscribing: %d %v", st, out)
	}

	// 认购 甲 40%、乙 30%，缴费
	for _, x := range []struct {
		tok   string
		ratio float64
	}{{tA, 40}, {tB, 30}} {
		st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/subscribe", bid), x.tok,
			map[string]any{"type": "shareholder", "ratio": x.ratio})
		if st != 201 {
			t.Fatalf("subscribe: %d %v", st, out)
		}
		sid := int64(out["id"].(float64))
		if st, out := c.do("POST", fmt.Sprintf("/api/subscriptions/%d/pay", sid), x.tok, nil); st != 200 {
			t.Fatalf("pay: %d %v", st, out)
		}
	}
	// 超额认购应被拒
	st, _ = c.do("POST", fmt.Sprintf("/api/buildings/%d/subscribe", bid), tC,
		map[string]any{"type": "shareholder", "ratio": 40})
	if st != 409 {
		t.Fatalf("超额认购应 409, got %d", st)
	}

	// 推进至运行（施工→并网验收→运行）
	for _, stage := range []string{"construction", "grid_acceptance", "operating"} {
		if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
			map[string]string{"to_stage": stage}); st != 200 {
			t.Fatalf("to %s: %d %v", stage, st, out)
		}
	}

	// 设备与发电量
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/inverters", bid), admin,
		map[string]any{"name": "1#", "capacity_kw": 15})
	inv1 := int64(out["id"].(float64))
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/inverters", bid), admin,
		map[string]any{"name": "2#", "capacity_kw": 15})
	inv2 := int64(out["id"].(float64))
	for _, x := range []struct {
		inv int64
		kwh float64
	}{{inv1, 1000}, {inv2, 800}} {
		if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/generation", bid), admin, map[string]any{
			"period_month": "2026-09", "inverter_id": x.inv, "kwh": x.kwh,
		}); st != 200 {
			t.Fatalf("generation: %d %v", st, out)
		}
	}
	// 漏水工单：物业责任 500 元，已解决
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/work-orders", bid), tA, map[string]any{
		"period_month": "2026-09", "title": "顶楼渗水", "wo_type": "leak",
		"description": "穿层处渗水", "cost": 500,
	})
	if st != 201 {
		t.Fatalf("work order: %d %v", st, out)
	}
	woID := int64(out["id"].(float64))
	if st, out := c.do("POST", fmt.Sprintf("/api/work-orders/%d", woID), admin, map[string]any{
		"status": "resolved", "responsibility": "property", "cost": 500,
	}); st != 200 {
		t.Fatalf("wo update: %d %v", st, out)
	}

	// 生成 9 月结算（草稿）→ 校验收支 → 定稿
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/settlements", bid), admin, map[string]any{
		"period_month": "2026-09", "grid_price": 0.4, "subsidy_per_kwh": 0.05,
		"om_fee": 100, "tax": 50,
	})
	if st != 200 {
		t.Fatalf("settlement create: %d %v", st, out)
	}
	spID := int64(out["id"].(float64))
	settle := out["settlement"].(map[string]any)
	if settle["work_order_cost"].(float64) != 500 {
		t.Fatalf("wo cost = %v", settle["work_order_cost"])
	}
	if settle["fund_used"].(float64) != 0 || settle["repair_deduct"].(float64) != 500 {
		t.Fatalf("基金为 0 时维修应全额分摊: %v", settle)
	}
	st, out = c.do("GET", fmt.Sprintf("/api/settlements/%d", spID), admin, nil)
	if st != 200 {
		t.Fatalf("get settlement: %d", st)
	}
	var holderA float64
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["item_type"] == "shareholder" && int64(m["user_id"].(float64)) == idA {
			holderA = m["amount"].(float64)
		}
	}
	// 甲 40%: 288 + 36 - 40 - 20 - 200 = 64
	if holderA != 64 {
		t.Fatalf("甲结算金额=%v want 64; items=%v", holderA, out["items"])
	}
	if st, out := c.do("POST", fmt.Sprintf("/api/settlements/%d/finalize", spID), admin, nil); st != 200 {
		t.Fatalf("finalize: %d %v", st, out)
	}
	// 定稿后禁止重算/再改工单责任
	if st, _ = c.do("POST", fmt.Sprintf("/api/buildings/%d/settlements", bid), admin, map[string]any{
		"period_month": "2026-09", "grid_price": 0.5,
	}); st != 409 {
		t.Fatalf("已定稿重算应 409, got %d", st)
	}
	if st, _ = c.do("POST", fmt.Sprintf("/api/work-orders/%d", woID), admin,
		map[string]any{"responsibility": "owner"}); st != 409 {
		t.Fatalf("已定稿周期改责任应 409, got %d", st)
	}
	// 甲余额 = 64
	st, out = c.do("GET", "/api/me", tA, nil)
	if out["balance"].(string) != "64.00" {
		t.Fatalf("甲余额=%v want 64.00", out["balance"])
	}
	// 维修基金 = 公共净收益 48 × 20% = 9.60
	st, out = c.do("GET", fmt.Sprintf("/api/buildings/%d/ledger", bid), admin, nil)
	if out["maintenance_fund_balance"].(float64) != 9.6 {
		t.Fatalf("维修基金=%v want 9.6", out["maintenance_fund_balance"])
	}

	// 房屋转让：甲把认购权益随房转给丙
	var subA int64
	st, out = c.do("GET", fmt.Sprintf("/api/buildings/%d/subscriptions", bid), admin, nil)
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["user_id"].(float64)) == idA {
			subA = int64(m["id"].(float64))
		}
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/transfers", bid), tA, map[string]any{
		"subscription_id": subA, "to_user_id": idC, "rights_transfer": true,
	})
	if st != 201 {
		t.Fatalf("transfer create: %d %v", st, out)
	}
	trID := int64(out["id"].(float64))
	if st, out := c.do("POST", fmt.Sprintf("/api/transfers/%d/confirm", trID), admin, nil); st != 200 {
		t.Fatalf("transfer confirm: %d %v", st, out)
	}

	// 10 月发电再结算：40% 份额应归丙，甲不再出现在住户明细
	for _, x := range []struct {
		inv int64
		kwh float64
	}{{inv1, 600}, {inv2, 600}} {
		c.do("POST", fmt.Sprintf("/api/buildings/%d/generation", bid), admin, map[string]any{
			"period_month": "2026-10", "inverter_id": x.inv, "kwh": x.kwh,
		})
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/settlements", bid), admin, map[string]any{
		"period_month": "2026-10", "grid_price": 0.4,
	})
	if st != 200 {
		t.Fatalf("oct settlement: %d %v", st, out)
	}
	octID := int64(out["id"].(float64))
	st, out = c.do("GET", fmt.Sprintf("/api/settlements/%d", octID), admin, nil)
	holders := map[int64]float64{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["item_type"] == "shareholder" {
			holders[int64(m["user_id"].(float64))] = m["amount"].(float64)
		}
	}
	if _, ok := holders[idA]; ok {
		t.Fatalf("甲已转让，不应再有 10 月分成: %v", holders)
	}
	// 丙 40%: 1200*0.4*0.4 = 192（无补贴无运维无维修）
	if holders[idC] != 192 {
		t.Fatalf("丙 10 月分成=%v want 192", holders[idC])
	}
	c.do("POST", fmt.Sprintf("/api/settlements/%d/finalize", octID), admin, nil)

	// 公告、异议投票、异常解释
	if st, _ := c.do("POST", fmt.Sprintf("/api/buildings/%d/announcements", bid), admin,
		map[string]string{"title": "月度公示", "content": "见结算", "pinned": "x"}); st != 201 {
		// pinned 传字符串会被 bool 解析失败 → 改成 bool 再试
	}
	if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/announcements", bid), admin,
		map[string]any{"title": "月度公示", "content": "见结算", "pinned": true}); st != 201 {
		t.Fatalf("announcement: %d %v", st, out)
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/polls", bid), tB,
		map[string]string{"title": "异议：收益偏低", "content": "要求复核"})
	pollID := int64(out["id"].(float64))
	c.do("POST", fmt.Sprintf("/api/polls/%d/vote", pollID), tA, map[string]bool{"support": true})
	c.do("POST", fmt.Sprintf("/api/polls/%d/vote", pollID), tB, map[string]bool{"support": true})
	st, out = c.do("POST", fmt.Sprintf("/api/polls/%d/close", pollID), admin, nil)
	if out["result"] != "approved" {
		t.Fatalf("异议投票应成立: %v", out)
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/alerts", bid), tA, map[string]any{
		"period_month": "2026-10", "inverter_id": inv2, "reason": "2# 停机",
	})
	alertID := int64(out["id"].(float64))
	if st, out := c.do("POST", fmt.Sprintf("/api/alerts/%d/explain", alertID), admin,
		map[string]string{"explanation": "电网检修限发 2 天"}); st != 200 {
		t.Fatalf("explain: %d %v", st, out)
	}

	// 年度报告
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/annual-reports", bid), admin,
		map[string]int{"year": 2026})
	if st != 200 {
		t.Fatalf("annual report: %d %v", st, out)
	}
	rid := int64(out["id"].(float64))
	summary := out["summary"].(map[string]any)
	if summary["finalized_months"].(float64) != 2 {
		t.Fatalf("年报月份数=%v", summary["finalized_months"])
	}
	if st, out := c.do("POST", fmt.Sprintf("/api/annual-reports/%d/publish", rid), admin, nil); st != 200 {
		t.Fatalf("publish: %d %v", st, out)
	}

	// 仪表盘
	st, out = c.do("GET", fmt.Sprintf("/api/buildings/%d/dashboard", bid), admin, nil)
	if st != 200 || out["total_kwh"].(float64) != 3000 {
		t.Fatalf("dashboard: %d %v", st, out)
	}

	// 阶段回退必须有原因
	if st, _ = c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
		map[string]string{"to_stage": "construction"}); st != 409 {
		t.Fatalf("无原因回退应 409, got %d", st)
	}
	if st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
		map[string]string{"to_stage": "construction", "reason": "设备供应延迟，组件到货推迟"}); st != 200 {
		t.Fatalf("有原因回退应 200: %d %v", st, out)
	}

	// ===== 顶楼雨后漏水投诉：证据链 + 临时处置 + 相关组件收益冻结 + 按责任比例释放 =====
	var fz, meC map[string]any
	// 甲已把房屋及认购权益转给丙，丙现为 3-601 顶楼住户与 40% 组件持有人
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/work-orders", bid), tC, map[string]any{
		"period_month": "2026-11", "title": "新装支架位置顶板滴水", "wo_type": "leak",
		"description": "暴雨后东侧新装支架锚固点下方滴水",
	})
	if st != 201 {
		t.Fatalf("新漏水工单: %d %v", st, out)
	}
	leakID := int64(out["id"].(float64))

	// 四类证据 + 第三方报告
	evidences := []map[string]any{
		{"evidence_type": "construction_photo", "title": "支架穿层节点施工照片", "recorded_on": "2026-07-01", "source": "安装商"},
		{"evidence_type": "waterproof_acceptance", "title": "屋顶防水验收记录", "detail": "蓄水试验合格，穿层节点五年质保", "recorded_on": "2026-07-05"},
		{"evidence_type": "rainfall", "title": "11月降雨记录", "detail": "小时雨强 22mm/h", "recorded_on": "2026-11-08", "amount": 68.5, "source": "气象局"},
		{"evidence_type": "consent", "title": "顶楼住户施工同意书", "detail": "业主已签署，但东侧加装支架未单独征询", "recorded_on": "2026-06-01"},
	}
	for _, e := range evidences {
		if st, out := c.do("POST", fmt.Sprintf("/api/work-orders/%d/evidences", leakID), tC, e); st != 201 {
			t.Fatalf("关联证据 %v: %d %v", e["evidence_type"], st, out)
		}
	}
	// 责任无法确认：平台安排临时止水、住户安抚、第三方检测
	for _, a := range []map[string]any{
		{"action_type": "temporary_seal", "content": "铺设临时防水卷材，导流止水", "cost": 300},
		{"action_type": "resident_reassurance", "content": "上门安抚并垫付住宿", "cost": 600},
		{"action_type": "third_party_inspection", "content": "第三方开孔检测穿层密封", "cost": 1500},
	} {
		if st, out := c.do("POST", fmt.Sprintf("/api/work-orders/%d/actions", leakID), admin, a); st != 201 {
			t.Fatalf("处置 %v: %d %v", a["action_type"], st, out)
		}
	}
	// 非法证据/处置类型应 400
	if st, _ := c.do("POST", fmt.Sprintf("/api/work-orders/%d/evidences", leakID), tC,
		map[string]string{"evidence_type": "bogus", "title": "x"}); st != 400 {
		t.Fatalf("非法证据类型应 400, got %d", st)
	}
	if st, _ := c.do("POST", fmt.Sprintf("/api/work-orders/%d/actions", leakID), tC,
		map[string]string{"action_type": "temporary_seal", "content": "住户不能安排处置"}); st != 403 {
		t.Fatalf("住户安排处置应 403, got %d", st)
	}

	// 工单详情聚合证据链/处置过程
	st, out = c.do("GET", fmt.Sprintf("/api/work-orders/%d", leakID), admin, nil)
	if st != 200 || len(out["evidences"].([]any)) != 4 || len(out["actions"].([]any)) != 3 {
		t.Fatalf("工单详情证据/处置缺失: %v", out)
	}

	// 设立定向收益冻结（相关组件持有人=丙），自 11 月起
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/freezes", bid), admin, map[string]any{
		"work_order_id": leakID, "period_month": "2026-11", "scope": "targeted",
		"target_user_ids": []int64{idC},
	})
	if st != 201 {
		t.Fatalf("创建冻结单: %d %v", st, out)
	}
	freezeID := int64(out["id"].(float64))
	// 同一工单重复冻结应 409
	if st, _ = c.do("POST", fmt.Sprintf("/api/buildings/%d/freezes", bid), admin, map[string]any{
		"work_order_id": leakID, "period_month": "2026-11",
	}); st != 409 {
		t.Fatalf("重复冻结应 409, got %d", st)
	}

	// 11 月发电与结算：丙 40% 分成 192 应被冻结挂账，不进余额
	if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
		map[string]string{"to_stage": "grid_acceptance"}); st != 200 {
		t.Fatalf("恢复至并网验收: %d %v", st, out)
	}
	if st, out := c.do("POST", fmt.Sprintf("/api/buildings/%d/stage", bid), admin,
		map[string]string{"to_stage": "operating", "reason": "设备到货，恢复运营"}); st != 200 {
		t.Fatalf("恢复运营: %d %v", st, out)
	}
	for _, x := range []struct {
		inv int64
		kwh float64
	}{{inv1, 600}, {inv2, 600}} {
		c.do("POST", fmt.Sprintf("/api/buildings/%d/generation", bid), admin, map[string]any{
			"period_month": "2026-11", "inverter_id": x.inv, "kwh": x.kwh,
		})
	}
	st, out = c.do("POST", fmt.Sprintf("/api/buildings/%d/settlements", bid), admin, map[string]any{
		"period_month": "2026-11", "grid_price": 0.4,
	})
	if st != 200 {
		t.Fatalf("11月结算: %d %v", st, out)
	}
	novID := int64(out["id"].(float64))
	if st, _ := c.do("POST", fmt.Sprintf("/api/settlements/%d/finalize", novID), admin, nil); st != 200 {
		t.Fatalf("11月定稿应 200, got %d", st)
	}
	// 冻结挂账 192，丙余额仍为 10 月的 192
	st, fz = c.do("GET", fmt.Sprintf("/api/freezes/%d", freezeID), admin, nil)
	if fz["frozen_total"].(float64) != 192 {
		t.Fatalf("冻结挂账=%v want 192", fz["frozen_total"])
	}
	if len(fz["items"].([]any)) != 1 {
		t.Fatalf("挂账明细应有 1 条: %v", fz["items"])
	}
	st, meC = c.do("GET", "/api/me", tC, nil)
	if meC["balance"].(string) != "192.00" {
		t.Fatalf("冻结期间丙余额应保持 192.00, got %v", meC["balance"])
	}
	// 结算明细中丙的分成带冻结挂账标记
	st, sd := c.do("GET", fmt.Sprintf("/api/settlements/%d", novID), admin, nil)
	var cFrozen float64
	for _, it := range sd["items"].([]any) {
		m := it.(map[string]any)
		if m["item_type"] == "shareholder" && int64(m["user_id"].(float64)) == idC {
			cFrozen = m["frozen_amount"].(float64)
		}
	}
	if cFrozen != 192 {
		t.Fatalf("丙的结算行冻结标记=%v want 192", cFrozen)
	}

	// 责任比例非法（合计 90）应 400
	if st, _ = c.do("POST", fmt.Sprintf("/api/freezes/%d/release", freezeID), admin, map[string]any{
		"installer_pct": 60, "fund_pct": 10, "public_pct": 20, "owner_pct": 0,
	}); st != 400 {
		t.Fatalf("比例合计 90 应 400, got %d", st)
	}
	// 责任确认：施工方 60% / 运维基金 10% / 公共收益 20% / 业主自担 10%
	st, out = c.do("POST", fmt.Sprintf("/api/freezes/%d/release", freezeID), admin, map[string]any{
		"installer_pct": 60, "fund_pct": 10, "public_pct": 20, "owner_pct": 10,
		"liability_note": "第三方检测认定穿层密封不达标，施工方主责",
	})
	if st != 200 {
		t.Fatalf("按比例释放: %d %v", st, out)
	}
	// 192: 施工方 115.20 + 基金 19.20 + 公共 38.40 = 172.80 发放；业主自担 19.20
	if out["released_to_owner"].(float64) != 172.8 || out["owner_borne"].(float64) != 19.2 ||
		out["installer_compensation"].(float64) != 115.2 {
		t.Fatalf("释放金额错误: %v", out)
	}
	st, meC = c.do("GET", "/api/me", tC, nil)
	if meC["balance"].(string) != "364.80" {
		t.Fatalf("释放后丙余额=192+172.80=364.80, got %v", meC["balance"])
	}
	// 冻结单已释放，重复释放 409
	if st, _ = c.do("POST", fmt.Sprintf("/api/freezes/%d/release", freezeID), admin,
		map[string]any{"installer_pct": 100}); st != 409 {
		t.Fatalf("重复释放应 409, got %d", st)
	}
	// 处理结论写入后续屋顶巡检计划
	st, ins := c.do("GET", fmt.Sprintf("/api/buildings/%d/inspections", bid), admin, nil)
	if st != 200 || len(ins["items"].([]any)) == 0 {
		t.Fatalf("应生成后续巡检计划: %v", ins)
	}
	found := false
	for _, it := range ins["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["work_order_id"].(float64)) == leakID && m["status"] == "scheduled" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未找到关联漏水工单 %d 的待巡检计划", leakID)
	}
	// 登记巡检完成
	inspID := int64(ins["items"].([]any)[0].(map[string]any)["id"].(float64))
	if st, _ := c.do("POST", fmt.Sprintf("/api/inspections/%d", inspID), admin,
		map[string]string{"status": "done", "result_note": "未见复渗"}); st != 200 {
		t.Fatalf("登记巡检完成应 200, got %d", st)
	}
}
