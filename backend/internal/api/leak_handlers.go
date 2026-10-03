package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"pvplatform/internal/auth"
)

// 顶楼漏水投诉处置与收益冻结：
//   - 工单证据（施工照片、屋顶防水验收、雨量记录、同意书、第三方检测报告）
//   - 临时止水 / 住户安抚 / 第三方检测 / 处理结论
//   - 顶楼住户屋顶使用同意书
//   - 相关组件收益暂时冻结，责任确认后按比例释放：施工方赔付 / 运维基金 / 楼栋公共收益 / 业主自负
//   - 处理结论写入后续屋顶巡检计划

var attachmentKinds = map[string]bool{
	"construction_photo":    true, // 施工照片
	"waterproof_acceptance": true, // 屋顶防水验收
	"rainfall":              true, // 雨量记录
	"consent":               true, // 住户同意书
	"third_party_report":    true, // 第三方检测报告
	"onsite_photo":          true, // 现场渗水照片
	"other":                 true,
}

// ensureFollowupInspection 为漏水工单生成一条 30 天后的屋顶复查计划（同一工单不重复生成）。
func ensureFollowupInspection(ctx context.Context, tx pgx.Tx, bid, wid, operator int64) error {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM roof_inspections WHERE work_order_id=$1`, wid).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	planDate := time.Now().AddDate(0, 0, 30)
	_, err := tx.Exec(ctx, `
		INSERT INTO roof_inspections(building_id,work_order_id,title,plan_date,focus,created_by)
		VALUES ($1,$2,'漏水维修后屋顶防水复查（支架穿层/卷材搭接）',$3,
			'复查支架穿层封堵、防水卷材搭接、雨水管口，雨后重点观察投诉点位',$4)`,
		bid, wid, planDate, operator)
	return err
}

// attachWorkOrder 为工单关联一条证据
func (s *Server) attachWorkOrder(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	wid := urlID(r, "wid")
	var in struct {
		Kind       string `json:"kind"`
		Title      string `json:"title"`
		URL        string `json:"url"`
		Detail     string `json:"detail"`
		RecordedAt string `json:"recorded_at"` // 可选 YYYY-MM-DD
	}
	if err := decodeJSON(r, &in); err != nil || !attachmentKinds[in.Kind] {
		writeErr(w, 400, "kind 不合法（construction_photo/waterproof_acceptance/rainfall/consent/third_party_report/onsite_photo/other）")
		return
	}
	var bid int64
	if err := s.Pool.QueryRow(r.Context(), `SELECT building_id FROM work_orders WHERE id=$1`, wid).Scan(&bid); err != nil {
		if isNoRows(err) {
			writeErr(w, 404, "工单不存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	recorded := time.Now()
	if in.RecordedAt != "" {
		if t, err := time.Parse("2006-01-02", in.RecordedAt); err == nil {
			recorded = t
		}
	}
	var id int64
	if err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO work_order_attachments(work_order_id,building_id,kind,title,url,detail,recorded_at,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
		wid, bid, in.Kind, in.Title, in.URL, in.Detail, recorded, u.ID).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// listAttachments 工单证据列表
func (s *Server) listAttachments(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	wid := urlID(r, "wid")
	rows, err := s.Pool.Query(r.Context(), `
		SELECT a.id, a.kind, COALESCE(a.title,''), COALESCE(a.url,''), COALESCE(a.detail,''),
			a.recorded_at, COALESCE(cu.name,''), a.created_at
		FROM work_order_attachments a
		LEFT JOIN users cu ON cu.id=a.created_by
		WHERE a.work_order_id=$1
		ORDER BY a.recorded_at, a.id`, wid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id int64
		var kind, title, url, detail, createdBy string
		var recordedAt, createdAt any
		if err := rows.Scan(&id, &kind, &title, &url, &detail, &recordedAt, &createdBy, &createdAt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "kind": kind, "title": title, "url": url, "detail": detail,
			"recorded_at": recordedAt, "created_by": createdBy, "created_at": createdAt,
		})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// leakAction 漏水工单处置动作：临时止水 / 住户安抚 / 第三方检测 / 处理结论
func (s *Server) leakAction(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	wid := urlID(r, "wid")
	var in struct {
		Action         string  `json:"action"`
		Note           string  `json:"note"`
		Org            string  `json:"org"`
		Responsibility string  `json:"responsibility"`
		Cost           float64 `json:"cost"`
		Allocation     []struct {
			Bearer string  `json:"bearer"`
			Ratio  float64 `json:"ratio"`
		} `json:"allocation"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	var bid int64
	var status, curResp string
	if err := s.Pool.QueryRow(r.Context(),
		`SELECT building_id, status, responsibility FROM work_orders WHERE id=$1`,
		wid).Scan(&bid, &status, &curResp); err != nil {
		if isNoRows(err) {
			writeErr(w, 404, "工单不存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}

	// 已最终结算周期不允许再补处置结论（与工单费用/责任锁定口径一致）
	if in.Action == "conclude" {
		var finalized string
		_ = s.Pool.QueryRow(r.Context(),
			`SELECT COALESCE(sp.status,'') FROM work_orders w
			 LEFT JOIN settlement_periods sp ON sp.building_id=w.building_id AND sp.period_month=w.period_month
			 WHERE w.id=$1`, wid).Scan(&finalized)
		if finalized == "finalized" {
			writeErr(w, 409, "该周期已完成月度结算，责任结论请在次月调整或通过冻结收益释放处理")
			return
		}
	}

	tx, err := s.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	setProcessing := func() error {
		if status == "open" {
			if _, err := tx.Exec(r.Context(),
				`UPDATE work_orders SET status='processing' WHERE id=$1 AND status='open'`, wid); err != nil {
				return err
			}
		}
		return nil
	}
	appendDesc := func(tag, note string) error {
		if note == "" {
			return nil
		}
		_, err := tx.Exec(r.Context(),
			`UPDATE work_orders SET
				description=description||CASE WHEN description='' THEN '' ELSE E'\n' END||$2
			 WHERE id=$1`, wid, "【"+tag+"】"+note)
		return err
	}

	switch in.Action {
	case "temporary_stoppage":
		if err := setProcessing(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE work_orders SET temporary_stoppage_at=now() WHERE id=$1`, wid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := appendDesc("临时止水", in.Note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	case "reassure":
		if err := setProcessing(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE work_orders SET reassured_at=now() WHERE id=$1`, wid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := appendDesc("住户安抚", in.Note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	case "third_party_test":
		if in.Org == "" {
			writeErr(w, 400, "第三方检测机构必填")
			return
		}
		if err := setProcessing(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := tx.Exec(r.Context(),
			`UPDATE work_orders SET third_party_test_at=now(), third_party_test_org=$2, third_party_test_note=$3 WHERE id=$1`,
			wid, in.Org, in.Note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := appendDesc("第三方检测", in.Org+" "+in.Note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	case "conclude":
		if in.Note == "" {
			writeErr(w, 400, "处理结论必填")
			return
		}
		resp := in.Responsibility
		if resp == "" {
			resp = curResp
		}
		if resp != "undetermined" && resp != "property" && resp != "installer" &&
			resp != "owner" && resp != "insurance" && resp != "shared" {
			writeErr(w, 400, "责任方不合法")
			return
		}
		var allocJSON []byte
		if resp == "shared" {
			var sum float64
			for _, a := range in.Allocation {
				if a.Bearer != "installer" && a.Bearer != "fund" && a.Bearer != "public" && a.Bearer != "owner" {
					writeErr(w, 400, "分担方仅支持 installer/fund/public/owner")
					return
				}
				sum += a.Ratio
			}
			if len(in.Allocation) == 0 {
				writeErr(w, 400, "多方分担（shared）必须给出 allocation 责任比例")
				return
			}
			if sum < 99.99 || sum > 100.01 {
				writeErr(w, 400, "责任比例之和必须为 100%")
				return
			}
			allocJSON, _ = json.Marshal(in.Allocation)
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE work_orders SET conclusion_note=$2,
				responsibility=CASE WHEN $3='' THEN responsibility ELSE $3 END,
				cost=CASE WHEN $4<=0 THEN cost ELSE $4 END,
				responsibility_allocation=COALESCE(NULLIF($5,'')::jsonb, responsibility_allocation)
			WHERE id=$1`,
			wid, in.Note, resp, in.Cost, string(allocJSON)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := ensureFollowupInspection(r.Context(), tx, bid, wid, u.ID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	default:
		writeErr(w, 400, "action 仅支持 temporary_stoppage/reassure/third_party_test/conclude")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- 屋顶使用同意书 ----------

func (s *Server) listConsents(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	bid := urlID(r, "id")
	rows, err := s.Pool.Query(r.Context(), `
		SELECT c.id, c.user_id, COALESCE(u.name,''), COALESCE(NULLIF(c.unit_no,''), COALESCE(u.unit_no,'')),
			c.signed_at, COALESCE(c.document_no,''), COALESCE(c.note,'')
		FROM rooftop_consents c JOIN users u ON u.id=c.user_id
		WHERE c.building_id=$1 ORDER BY c.signed_at DESC`, bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, uid int64
		var name, unit, docNo, note string
		var signedAt any
		if err := rows.Scan(&id, &uid, &name, &unit, &signedAt, &docNo, &note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "user_id": uid, "name": name, "unit_no": unit,
			"signed_at": signedAt, "document_no": docNo, "note": note,
		})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) createConsent(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	bid := urlID(r, "id")
	var in struct {
		UserID     int64  `json:"user_id"`
		UnitNo     string `json:"unit_no"`
		SignedAt   string `json:"signed_at"`
		DocumentNo string `json:"document_no"`
		Note       string `json:"note"`
	}
	if err := decodeJSON(r, &in); err != nil || in.UserID == 0 {
		writeErr(w, 400, "user_id 必填")
		return
	}
	signedAt := time.Now()
	if in.SignedAt != "" {
		if t, err := time.Parse("2006-01-02", in.SignedAt); err == nil {
			signedAt = t
		}
	}
	var id int64
	err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO rooftop_consents(building_id,user_id,unit_no,signed_at,document_no,note)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (building_id,user_id) DO UPDATE SET
			unit_no=EXCLUDED.unit_no, signed_at=EXCLUDED.signed_at,
			document_no=EXCLUDED.document_no, note=EXCLUDED.note
		RETURNING id`,
		bid, in.UserID, in.UnitNo, signedAt, in.DocumentNo, in.Note).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// ---------- 收益冻结 ----------

type freezeAlloc struct {
	Bearer string  `json:"bearer"`
	Ratio  float64 `json:"ratio"`
}

func (s *Server) createFreeze(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	bid := urlID(r, "id")
	var in struct {
		WorkOrderID   int64   `json:"work_order_id"`
		PeriodMonth   string  `json:"period_month"`
		EstimatedCost float64 `json:"estimated_cost"`
		Note          string  `json:"note"`
		Targets       []struct {
			UserID int64   `json:"user_id"` // 0 表示公共部分
			Ratio  float64 `json:"ratio"`   // 冻结该对象当期收益的百分比
		} `json:"targets"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if in.WorkOrderID == 0 || in.PeriodMonth == "" || len(in.Targets) == 0 {
		writeErr(w, 400, "work_order_id、period_month、targets 必填")
		return
	}
	var woBid int64
	if err := s.Pool.QueryRow(r.Context(),
		`SELECT building_id FROM work_orders WHERE id=$1`, in.WorkOrderID).Scan(&woBid); err != nil {
		writeErr(w, 404, "工单不存在")
		return
	}
	if woBid != bid {
		writeErr(w, 400, "工单不属于该楼栋")
		return
	}
	hasUser := false
	for _, t := range in.Targets {
		if t.Ratio <= 0 || t.Ratio > 100 {
			writeErr(w, 400, "冻结比例须在 0~100 之间")
			return
		}
		if t.UserID > 0 {
			hasUser = true
			var ok bool
			if err := s.Pool.QueryRow(r.Context(), `
				SELECT TRUE FROM subscriptions
				WHERE building_id=$1 AND user_id=$2 AND sub_type='shareholder' AND status IN ('paid','transferred')`,
				bid, t.UserID).Scan(&ok); err != nil {
				writeErr(w, 400, "冻结对象须为本楼已缴费认购住户（user_id="+strconv.FormatInt(t.UserID, 10)+"）")
				return
			}
		}
	}
	scope := "public"
	if hasUser {
		scope = "component"
	}
	tx, err := s.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	var fid int64
	err = tx.QueryRow(r.Context(), `
		INSERT INTO revenue_freezes(building_id,work_order_id,period_month,scope,freeze_ratio,estimated_cost,status,note,created_by)
		VALUES ($1,$2,$3,$4,100,$5,'pending',$6,$7) RETURNING id`,
		bid, in.WorkOrderID, in.PeriodMonth, scope, in.EstimatedCost, in.Note, u.ID).Scan(&fid)
	if err != nil {
		writeErr(w, 409, "该工单在同一月份已存在收益冻结单，请勿重复创建")
		return
	}
	for _, t := range in.Targets {
		var uid *int64
		if t.UserID > 0 {
			uid = &t.UserID
		}
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO revenue_freeze_targets(freeze_id,user_id,ratio) VALUES ($1,$2,$3)`,
			fid, uid, t.Ratio); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": fid})
}

// listFreezes 楼栋收益冻结单（含目标与冻结/释放金额）
func (s *Server) listFreezes(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	bid := urlID(r, "id")
	rows, err := s.Pool.Query(r.Context(), `
		SELECT f.id, f.work_order_id, COALESCE(w.title,''), f.period_month, f.scope,
			f.estimated_cost, f.status, COALESCE(f.note,''),
			f.created_at, f.activated_at, f.released_at, COALESCE(f.release_note,''),
			COALESCE(w.responsibility,'undetermined'), COALESCE(w.responsibility_allocation::text,'[]')
		FROM revenue_freezes f LEFT JOIN work_orders w ON w.id=f.work_order_id
		WHERE f.building_id=$1 ORDER BY f.id DESC`, bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, wid int64
		var woTitle, month, scope, status, note, releaseNote, resp, allocJSON string
		var estCost float64
		var createdAt, activatedAt, releasedAt any
		if err := rows.Scan(&id, &wid, &woTitle, &month, &scope, &estCost, &status, &note,
			&createdAt, &activatedAt, &releasedAt, &releaseNote, &resp, &allocJSON); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		var allocs []freezeAlloc
		_ = json.Unmarshal([]byte(allocJSON), &allocs)
		out = append(out, map[string]any{
			"id": id, "work_order_id": wid, "work_order_title": woTitle,
			"period_month": month, "scope": scope, "estimated_cost": estCost,
			"status": status, "note": note, "created_at": createdAt,
			"activated_at": activatedAt, "released_at": releasedAt, "release_note": releaseNote,
			"responsibility": resp, "allocation": allocs,
			"targets": s.freezeTargets(r.Context(), id),
		})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// freezeTargets 返回冻结单的目标及冻结/释放金额
func (s *Server) freezeTargets(ctx context.Context, fid int64) []any {
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, COALESCE(t.user_id,0), COALESCE(u.name,'公共部分'),
			t.ratio, t.settled_amount, t.frozen_amount, t.released_amount
		FROM revenue_freeze_targets t LEFT JOIN users u ON u.id=t.user_id
		WHERE t.freeze_id=$1 ORDER BY t.id`, fid)
	if err != nil {
		return []any{}
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, uid int64
		var name string
		var ratio, settled, frozen, released float64
		if err := rows.Scan(&id, &uid, &name, &ratio, &settled, &frozen, &released); err != nil {
			return out
		}
		out = append(out, map[string]any{
			"id": id, "user_id": uid, "name": name, "ratio": ratio,
			"settled_amount": settled, "frozen_amount": frozen, "released_amount": released,
		})
	}
	return out
}

// cancelFreeze 取消尚未生效（结算定稿前）的冻结单
func (s *Server) cancelFreeze(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	fid := urlID(r, "fid")
	var status string
	if err := s.Pool.QueryRow(r.Context(), `SELECT status FROM revenue_freezes WHERE id=$1`, fid).Scan(&status); err != nil {
		writeErr(w, 404, "冻结单不存在")
		return
	}
	if status != "pending" {
		writeErr(w, 409, "仅待生效的冻结单可取消；已生效冻结请通过责任比例释放")
		return
	}
	if _, err := s.Pool.Exec(r.Context(),
		`UPDATE revenue_freezes SET status='cancelled' WHERE id=$1`, fid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// releaseFreeze 责任确认后按比例释放冻结收益。
//
// 每 1 元冻结额按 bearer 比例处置：
//   - installer（施工方赔付）：赔偿款入公共账并登记维修支出，住户拿回该部分收益；
//   - fund（运维/维修基金承担）：动用维修基金，住户拿回；
//   - public（楼栋公共收益支付）：公共账户列支维修，住户拿回；
//   - owner（业主自负）：从冻结收益中扣减，不再返还住户。
func (s *Server) releaseFreeze(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	fid := urlID(r, "fid")
	var in struct {
		Note       string        `json:"note"`
		Allocation []freezeAlloc `json:"allocation"`
	}
	_ = decodeJSON(r, &in)

	var bid, wid int64
	var month, status, woResp, allocJSON string
	if err := s.Pool.QueryRow(r.Context(), `
		SELECT f.building_id, f.work_order_id, f.period_month, f.status,
			COALESCE(w.responsibility,'undetermined'), COALESCE(w.responsibility_allocation::text,'[]')
		FROM revenue_freezes f JOIN work_orders w ON w.id=f.work_order_id
		WHERE f.id=$1`, fid).Scan(&bid, &wid, &month, &status, &woResp, &allocJSON); err != nil {
		if isNoRows(err) {
			writeErr(w, 404, "冻结单不存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if status != "active" {
		writeErr(w, 409, "冻结单当前状态为 "+status+"：待生效冻结在月度结算定稿后才持有收益，可先取消")
		return
	}

	allocs := in.Allocation
	if len(allocs) == 0 {
		// 未显式给比例时，按工单责任判定推导
		switch woResp {
		case "installer", "insurance":
			allocs = []freezeAlloc{{Bearer: "installer", Ratio: 100}}
		case "property":
			allocs = []freezeAlloc{{Bearer: "fund", Ratio: 100}}
		case "owner":
			allocs = []freezeAlloc{{Bearer: "owner", Ratio: 100}}
		case "shared":
			_ = json.Unmarshal([]byte(allocJSON), &allocs)
		}
		if len(allocs) == 0 {
			writeErr(w, 400, "责任尚未确认，请先在工单上给出处理结论，或提交 allocation 分担比例（installer/fund/public/owner，合计100%）")
			return
		}
	}
	var sum float64
	for _, a := range allocs {
		if a.Bearer != "installer" && a.Bearer != "fund" && a.Bearer != "public" && a.Bearer != "owner" {
			writeErr(w, 400, "分担方仅支持 installer/fund/public/owner")
			return
		}
		sum += a.Ratio
	}
	if sum < 99.99 || sum > 100.01 {
		writeErr(w, 400, "责任比例之和必须为 100%")
		return
	}
	ratioOf := func(bearer string) float64 {
		for _, a := range allocs {
			if a.Bearer == bearer {
				return a.Ratio
			}
		}
		return 0
	}
	rInst, rFund, rPublic, rOwner := ratioOf("installer"), ratioOf("fund"), ratioOf("public"), ratioOf("owner")

	// 待处置的持有记录
	type hold struct {
		ID, UserID *int64
		Amount     float64
	}
	rows, err := s.Pool.Query(r.Context(), `
		SELECT id, user_id, amount-released_amount
		FROM freeze_holds WHERE freeze_id=$1 AND status IN ('held','partially_released')`, fid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var holds []hold
	var totalFrozen float64
	for rows.Next() {
		var h hold
		if err := rows.Scan(&h.ID, &h.UserID, &h.Amount); err != nil {
			rows.Close()
			writeErr(w, 500, err.Error())
			return
		}
		if h.Amount <= 0 {
			continue
		}
		holds = append(holds, h)
		totalFrozen += h.Amount
	}
	rows.Close()
	if len(holds) == 0 {
		writeErr(w, 409, "没有待释放的冻结持有额")
		return
	}

	// 逐笔冻结持有分配责任额：施工方/基金/公共方向向下取整到分，尾差由业主侧吸收（保证每笔合计精确）。
	type holdAlloc struct {
		h                                      hold
		inst, fund, public, owner, ownerReturn float64
	}
	var holdAllocs []holdAlloc
	var amtInst, amtFund, amtPublic, amtOwner float64
	for _, h := range holds {
		pi := math.Floor(h.Amount*rInst) / 100
		pf := math.Floor(h.Amount*rFund) / 100
		pp := math.Floor(h.Amount*rPublic) / 100
		po := round2(h.Amount - pi - pf - pp)
		ha := holdAlloc{h: h, inst: pi, fund: pf, public: pp, owner: po,
			ownerReturn: round2(pi + pf + pp)}
		holdAllocs = append(holdAllocs, ha)
		amtInst = round2(amtInst + pi)
		amtFund = round2(amtFund + pf)
		amtPublic = round2(amtPublic + pp)
		amtOwner = round2(amtOwner + po)
	}

	tx, err := s.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	led := func(uid any, dir, cat string, amount float64, memo string) error {
		if amount == 0 {
			return nil
		}
		_, err := tx.Exec(r.Context(), `
			INSERT INTO ledger(building_id,user_id,direction,amount,category,period_month,ref_type,ref_id,memo)
			VALUES ($1,$2,$3,$4,$5,$6,'freeze',$7,$8)`,
			bid, uid, dir, amount, cat, month, fid, memo)
		return err
	}

	// 楼级账目：冻结收益对应的维修费全额支出，再按资金来源配平——
	// 施工方现金赔偿、维修基金专款（不动用现金）、业主自负（住户冻结收益转入）、楼栋公共收益。
	if err := led(nil, "out", "repair", totalFrozen, month+" 顶楼漏水维修费（冻结收益责任释放）"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := led(nil, "in", "compensation", amtInst, month+" 施工方赔付漏水维修"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 业主自负部分：住户被冻结的收益转入公共账冲抵维修（与住户维修扣减台账配平）
	if err := led(nil, "in", "repair", amtOwner, month+" 业主按责任比例承担，冻结收益冲抵维修"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if amtInst > 0 {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO payments(building_id,user_id,amount,pay_type,memo) VALUES ($1,NULL,$2,'compensation',$3)`,
			bid, amtInst, month+" 施工方漏水赔付"); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// 维修基金承担：专款标记动用；基金不足部分由楼栋公共收益自然承担（已含在楼级维修支出内，不重复记账）
	fundBal := fundBalanceTx(r.Context(), tx, bid)
	fundPaid := min2(amtFund, fundBal)
	if err := led(nil, "out", "maintenance_fund", fundPaid, month+" 动用维修基金承担漏水维修（专款标记）"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	for _, a := range holdAllocs {
		h := a.h
		if h.UserID == nil {
			// 公共部分冻结：冲回激活时的冻结台账，按责任比例参与维修分担
			if err := led(nil, "in", "freeze_release", h.Amount, month+" 公共冻结收益释放，按责任比例参与漏水维修分担"); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			if _, err := tx.Exec(r.Context(), `
				UPDATE freeze_holds SET status='released', released_amount=amount,
					bearer='public', note=$2, released_at=now() WHERE id=$1`,
				*h.ID, "公共收益参与漏水维修责任分担"); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			continue
		}
		uid := *h.UserID
		ownerPart := a.owner
		returnPart := a.ownerReturn
		// 返还住户（施工方赔付/维修基金/公共收益承担的部分）
		if returnPart > 0 {
			if _, err := tx.Exec(r.Context(),
				`UPDATE users SET balance=balance+$2 WHERE id=$1`, uid, returnPart); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			if err := led(uid, "in", "freeze_release", returnPart, month+" 漏水责任确认，冻结收益按比例返还"); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		if ownerPart > 0 {
			if err := led(uid, "out", "repair", ownerPart, month+" 业主按责任比例承担漏水维修（冻结收益扣减）"); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		st := "released"
		if ownerPart > 0 && returnPart > 0 {
			st = "partially_released"
		} else if ownerPart > 0 {
			st = "deducted"
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE freeze_holds SET released_amount=$2, status=$3,
				bearer=CASE WHEN $4>0 THEN 'owner' ELSE '' END,
				note=$5, released_at=now() WHERE id=$1`,
			*h.ID, returnPart, st, ownerPart,
			"返还 "+strconv.FormatFloat(returnPart, 'f', 2, 64)+" 元，业主自负 "+strconv.FormatFloat(ownerPart, 'f', 2, 64)+" 元"); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}

	// 回填 target 快照金额
	if _, err := tx.Exec(r.Context(), `
		UPDATE revenue_freeze_targets SET released_amount=frozen_amount WHERE freeze_id=$1`, fid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	note := in.Note
	fundShort := round2(amtFund - fundPaid)
	if fundShort > 0 {
		note += "（维修基金不足 " + strconv.FormatFloat(fundShort, 'f', 2, 64) + " 元，由楼栋公共收益承担）"
	}
	if _, err := tx.Exec(r.Context(),
		`UPDATE revenue_freezes SET status='released', released_at=now(), release_note=$2 WHERE id=$1`,
		fid, note); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 责任结论回写工单（多方分担写 shared 与比例），并确保有后续巡检计划
	if woResp == "undetermined" {
		resp := "shared"
		if rInst == 100 {
			resp = "installer"
		} else if rFund == 100 {
			resp = "property"
		} else if rOwner == 100 {
			resp = "owner"
		} else if rPublic == 100 {
			resp = "shared"
		}
		allocBytes, _ := json.Marshal(in.Allocation)
		if _, err := tx.Exec(r.Context(),
			`UPDATE work_orders SET responsibility=$2, responsibility_allocation=$3 WHERE id=$1`,
			wid, resp, string(allocBytes)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := ensureFollowupInspection(r.Context(), tx, bid, wid, u.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "total_frozen": totalFrozen,
		"installer": amtInst, "fund": fundPaid, "fund_shortfall": fundShort,
		"public": round2(amtPublic + fundShort), "owner": amtOwner,
	})
}

// ---------- 屋顶巡检计划 ----------

func (s *Server) listInspections(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	bid := urlID(r, "id")
	rows, err := s.Pool.Query(r.Context(), `
		SELECT i.id, COALESCE(i.work_order_id,0), i.title, i.plan_date, COALESCE(i.focus,''),
			i.status, COALESCE(i.result_note,''), COALESCE(w.title,''), i.created_at, i.completed_at
		FROM roof_inspections i LEFT JOIN work_orders w ON w.id=i.work_order_id
		WHERE i.building_id=$1 ORDER BY i.plan_date, i.id`, bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, wid int64
		var title, focus, status, result, woTitle string
		var planDate time.Time
		var createdAt, completedAt any
		if err := rows.Scan(&id, &wid, &title, &planDate, &focus, &status, &result, &woTitle, &createdAt, &completedAt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "work_order_id": wid, "work_order_title": woTitle,
			"title": title, "plan_date": planDate.Format("2006-01-02"), "focus": focus, "status": status,
			"result_note": result, "created_at": createdAt, "completed_at": completedAt,
		})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) createInspection(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	bid := urlID(r, "id")
	var in struct {
		WorkOrderID int64  `json:"work_order_id"`
		Title       string `json:"title"`
		PlanDate    string `json:"plan_date"` // YYYY-MM-DD
		Focus       string `json:"focus"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Title == "" || in.PlanDate == "" {
		writeErr(w, 400, "title、plan_date（YYYY-MM-DD）必填")
		return
	}
	planDate, err := time.Parse("2006-01-02", in.PlanDate)
	if err != nil {
		writeErr(w, 400, "plan_date 格式应为 YYYY-MM-DD")
		return
	}
	var wid *int64
	if in.WorkOrderID > 0 {
		var woBid int64
		if e := s.Pool.QueryRow(r.Context(), `SELECT building_id FROM work_orders WHERE id=$1`, in.WorkOrderID).Scan(&woBid); e != nil {
			writeErr(w, 404, "关联工单不存在")
			return
		}
		if woBid != bid {
			writeErr(w, 400, "工单不属于该楼栋")
			return
		}
		wid = &in.WorkOrderID
	}
	var id int64
	if err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO roof_inspections(building_id,work_order_id,title,plan_date,focus,created_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		bid, wid, in.Title, planDate, in.Focus, u.ID).Scan(&id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) completeInspection(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	iid := urlID(r, "iid")
	var in struct {
		ResultNote string `json:"result_note"`
		Status     string `json:"status"`
	}
	_ = decodeJSON(r, &in)
	if in.ResultNote == "" {
		writeErr(w, 400, "请填写巡检结果")
		return
	}
	status := "done"
	if in.Status == "cancelled" {
		status = "cancelled"
	}
	ct, err := s.Pool.Exec(r.Context(), `
		UPDATE roof_inspections
		SET status=$2, result_note=$3, completed_at=now()
		WHERE id=$1 AND status='scheduled'`, iid, status, in.ResultNote)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ct.RowsAffected() == 0 {
		writeErr(w, 409, "巡检计划不存在或已处理")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- 小工具 ----------

// finalizeItem 结算定稿后用于建立冻结持有的明细摘要
type finalizeItem struct {
	ID     int64
	UserID *int64
	Type   string
	Amount float64
	Frozen float64
}

// activateFreezes 月度结算定稿时：按冻结单 target 比例把冻结额挂为持有记录，并激活冻结单。
// 同一对象（住户/公共）若有多张冻结单，按 target.ratio 权重分配引擎已算出的 item.Frozen，尾差并入最后一条。
func (s *Server) activateFreezes(r *http.Request, tx pgx.Tx, bid int64, month string, sid int64, items []finalizeItem) error {
	itemByUser := map[int64]finalizeItem{}
	var publicItem *finalizeItem
	for i := range items {
		if items[i].Type == "shareholder" && items[i].UserID != nil {
			itemByUser[*items[i].UserID] = items[i]
		}
		if items[i].Type == "public" {
			p := items[i]
			publicItem = &p
		}
	}
	rows, err := tx.Query(r.Context(), `
		SELECT f.id, t.id, t.user_id, t.ratio
		FROM revenue_freezes f JOIN revenue_freeze_targets t ON t.freeze_id=f.id
		WHERE f.building_id=$1 AND f.period_month=$2 AND f.status='pending'
		ORDER BY f.id, t.id`, bid, month)
	if err != nil {
		return err
	}
	type tgt struct {
		FreezeID, TargetID int64
		UserID             *int64
		Ratio              float64
	}
	var targets []tgt
	freezeIDs := map[int64]bool{}
	for rows.Next() {
		var tg tgt
		if err := rows.Scan(&tg.FreezeID, &tg.TargetID, &tg.UserID, &tg.Ratio); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, tg)
		freezeIDs[tg.FreezeID] = true
	}
	rows.Close()
	if len(targets) == 0 {
		return nil
	}

	sameParty := func(a, b *int64) bool {
		return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
	}
	remaining := map[int64]float64{} // key 0 = 公共
	for _, tg := range targets {
		var settled, frozenCap float64
		var itemID, uid *int64
		if tg.UserID != nil {
			it, ok := itemByUser[*tg.UserID]
			if !ok || it.Amount <= 0 {
				continue
			}
			settled, frozenCap = it.Amount, it.Frozen
			id := it.ID
			itemID = &id
			uid = tg.UserID
		} else if publicItem != nil && publicItem.Amount > 0 {
			settled, frozenCap = publicItem.Amount, publicItem.Frozen
			id := publicItem.ID
			itemID = &id
		} else {
			continue
		}
		var ratioSum float64
		for _, x := range targets {
			if sameParty(x.UserID, tg.UserID) {
				ratioSum += x.Ratio
			}
		}
		var alloc float64
		if ratioSum > 0 {
			alloc = round2(frozenCap * tg.Ratio / ratioSum)
		}
		key := int64(0)
		if tg.UserID != nil {
			key = *tg.UserID
		}
		if _, seen := remaining[key]; !seen {
			remaining[key] = frozenCap
		}
		if alloc > remaining[key] {
			alloc = remaining[key]
		}
		remaining[key] = round2(remaining[key] - alloc)
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO freeze_holds(building_id,freeze_id,target_id,period_id,item_id,user_id,
				settlement_amount,amount,status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'held')`,
			bid, tg.FreezeID, tg.TargetID, sid, itemID, uid, settled, alloc); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE revenue_freeze_targets SET settled_amount=$2, frozen_amount=$3 WHERE id=$1`,
			tg.TargetID, settled, alloc); err != nil {
			return err
		}
	}
	// 尾差并入同对象最后一条 target
	for key, residual := range remaining {
		if residual <= 0 {
			continue
		}
		for j := len(targets) - 1; j >= 0; j-- {
			tg := targets[j]
			match := (key == 0 && tg.UserID == nil) || (key != 0 && tg.UserID != nil && *tg.UserID == key)
			if !match {
				continue
			}
			if _, err := tx.Exec(r.Context(),
				`UPDATE freeze_holds SET amount=amount+$2 WHERE target_id=$1`, tg.TargetID, residual); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(),
				`UPDATE revenue_freeze_targets SET frozen_amount=frozen_amount+$2 WHERE id=$1`,
				tg.TargetID, residual); err != nil {
				return err
			}
			break
		}
	}
	for fid := range freezeIDs {
		if _, err := tx.Exec(r.Context(),
			`UPDATE revenue_freezes SET status='active', activated_at=now() WHERE id=$1`, fid); err != nil {
			return err
		}
	}
	// 公共部分被冻结的可支配收益记一条冻结台账（影响公共账户可用余额展示）
	var publicHeld float64
	if err := tx.QueryRow(r.Context(), `
		SELECT COALESCE(SUM(amount),0) FROM freeze_holds WHERE period_id=$1 AND user_id IS NULL`,
		sid).Scan(&publicHeld); err != nil {
		return err
	}
	if publicHeld > 0 {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO ledger(building_id,user_id,direction,amount,category,period_month,ref_type,ref_id,memo)
			VALUES ($1,NULL,'out',$2,'freeze_hold',$3,'settlement',$4,$5)`,
			bid, publicHeld, month, sid, month+" 顶楼漏水投诉期间冻结公共可支配收益（待责任确认）"); err != nil {
			return err
		}
	}
	return nil
}

func round2(x float64) float64 {
	return math.Round(x*100) / 100
}

func min2(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// fundBalanceTx 事务内查询维修基金余额
func fundBalanceTx(ctx context.Context, tx pgx.Tx, bid int64) float64 {
	var bal float64
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN direction='in' THEN amount ELSE -amount END),0)
		FROM ledger WHERE building_id=$1 AND category='maintenance_fund'`, bid).Scan(&bal)
	return bal
}
