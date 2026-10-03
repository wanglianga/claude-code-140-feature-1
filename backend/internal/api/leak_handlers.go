package api

import (
	"net/http"
	"strconv"
	"time"

	"pvplatform/internal/auth"
	"pvplatform/internal/settlement"
)

// 本文件实现顶楼漏水投诉的扩展链路：
//   工单证据（施工照片/防水验收/雨量/同意签署/第三方报告）、
//   处置过程（临时止水/住户安抚/第三方检测）、
//   维修期间相关组件收益冻结与责任确认后按比例释放、
//   处理结论写入后续屋顶巡检计划。

var evidenceTypes = map[string]bool{
	"construction_photo":    true,
	"waterproof_acceptance": true,
	"rainfall":              true,
	"consent":               true,
	"inspection_report":     true,
}

var actionTypes = map[string]bool{
	"temporary_seal":         true,
	"resident_reassurance":   true,
	"third_party_inspection": true,
	"repair":                 true,
	"other":                  true,
}

// getWorkOrder 工单详情：基本信息 + 证据链 + 处置过程 + 收益冻结单
func (s *Server) getWorkOrder(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	wid := urlID(r, "wid")
	var bid, invID, reporterID int64
	var month, title, desc, wtype, resp, status, assignee string
	var cost float64
	var createdAt, resolvedAt any
	err := s.Pool.QueryRow(r.Context(), `
		SELECT id, building_id, COALESCE(inverter_id,0), period_month, title,
			COALESCE(description,''), wo_type, responsibility, cost, status, COALESCE(assignee,''),
			COALESCE(reporter_id,0), created_at, resolved_at
		FROM work_orders WHERE id=$1`, wid).
		Scan(&wid, &bid, &invID, &month, &title, &desc, &wtype, &resp, &cost, &status,
			&assignee, &reporterID, &createdAt, &resolvedAt)
	if isNoRows(err) {
		writeErr(w, 404, "工单不存在")
		return
	} else if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := map[string]any{
		"id": wid, "building_id": bid, "inverter_id": invID, "period_month": month,
		"title": title, "description": desc, "wo_type": wtype, "responsibility": resp,
		"cost": cost, "status": status, "assignee": assignee, "reporter_id": reporterID,
		"created_at": createdAt, "resolved_at": resolvedAt,
		"evidences": s.listEvidenceRows(r, wid),
		"actions":   s.listActionRows(r, wid),
	}
	if f := s.freezeOfWorkOrder(r, wid); f != nil {
		out["freeze"] = f
	}
	writeJSON(w, 200, out)
}

func (s *Server) listEvidenceRows(r *http.Request, wid int64) []any {
	rows, err := s.Pool.Query(r.Context(), `
		SELECT e.id, e.evidence_type, COALESCE(e.title,''), COALESCE(e.detail,''),
			COALESCE(to_char(e.recorded_on,'YYYY-MM-DD'),''), COALESCE(e.amount,0),
			COALESCE(e.source,''), COALESCE(e.file_url,''), COALESCE(u.name,''), e.created_at
		FROM work_order_evidences e LEFT JOIN users u ON u.id=e.created_by
		WHERE e.work_order_id=$1 ORDER BY e.id`, wid)
	if err != nil {
		return []any{}
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id int64
		var etype, t, detail, on, source, url, author string
		var amount float64
		var at any
		if err := rows.Scan(&id, &etype, &t, &detail, &on, &amount, &source, &url, &author, &at); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "evidence_type": etype, "title": t, "detail": detail,
			"recorded_on": on, "amount": amount, "source": source,
			"file_url": url, "author": author, "created_at": at,
		})
	}
	return out
}

func (s *Server) listActionRows(r *http.Request, wid int64) []any {
	rows, err := s.Pool.Query(r.Context(), `
		SELECT a.id, a.action_type, COALESCE(a.content,''), a.cost, COALESCE(u.name,''), a.created_at
		FROM work_order_actions a LEFT JOIN users u ON u.id=a.operator_id
		WHERE a.work_order_id=$1 ORDER BY a.id`, wid)
	if err != nil {
		return []any{}
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id int64
		var atype, content, operator string
		var cost float64
		var at any
		if err := rows.Scan(&id, &atype, &content, &cost, &operator, &at); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "action_type": atype, "content": content,
			"cost": cost, "operator": operator, "created_at": at,
		})
	}
	return out
}

