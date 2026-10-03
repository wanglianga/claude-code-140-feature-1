// Package api 实现平台全部 HTTP 接口。
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pvplatform/internal/auth"
	"pvplatform/internal/web"
)

type Server struct {
	Pool      *pgxpool.Pool
	JWTSecret string
}

func NewServer(pool *pgxpool.Pool, jwtSecret string) *Server {
	return &Server{Pool: pool, JWTSecret: jwtSecret}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(auth.Middleware(s.JWTSecret))

	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Post("/auth/register", s.register)
		r.Get("/me", s.me)

		r.Get("/buildings", s.listBuildings)
		r.Post("/buildings", s.createBuilding)
		r.Get("/buildings/{id}", s.getBuilding)
		r.Patch("/buildings/{id}", s.updateBuilding)
		r.Get("/buildings/{id}/dashboard", s.buildingDashboard)
		r.Get("/buildings/{id}/stage-logs", s.listStageLogs)
		r.Post("/buildings/{id}/stage", s.transitionStage)

		r.Get("/buildings/{id}/members", s.listMembers)
		r.Post("/buildings/{id}/members", s.addMember)

		// 立项表决
		r.Get("/buildings/{id}/votes", s.listVotes)
		r.Post("/buildings/{id}/votes", s.createVote)
		r.Post("/votes/{vid}/choose", s.chooseVote)
		r.Post("/votes/{vid}/close", s.closeVote)

		// 认购与缴费
		r.Get("/buildings/{id}/subscriptions", s.listSubscriptions)
		r.Post("/buildings/{id}/subscribe", s.subscribe)
		r.Post("/subscriptions/{sid}/pay", s.paySubscription)
		r.Post("/subscriptions/{sid}/refund", s.refundSubscription)

		// 设备与发电
		r.Get("/buildings/{id}/inverters", s.listInverters)
		r.Post("/buildings/{id}/inverters", s.createInverter)
		r.Post("/inverters/{iid}/fault", s.faultInverter)
		r.Post("/inverters/{iid}/resolve", s.resolveInverter)
		r.Get("/buildings/{id}/generation", s.listGeneration)
		r.Post("/buildings/{id}/generation", s.uploadGeneration)
		r.Get("/buildings/{id}/alerts", s.listAlerts)
		r.Post("/buildings/{id}/alerts", s.createAlert)
		r.Post("/alerts/{aid}/explain", s.explainAlert)

		// 维修工单
		r.Get("/buildings/{id}/work-orders", s.listWorkOrders)
		r.Post("/buildings/{id}/work-orders", s.createWorkOrder)
		r.Post("/work-orders/{wid}", s.updateWorkOrder)

		// 顶楼漏水处置：证据、临时止水/安抚/第三方检测/结论、同意书
		r.Get("/work-orders/{wid}/attachments", s.listAttachments)
		r.Post("/work-orders/{wid}/attachments", s.attachWorkOrder)
		r.Post("/work-orders/{wid}/leak-action", s.leakAction)
		r.Get("/buildings/{id}/consents", s.listConsents)
		r.Post("/buildings/{id}/consents", s.createConsent)

		// 收益冻结与释放
		r.Get("/buildings/{id}/freezes", s.listFreezes)
		r.Post("/buildings/{id}/freezes", s.createFreeze)
		r.Post("/freezes/{fid}/cancel", s.cancelFreeze)
		r.Post("/freezes/{fid}/release", s.releaseFreeze)

		// 后续屋顶巡检计划
		r.Get("/buildings/{id}/roof-inspections", s.listInspections)
		r.Post("/buildings/{id}/roof-inspections", s.createInspection)
		r.Post("/roof-inspections/{iid}/complete", s.completeInspection)

		// 结算与台账
		r.Get("/buildings/{id}/settlements", s.listSettlements)
		r.Post("/buildings/{id}/settlements", s.createSettlement)
		r.Get("/settlements/{sid}", s.getSettlement)
		r.Post("/settlements/{sid}/finalize", s.finalizeSettlement)
		r.Get("/buildings/{id}/ledger", s.listLedger)
		r.Get("/buildings/{id}/payments", s.listPayments)

		// 房屋转让/退购
		r.Get("/buildings/{id}/transfers", s.listTransfers)
		r.Post("/buildings/{id}/transfers", s.createTransfer)
		r.Post("/transfers/{tid}/confirm", s.confirmTransfer)
		r.Post("/transfers/{tid}/reject", s.rejectTransfer)

		// 公告 / 异议投票 / 年报
		r.Get("/buildings/{id}/announcements", s.listAnnouncements)
		r.Post("/buildings/{id}/announcements", s.createAnnouncement)
		r.Delete("/announcements/{aid}", s.deleteAnnouncement)
		r.Get("/buildings/{id}/polls", s.listPolls)
		r.Post("/buildings/{id}/polls", s.createPoll)
		r.Post("/polls/{pid}/vote", s.votePoll)
		r.Post("/polls/{pid}/close", s.closePoll)
		r.Get("/buildings/{id}/annual-reports", s.listAnnualReports)
		r.Post("/buildings/{id}/annual-reports", s.createAnnualReport)
		r.Get("/annual-reports/{rid}", s.getAnnualReport)
		r.Post("/annual-reports/{rid}/publish", s.publishAnnualReport)

		r.Get("/users", s.listUsers)
	})

	// 内嵌的前端静态资源（SPA）
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err == nil {
		fileServer := http.FileServer(http.FS(distFS))
		r.Handle("/*", spaHandler(fileServer, distFS))
	}
	return r
}

func spaHandler(fileServer http.Handler, distFS fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(distFS, p); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func urlID(r *http.Request, key string) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, key), 10, 64)
	return id
}

// isNoRows 判断 pgx 无结果
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
