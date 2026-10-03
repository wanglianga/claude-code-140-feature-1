package api

import (
	"net/http"

	"pvplatform/internal/auth"
)

// 屋顶巡检计划：漏水工单处理结论写入后续巡检；支持物业排期与登记结果。

func (s *Server) listInspections(w http.ResponseWriter, r *http.Request) {
	if u := auth.Require(w, r); u == nil {
		return
	}
	bid := urlID(r, "id")
	rows, err := s.Pool.Query(r.Context(), `
		SELECT i.id, COALESCE(i.work_order_id,0), to_char(i.scheduled_date,'YYYY-MM-DD'),
			COALESCE(i.title,''), COALESCE(i.focus_area,''), COALESCE(i.conclusion,''),
			i.status, COALESCE(i.result_note,''), COALESCE(to_char(i.done_at,'YYYY-MM-DD'),''),
			COALESCE(u.name,''), i.created_at
		FROM roof_inspections i LEFT JOIN users u ON u.id=i.created_by
		WHERE i.building_id=$1
		ORDER BY CASE i.status WHEN 'scheduled' THEN 0 ELSE 1 END, i.scheduled_date DESC, i.id DESC`,
		bid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, wid int64
		var date, title, focus, conclusion, status, note, doneAt, author string
		var createdAt any
		if err := rows.Scan(&id, &wid, &date, &title, &focus, &conclusion, &status,
			&note, &doneAt, &author, &createdAt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "work_order_id": wid, "scheduled_date": date, "title": title,
			"focus_area": focus, "conclusion": conclusion, "status": status,
			"result_note": note, "done_at": doneAt, "author": author, "created_at": createdAt,
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
		WorkOrderID   int64  `json:"work_order_id"`
		ScheduledDate string `json:"scheduled_date"`
		Title         string `json:"title"`
		FocusArea     string `json:"focus_area"`
		Conclusion    string `json:"conclusion"`
	}
	if err := decodeJSON(r, &in); err != nil || in.ScheduledDate == "" {
		writeErr(w, 400, "scheduled_date（YYYY-MM-DD）必填")
		return
	}
	var id int64
	err := s.Pool.QueryRow(r.Context(), `
		INSERT INTO roof_inspections(building_id,work_order_id,scheduled_date,title,focus_area,conclusion,created_by)
		VALUES ($1,NULLIF($2,0),$3::date,$4,$5,$6,$7) RETURNING id`,
		bid, in.WorkOrderID, in.ScheduledDate, in.Title, in.FocusArea, in.Conclusion, u.ID).Scan(&id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// updateInspection 登记巡检结果（完成/取消）
func (s *Server) updateInspection(w http.ResponseWriter, r *http.Request) {
	u := auth.RequireRole(w, r, "admin", "property")
	if u == nil {
		return
	}
	iid := urlID(r, "iid")
	var in struct {
		Status     string `json:"status"`
		ResultNote string `json:"result_note"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	if in.Status != "" && in.Status != "scheduled" && in.Status != "done" && in.Status != "cancelled" {
		writeErr(w, 400, "状态不合法")
		return
	}
	ct, err := s.Pool.Exec(r.Context(), `
		UPDATE roof_inspections SET
			status=COALESCE(NULLIF($2,''),status),
			result_note=CASE WHEN $3='' THEN result_note ELSE $3 END,
			done_at=CASE WHEN $2='done' THEN now() ELSE done_at END
		WHERE id=$1`, iid, in.Status, in.ResultNote)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ct.RowsAffected() == 0 {
		writeErr(w, 404, "巡检计划不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
