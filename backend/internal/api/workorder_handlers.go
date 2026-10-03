package api

import (
	"net/http"

	"pvplatform/internal/auth"
)

func (s *Server) createWorkOrder(w http.ResponseWriter, r *http.Request) {
	u := auth.Require(w, r)
	if u == nil {
		return
	}
	bid := urlID(r, "id")
	var in struct {
		InverterID  int64   `json:"inverter_id"`
		PeriodMonth string  `json:"period_month"`
		Title       string  `json:"title"`
		Description string  `json:"description"`
		WOType      string  `json:"wo_type"`
		Cost        float64 `json:"cost"`
		Assignee    string  `json:"assignee"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Title == "" || in.PeriodMonth == "" {
		writeErr(w, 400, "title、period_month 必填")
		return
	}
	if in.WOType == "" {
		in.WOType = "other"
	}
	var id int64
	err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO work_orders(building_id,inverter_id,period_month,title,description,wo_type,cost,assignee,reporter_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		bid, nullableID(in.InverterID), in.PeriodMonth, in.Title, in.Description, in.WOType,
		in.Cost, in.Assignee, u.ID).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 逆变器故障类工单同步标记设备状态（可随后由维修完成恢复）
	if in.InverterID != 0 && in.WOType == "inverter" {
		_, _ = s.Pool.Exec(r.Context(),
			`UPDATE inverters SET status='faulty', fault_note=$2, fault_at=now() WHERE id=$1`,
			in.InverterID, in.Title)
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) listWorkOrders(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	bid := urlID(r, "id")
	month := r.URL.Query().Get("period_month")
	rows, err := s.Pool.Query(r.Context(), `
		SELECT w.id, COALESCE(w.inverter_id,0), COALESCE(i.name,''), w.period_month, w.title,
			w.description, w.wo_type, w.responsibility, w.cost, w.status, w.assignee,
			COALESCE(re.name,''), w.created_at, w.resolved_at,
			COALESCE(rf.id,0), COALESCE(rf.status,'')
		FROM work_orders w
		LEFT JOIN inverters i ON i.id=w.inverter_id
		LEFT JOIN users re ON re.id=w.reporter_id
		LEFT JOIN revenue_freezes rf ON rf.work_order_id=w.id
		WHERE w.building_id=$1 AND ($2='' OR w.period_month=$2)
		ORDER BY w.period_month DESC, w.id DESC`, bid, month)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, invID, freezeID int64
		var title, desc, wtype, resp, status, assignee, reporter, month string
		var invName, freezeStatus string
		var cost float64
		var createdAt, resolvedAt any
		if err := rows.Scan(&id, &invID, &invName, &month, &title, &desc, &wtype, &resp,
			&cost, &status, &assignee, &reporter, &createdAt, &resolvedAt,
			&freezeID, &freezeStatus); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "inverter_id": invID, "inverter_name": invName,
			"period_month": month, "title": title, "description": desc,
			"wo_type": wtype, "responsibility": resp, "cost": cost,
			"status": status, "assignee": assignee, "reporter": reporter,
			"created_at": createdAt, "resolved_at": resolvedAt,
			"freeze_id": freezeID, "freeze_status": freezeStatus,
		})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

// updateWorkOrder 更新处理状态、费用与责任判定；resolved 时若关联逆变器故障则恢复
func (s *Server) updateWorkOrder(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	wid := urlID(r, "wid")
	var in struct {
		Status         string  `json:"status"`
		Cost           float64 `json:"cost"`
		Responsibility string  `json:"responsibility"`
		Assignee       string  `json:"assignee"`
		Description    string  `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if in.Status != "" && in.Status != "open" && in.Status != "processing" && in.Status != "resolved" {
		writeErr(w, 400, "工单状态不合法")
		return
	}
	if in.Responsibility != "" && in.Responsibility != "undetermined" &&
		in.Responsibility != "property" && in.Responsibility != "installer" &&
		in.Responsibility != "owner" && in.Responsibility != "insurance" {
		writeErr(w, 400, "责任方不合法")
		return
	}
	var invID *int64
	var oldStatus string
	err := s.Pool.QueryRow(r.Context(),
		`SELECT inverter_id, status FROM work_orders WHERE id=$1`, wid).Scan(&invID, &oldStatus)
	if isNoRows(err) {
		writeErr(w, 404, "工单不存在")
		return
	} else if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 已最终结算周期涉及的工单不允许再改责任/费用，防止账目被篡改
	var finalized string
	_ = s.Pool.QueryRow(r.Context(),
		`SELECT COALESCE(sp.status,'') FROM work_orders w
		 LEFT JOIN settlement_periods sp ON sp.building_id=w.building_id AND sp.period_month=w.period_month
		 WHERE w.id=$1`, wid).Scan(&finalized)
	if finalized == "finalized" && (in.Cost > 0 || in.Responsibility != "") {
		writeErr(w, 409, "该周期已完成月度结算，工单费用/责任不可再变更，请在次月调整")
		return
	}
	tx, err := s.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `
		UPDATE work_orders SET
			status=COALESCE(NULLIF($2,''),status),
			cost=CASE WHEN $3<0 THEN cost ELSE $3 END,
			responsibility=COALESCE(NULLIF($4,''),responsibility),
			assignee=COALESCE(NULLIF($5,''),assignee),
			description=CASE WHEN $6='' THEN description ELSE $6 END,
			resolved_at=CASE WHEN $2='resolved' THEN now() ELSE resolved_at END
		WHERE id=$1`,
		wid, in.Status, in.Cost, in.Responsibility, in.Assignee, in.Description); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if in.Status == "resolved" && invID != nil {
		if _, err := tx.Exec(r.Context(),
			`UPDATE inverters SET status='normal', fault_note='', fault_at=NULL WHERE id=$1 AND status='faulty'`,
			*invID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
