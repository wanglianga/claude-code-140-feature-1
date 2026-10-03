package api

import (
	"net/http"

	"pvplatform/internal/auth"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	var (
		id           int64
		name, role   string
		unitNo, hash string
	)
	err := s.Pool.QueryRow(r.Context(),
		`SELECT id, name, role, COALESCE(unit_no,''), password_hash FROM users WHERE phone=$1`, in.Phone).
		Scan(&id, &name, &role, &unitNo, &hash)
	if err != nil || !auth.CheckPassword(hash, in.Password) {
		writeErr(w, 401, "手机号或密码错误")
		return
	}
	token, err := auth.Issue(s.JWTSecret, auth.Claims{UserID: id, Name: name, Role: role, UnitNo: unitNo})
	if err != nil {
		writeErr(w, 500, "签发令牌失败")
		return
	}
	writeJSON(w, 200, map[string]any{"token": token, "user": map[string]any{
		"id": id, "name": name, "role": role, "unit_no": unitNo, "phone": in.Phone,
	}})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Phone    string `json:"phone"`
		UnitNo   string `json:"unit_no"`
		Password string `json:"password"`
		Role     string `json:"role"` // 仅管理员可创建 property/admin
	}
	if err := decodeJSON(r, &in); err != nil || in.Name == "" || in.Phone == "" || in.Password == "" {
		writeErr(w, 400, "姓名、手机号、密码为必填项")
		return
	}
	role := "resident"
	if cu := auth.CurrentUser(r); cu != nil && cu.Role == "admin" && in.Role != "" {
		role = in.Role
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, 500, "密码处理失败")
		return
	}
	var id int64
	err = s.Pool.QueryRow(r.Context(),
		`INSERT INTO users(name, phone, unit_no, role, password_hash) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		in.Name, in.Phone, in.UnitNo, role, hash).Scan(&id)
	if err != nil {
		writeErr(w, 400, "注册失败：手机号可能已被注册")
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "name": in.Name, "phone": in.Phone, "role": role, "unit_no": in.UnitNo})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := auth.Require(w, r)
	if u == nil {
		return
	}
	var balance string
	var phone string
	err := s.Pool.QueryRow(r.Context(),
		`SELECT phone, balance FROM users WHERE id=$1`, u.ID).Scan(&phone, &balance)
	if err != nil {
		writeErr(w, 500, "用户不存在")
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": u.ID, "name": u.Name, "role": u.Role, "unit_no": u.UnitNo,
		"phone": phone, "balance": balance, "frozen_held": s.frozenHeldFor(r, u.ID),
	})
}

// frozenHeldFor 返回用户当前仍被持有、未按责任释放的漏水冻结收益
func (s *Server) frozenHeldFor(r *http.Request, uid int64) float64 {
	var held float64
	_ = s.Pool.QueryRow(r.Context(),
		`SELECT COALESCE(SUM(amount),0) FROM freeze_holds
		 WHERE user_id=$1 AND status='held'`, uid).Scan(&held)
	return held
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if u := auth.RequireRole(w, r, "admin", "property"); u == nil {
		return
	}
	q := r.URL.Query().Get("q")
	rows, err := s.Pool.Query(r.Context(),
		`SELECT id, name, phone, COALESCE(unit_no,''), role, balance FROM users
		 WHERE ($1='' OR name ILIKE '%'||$1||'%' OR phone ILIKE '%'||$1||'%' OR COALESCE(unit_no,'') ILIKE '%'||$1||'%')
		 ORDER BY id LIMIT 200`, q)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id int64
		var name, phone, unitNo, role string
		var balance string
		if err := rows.Scan(&id, &name, &phone, &unitNo, &role, &balance); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "phone": phone, "unit_no": unitNo, "role": role, "balance": balance,
		})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