// addWorkOrderEvidence 关联证据材料（施工照片、防水验收、雨量记录、同意签署、第三方报告）
func (s *Server) addWorkOrderEvidence(w http.ResponseWriter, r *http.Request) {
	u := auth.Require(w, r)
	if u == nil {
		return
	}
	wid := urlID(r, "wid")
	var in struct {
		EvidenceType string   `json:"evidence_type"`
		Title        string   `json:"title"`
		Detail       string   `json:"detail"`
		RecordedOn   string   `json:"recorded_on"`
		Amount       *float64 `json:"amount"`
		Source       string   `json:"source"`
		FileURL      string   `json:"file_url"`
	}
	if err := decodeJSON(r, &in); err != nil || !evidenceTypes[in.EvidenceType] {
		writeErr(w, 400, "evidence_type 不合法（construction_photo/waterproof_acceptance/rainfall/consent/inspection_report）")
		return
	}
	var bid int64
	if err := s.Pool.QueryRow(r.Context(), `SELECT building_id FROM work_orders WHERE id=$1`, wid).Scan(&bid); err != nil {
		writeErr(w, 404, "工单不存在")
		return
	}
	var id int64
	err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO work_order_evidences(work_order_id,building_id,evidence_type,title,detail,recorded_on,amount,source,file_url,created_by)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::date,$7,$8,$9,$10) RETURNING id`,
		wid, bid, in.EvidenceType, in.Title, in.Detail, in.RecordedOn, in.Amount,
		in.Source, in.FileURL, u.ID).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// addWorkOrderAction 记录处置过程（临时止水/住户安抚/第三方检测）；平台（物业/管理员）安排
func (s *Server) addWorkOrderAction(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	wid := urlID(r, "wid")
	var in struct {
		ActionType string  `json:"action_type"`
		Content    string  `json:"content"`
		Cost       float64 `json:"cost"`
	}
	if err := decodeJSON(r, &in); err != nil || !actionTypes[in.ActionType] {
		writeErr(w, 400, "action_type 不合法（temporary_seal/resident_reassurance/third_party_inspection/repair/other）")
		return
	}
	var bid int64
	if err := s.Pool.QueryRow(r.Context(), `SELECT building_id FROM work_orders WHERE id=$1`, wid).Scan(&bid); err != nil {
		writeErr(w, 404, "工单不存在")
		return
	}
	var id int64
	err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO work_order_actions(work_order_id,building_id,action_type,content,cost,operator_id)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		wid, bid, in.ActionType, in.Content, in.Cost, u.ID).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// freezeOfWorkOrder 返回工单关联的冻结单（无则 nil）
func (s *Server) freezeOfWorkOrder(r *http.Request, wid int64) map[string]any {
	var f map[string]any
	row := s.Pool.QueryRow(r.Context(), freezeSelect+` FROM revenue_freezes WHERE work_order_id=$1`, wid)
	if m, ok := scanFreeze(row); ok {
		f = m
	}
	return f
}

const freezeSelect = `
	SELECT id, building_id, work_order_id, period_month, COALESCE(title,''), scope, status,
		COALESCE(installer_pct,0), COALESCE(fund_pct,0), COALESCE(public_pct,0), COALESCE(owner_pct,0),
		COALESCE(liability_note,''), COALESCE(frozen_total,0), COALESCE(released_to_owner,0),
		COALESCE(owner_borne_total,0), COALESCE(installer_compensation,0), COALESCE(fund_borne,0),
		COALESCE(public_borne,0), created_at, released_at`

type freezeScanner interface {
	Scan(dest ...any) error
}

func scanFreeze(row freezeScanner) (map[string]any, bool) {
	var id, bid, wid int64
	var month, title, scope, status, note string
	var instPct, fundPct, pubPct, ownPct float64
	var frozenTotal, released, ownerBorne, instComp, fundBorne, pubBorne float64
	var createdAt, releasedAt any
	if err := row.Scan(&id, &bid, &wid, &month, &title, &scope, &status,
		&instPct, &fundPct, &pubPct, &ownPct, &note, &frozenTotal, &released,
		&ownerBorne, &instComp, &fundBorne, &pubBorne, &createdAt, &releasedAt); err != nil {
		return nil, false
	}
	return map[string]any{
		"id": id, "building_id": bid, "work_order_id": wid, "period_month": month,
		"title": title, "scope": scope, "status": status,
		"installer_pct": instPct, "fund_pct": fundPct, "public_pct": pubPct, "owner_pct": ownPct,
		"liability_note": note, "frozen_total": frozenTotal, "released_to_owner": released,
		"owner_borne_total": ownerBorne, "installer_compensation": instComp,
		"fund_borne": fundBorne, "public_borne": pubBorne,
		"created_at": createdAt, "released_at": releasedAt,
	}, true
}

// createFreeze 维修期间对相关组件收益设立冻结
func (s *Server) createFreeze(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	bid := urlID(r, "id")
	var in struct {
		WorkOrderID   int64   `json:"work_order_id"`
		PeriodMonth   string  `json:"period_month"`
		Title         string  `json:"title"`
		Scope         string  `json:"scope"` // targeted=相关组件持有人 / all=整楼全部组件
		TargetUserIDs []int64 `json:"target_user_ids"`
	}
	if err := decodeJSON(r, &in); err != nil || in.WorkOrderID == 0 || in.PeriodMonth == "" {
		writeErr(w, 400, "work_order_id、period_month 必填")
		return
	}
	if len(in.PeriodMonth) != 7 {
		writeErr(w, 400, "period_month 格式应为 YYYY-MM")
		return
	}
	if in.Scope == "" {
		in.Scope = "targeted"
	}
	if in.Scope != "targeted" && in.Scope != "all" {
		writeErr(w, 400, "scope 仅支持 targeted / all")
		return
	}
	var woBid int64
	var wtype, wstatus string
	var reporter int64
	if err := s.Pool.QueryRow(r.Context(),
		`SELECT building_id, wo_type, status, COALESCE(reporter_id,0) FROM work_orders WHERE id=$1`,
		in.WorkOrderID).Scan(&woBid, &wtype, &wstatus, &reporter); err != nil {
		writeErr(w, 404, "工单不存在")
		return
	}
	if woBid != bid {
		writeErr(w, 400, "工单不属于该楼栋")
		return
	}
	if wtype != "leak" {
		writeErr(w, 400, "收益冻结用于顶楼漏水工单，当前工单类型："+wtype)
		return
	}
	if wstatus == "resolved" {
		writeErr(w, 409, "工单已结案，无需再冻结收益")
		return
	}
	// targeted：显式目标住户优先，未提供则回退为报修人；校验均为本楼在持认购住户
	var targets []int64
	if in.Scope == "targeted" {
		if len(in.TargetUserIDs) > 0 {
			targets = in.TargetUserIDs
		} else if reporter != 0 {
			targets = []int64{reporter}
		}
		if len(targets) == 0 {
			writeErr(w, 400, "定向冻结需指定相关组件持有人（target_user_ids）")
			return
		}
		var valid int
		if err := s.Pool.QueryRow(r.Context(), `
			SELECT count(DISTINCT user_id) FROM subscriptions
			WHERE building_id=$1 AND sub_type='shareholder' AND status IN ('paid','transferred')
			  AND user_id = ANY($2)`, bid, targets).Scan(&valid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if valid == 0 {
			writeErr(w, 409, "目标住户在本楼无在持认购组件，定向冻结无对象；请改用整楼范围（scope=all）")
			return
		}
	}
	var exists int
	_ = s.Pool.QueryRow(r.Context(),
		`SELECT count(*) FROM revenue_freezes WHERE work_order_id=$1`, in.WorkOrderID).Scan(&exists)
	if exists > 0 {
		writeErr(w, 409, "该工单已设立收益冻结单")
		return
	}
	title := in.Title
	if title == "" {
		title = "漏水维修期间相关组件收益冻结"
	}
	tx, err := s.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	err = tx.QueryRow(r.Context(), `
		INSERT INTO revenue_freezes(building_id,work_order_id,period_month,title,scope,created_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		bid, in.WorkOrderID, in.PeriodMonth, title, in.Scope, u.ID).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, uid := range targets {
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO freeze_targets(freeze_id,user_id) VALUES ($1,$2)
			 ON CONFLICT DO NOTHING`, id, uid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) listFreezes(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	bid := urlID(r, "id")
	rows, err := s.Pool.Query(r.Context(),
		freezeSelect+` FROM revenue_freezes WHERE building_id=$1 ORDER BY id DESC`, bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		if m, ok := scanFreeze(rows); ok {
			out = append(out, m)
		}
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// getFreeze 冻结单详情，含每笔挂账明细
func (s *Server) getFreeze(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	fid := urlID(r, "fid")
	row := s.Pool.QueryRow(r.Context(), freezeSelect+` FROM revenue_freezes WHERE id=$1`, fid)
	f, ok := scanFreeze(row)
	if !ok {
		writeErr(w, 404, "冻结单不存在")
		return
	}
	rows, err := s.Pool.Query(r.Context(), `
		SELECT fl.id, fl.user_id, COALESCE(u.name,''), COALESCE(u.unit_no,''), fl.period_month,
			fl.amount, fl.released_amount, fl.owner_borne, fl.status, fl.created_at, fl.released_at
		FROM freeze_ledger fl JOIN users u ON u.id=fl.user_id
		WHERE fl.freeze_id=$1 ORDER BY fl.id`, fid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		var id, uid int64
		var name, unit, month, st string
		var amount, released, borne float64
		var at, rat any
		if err := rows.Scan(&id, &uid, &name, &unit, &month, &amount, &released, &borne, &st, &at, &rat); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id": id, "user_id": uid, "name": name, "unit_no": unit, "period_month": month,
			"amount": amount, "released_amount": released, "owner_borne": borne,
			"status": st, "created_at": at, "released_at": rat,
		})
	}
	f["items"] = items
	// 定向冻结的相关组件持有人（scope=all 时为空，表示整楼全部组件）
	trows, terr := s.Pool.Query(r.Context(), `
		SELECT ft.user_id, u.name, COALESCE(u.unit_no,'')
		FROM freeze_targets ft JOIN users u ON u.id=ft.user_id
		WHERE ft.freeze_id=$1`, fid)
	targets := []any{}
	if terr == nil {
		for trows.Next() {
			var uid int64
			var name, unit string
			if trows.Scan(&uid, &name, &unit) == nil {
				targets = append(targets, map[string]any{"user_id": uid, "name": name, "unit_no": unit})
			}
		}
		trows.Close()
	}
	f["targets"] = targets
	writeJSON(w, 200, f)
}

// releaseFreeze 责任确认后按比例释放冻结收益：
// 施工方赔付 / 运维(维修)基金承担 / 楼栋公共收益支付 / 业主自担，
// 非业主责任部分发放回住户，业主自担部分冲减；处理结论写入后续屋顶巡检计划。
func (s *Server) releaseFreeze(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	fid := urlID(r, "fid")
	var in struct {
		InstallerPct  *float64 `json:"installer_pct"`
		FundPct       *float64 `json:"fund_pct"`
		PublicPct     *float64 `json:"public_pct"`
		OwnerPct      *float64 `json:"owner_pct"`
		LiabilityNote string   `json:"liability_note"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	pct := func(p *float64) float64 {
		if p == nil {
			return 0
		}
		return *p
	}
	split := settlement.LiabilitySplit{
		InstallerPct: pct(in.InstallerPct),
		FundPct:      pct(in.FundPct),
		PublicPct:    pct(in.PublicPct),
		OwnerPct:     pct(in.OwnerPct),
	}
	if in.OwnerPct == nil {
		split.OwnerPct = 100 - split.InstallerPct - split.FundPct - split.PublicPct
	}
	if !split.Valid() {
		writeErr(w, 400, "责任比例须非负且合计为 100%（施工方+运维基金+公共收益+业主自担）")
		return
	}

	var bid, wid int64
	var fstatus, month string
	if err := s.Pool.QueryRow(r.Context(),
		`SELECT building_id, work_order_id, status, period_month FROM revenue_freezes WHERE id=$1`, fid).
		Scan(&bid, &wid, &fstatus, &month); err != nil {
		writeErr(w, 404, "冻结单不存在")
		return
	}
	if fstatus != "frozen" {
		writeErr(w, 409, "冻结单已释放")
		return
	}

	tx, err := s.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	// 挂账明细
	rows, err := tx.Query(r.Context(),
		`SELECT id, user_id, period_month, amount FROM freeze_ledger WHERE freeze_id=$1 AND status='held' FOR UPDATE`,
		fid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type held struct {
		id     int64
		uid    int64
		month  string
		amount float64
	}
	var helds []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.uid, &h.month, &h.amount); err != nil {
			rows.Close()
			writeErr(w, 500, err.Error())
			return
		}
		helds = append(helds, h)
	}
	rows.Close()

	// 预估维修基金承担额度，不足则拒绝（防止基金透支）
	var fundWillBear float64
	var totalHeld float64
	for _, h := range helds {
		rel := settlement.ReleaseFreeze(h.amount, split)
		fundWillBear += rel.FundPart
		totalHeld += h.amount
	}
	var fundBal float64
	if err := tx.QueryRow(r.Context(), `
		SELECT COALESCE(SUM(CASE WHEN direction='in' THEN amount ELSE -amount END),0)
		FROM ledger WHERE building_id=$1 AND category='maintenance_fund'`, bid).Scan(&fundBal); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if fundWillBear > fundBal+0.001 {
		writeErr(w, 409, "维修(运维)基金余额不足以承担其责任比例，请调整比例或先补充基金")
		return
	}

	led := func(uid any, dir, cat string, amount float64, pm, memo string) {
		if amount == 0 {
			return
		}
		_, _ = tx.Exec(r.Context(), `
			INSERT INTO ledger(building_id,user_id,direction,amount,category,period_month,ref_type,ref_id,memo)
			VALUES ($1,$2,$3,$4,$5,$6,'freeze',$7,$8)`,
			bid, uid, dir, amount, cat, pm, fid, memo)
	}

	var sumInstaller, sumFund, sumPublic, sumOwner, sumReleased float64
	for _, h := range helds {
		rel := settlement.ReleaseFreeze(h.amount, split)
		if _, err := tx.Exec(r.Context(), `
			UPDATE freeze_ledger SET released_amount=$2, owner_borne=$3, status='released', released_at=now()
			WHERE id=$1`, h.id, rel.ReleasedToOwner, rel.OwnerBorne); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if rel.ReleasedToOwner > 0 {
			if _, err := tx.Exec(r.Context(),
				`UPDATE users SET balance=balance+$2 WHERE id=$1`, h.uid, rel.ReleasedToOwner); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		// 住户侧台账：非业主责任部分解冻入账；业主自担部分冲减挂账
		led(h.uid, "in", "freeze_release", rel.ReleasedToOwner, h.month, h.month+" 漏水责任确认，冻结收益按比例解冻")
		led(h.uid, "out", "freeze_deduction", rel.OwnerBorne, h.month, h.month+" 漏水业主自担部分，冲减冻结收益")
		sumInstaller += rel.InstallerPart
		sumFund += rel.FundPart
		sumPublic += rel.PublicPart
		sumOwner += rel.OwnerBorne
		sumReleased += rel.ReleasedToOwner
	}

	// 楼级账户：施工方赔付到账 → 冻结现金发放给住户；维修基金动用记专款标记
	if sumInstaller > 0 {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO payments(building_id,user_id,amount,pay_type,memo)
			VALUES ($1,NULL,$2,'compensation',$3)`,
			bid, sumInstaller, "楼顶漏水施工方责任赔付"); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		led(nil, "in", "compensation", sumInstaller, month, "施工方(安装商)漏水赔付到账")
	}
	led(nil, "out", "freeze_release", sumReleased, month, "冻结收益按责任结论发放（施工方+基金+公共收益）")
	led(nil, "out", "maintenance_fund", sumFund, month, "维修(运维)基金承担漏水责任（专款标记）")

	if _, err := tx.Exec(r.Context(), `
		UPDATE revenue_freezes SET status='released', released_at=now(),
			installer_pct=$2, fund_pct=$3, public_pct=$4, owner_pct=$5, liability_note=$6,
			released_to_owner=$7, owner_borne_total=$8, installer_compensation=$9,
			fund_borne=$10, public_borne=$11
		WHERE id=$1`,
		fid, split.InstallerPct, split.FundPct, split.PublicPct, split.OwnerPct, in.LiabilityNote,
		sumReleased, sumOwner, sumInstaller, sumFund, sumPublic); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	// 处理结论写入后续屋顶巡检计划（30 天后重点复查渗漏点）
	if _, err := tx.Exec(r.Context(), `
		INSERT INTO roof_inspections(building_id,work_order_id,scheduled_date,title,focus_area,conclusion,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		bid, wid, time.Now().AddDate(0, 0, 30),
		"漏水处理结论后专项屋顶巡检",
		"支架穿层防水节点、顶板线管周边、防水卷材收口",
		"责任比例：施工方"+pctText(split.InstallerPct)+
			"% / 运维基金"+pctText(split.FundPct)+
			"% / 公共收益"+pctText(split.PublicPct)+
			"% / 业主自担"+pctText(split.OwnerPct)+
			"%。"+in.LiabilityNote,
		u.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "held_total": totalHeld, "released_to_owner": sumReleased,
		"owner_borne": sumOwner, "installer_compensation": sumInstaller,
		"fund_borne": sumFund, "public_borne": sumPublic,
	})
}

func pctText(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
