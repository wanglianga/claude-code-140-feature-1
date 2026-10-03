#!/usr/bin/env python3
"""端到端 API 验证脚本（依赖 python3，无第三方包）。

用法：
    python3 scripts/verify_api.py [BASE_URL]

前置：服务已启动且数据库为初始种子状态（SEED_ON_START=true 首次启动后的干净库）。
脚本会校验：驾驶舱 → 9 月结算（维修基金动用+分摊）→ 定稿入账 →
10 月雨后漏水投诉证据链/处置 → 相关组件收益冻结挂账 → 责任按比例释放（施工方/运维基金/公共收益/业主自担）
→ 处理结论写入巡检计划 → 房屋转让后次月分成归新业主 → 公告/异议/异常解释/年报 → 阶段回退原因强制。
任一断言失败立即退出（exit 1）。
"""
import json
import sys
import urllib.request
import urllib.error

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8080"


def call(method, path, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req) as r:
            raw = r.read().decode()
            return r.status, json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"raw": raw}


def check(name, cond, detail=""):
    if not cond:
        print(f"FAIL: {name} {detail}")
        sys.exit(1)
    print(f"PASS: {name} {detail}")


def approx(a, b, eps=0.01):
    return abs(float(a) - float(b)) < eps


def login(phone, pwd):
    st, r = call("POST", "/api/auth/login", body={"phone": phone, "password": pwd})
    check(f"login {phone}", st == 200)
    return r["token"]


def main():
    prop = login("13800000001", "property123")
    zhang = login("13900000001", "resident123")
    liu = login("13900000006", "resident123")

    st, r = call("GET", "/api/buildings", prop)
    b1 = next(b for b in r["items"] if "1 号楼" in b["name"])
    bid = b1["id"]
    print(f"-- 目标楼栋：{b1['name']} (id={bid})，阶段 {b1['stage']}")

    # A. 8 月已定稿的驾驶舱
    st, d = call("GET", f"/api/buildings/{bid}/dashboard", prop)
    check("8月后公共账户余额=85631.00", approx(d["public_balance"], 85631.00), str(d["public_balance"]))
    check("维修基金余额=97.86", approx(d["maintenance_fund_balance"], 97.86), str(d["maintenance_fund_balance"]))
    check("8月累计发电4150", approx(d["total_kwh"], 4150))
    check("碳减排2411.15kg", approx(d["carbon_kg"], 2411.15))

    # B. 9 月试算：2600kWh，漏水 600 物业责任，基金期初 97.86
    st, r = call("POST", f"/api/buildings/{bid}/settlements", prop, {
        "period_month": "2026-09", "grid_price": 0.4153, "subsidy_per_kwh": 0.05,
        "om_fee": 200, "tax": 120,
    })
    s = r["settlement"]
    check("9月动用基金97.86", approx(s["fund_used"], 97.86), str(s["fund_used"]))
    check("9月维修分摊502.14", approx(s["repair_deduct"], 502.14), str(s["repair_deduct"]))
    check("9月计提基金23.26", approx(s["fund_addition"], 23.26), str(s["fund_addition"]))
    zhang_item = next(i for i in s["items"] if i["item_type"] == "shareholder" and i["name"].startswith("张伟"))
    check("张伟9月分成116.29", approx(zhang_item["amount"], 116.29), str(zhang_item["amount"]))
    spid = r["id"]

    # C. 定稿
    st, _ = call("POST", f"/api/settlements/{spid}/finalize", prop)
    check("9月结算定稿", st == 200)
    st, _ = call("POST", f"/api/buildings/{bid}/settlements", prop,
                 {"period_month": "2026-09", "grid_price": 0.5})
    check("定稿后禁止重算(409)", st == 409, str(st))
    st, me = call("GET", "/api/me", zhang)
    check("张伟余额=8月489.30+9月116.29=605.59", approx(me["balance"], 605.59), str(me["balance"]))
    st, lg = call("GET", f"/api/buildings/{bid}/ledger", prop)
    check("9月后公共账户=85920.78", approx(lg["public_balance"], 85920.78), str(lg["public_balance"]))
    check("9月后维修基金=23.26", approx(lg["maintenance_fund_balance"], 23.26), str(lg["maintenance_fund_balance"]))

    # D. 10 月雨后新漏水投诉档案：证据链（施工照片/防水验收/雨量/同意签署）+ 三项临时处置
    st, wos = call("GET", f"/api/buildings/{bid}/work-orders", prop)
    leak = next(w for w in wos["items"] if "新装支架" in w["title"])
    wid = leak["id"]
    check("漏水工单为责任待判定", leak["responsibility"] == "undetermined", leak["responsibility"])
    check("漏水工单处理中", leak["status"] == "processing", leak["status"])
    st, detail = call("GET", f"/api/work-orders/{wid}", prop)
    etypes = sorted(e["evidence_type"] for e in detail["evidences"])
    check("证据链四类齐全(含第三方外的四类)",
          etypes == ["consent", "construction_photo", "rainfall", "waterproof_acceptance"], str(etypes))
    rain = next(e for e in detail["evidences"] if e["evidence_type"] == "rainfall")
    check("雨量记录68.5mm", approx(rain["amount"], 68.5), str(rain["amount"]))
    consent = next(e for e in detail["evidences"] if e["evidence_type"] == "consent")
    check("记录了顶楼住户此前已签署同意", "签署" in consent["detail"], consent["detail"])
    atypes = sorted(a["action_type"] for a in detail["actions"])
    check("临时止水/住户安抚/第三方检测均已安排",
          atypes == ["resident_reassurance", "temporary_seal", "third_party_inspection"], str(atypes))
    # 非法证据类型 400；住户无权安排处置 403
    st, _ = call("POST", f"/api/work-orders/{wid}/evidences", zhang,
                 {"evidence_type": "bogus", "title": "x"})
    check("非法证据类型拒绝(400)", st == 400, str(st))
    st, _ = call("POST", f"/api/work-orders/{wid}/actions", zhang,
                 {"action_type": "temporary_seal", "content": "自行止水"})
    check("住户安排处置被拒(403)", st == 403, str(st))

    # E. 维修期间收益冻结单（种子已按定向范围设立：张伟 30% 相关组件，自 10 月起）
    st, fzs = call("GET", f"/api/buildings/{bid}/freezes", prop)
    fz = next((f for f in fzs["items"] if f["work_order_id"] == wid), None)
    check("漏水工单已挂收益冻结单", fz is not None and fz["status"] == "frozen", str(fzs["items"]))
    fid = fz["id"]
    st, fd = call("GET", f"/api/freezes/{fid}", prop)
    check("冻结范围为相关组件(定向)", fd["scope"] == "targeted", fd["scope"])
    check("冻结对象为顶楼住户张伟", any(t["name"].startswith("张伟") for t in fd["targets"]), str(fd["targets"]))
    st, _ = call("POST", f"/api/buildings/{bid}/freezes", prop,
                 {"work_order_id": wid, "period_month": "2026-10"})
    check("同一工单重复冻结拒绝(409)", st == 409, str(st))

    # F. 10 月发电与结算定稿：张伟 30% 分成 335.02 应冻结挂账，不进余额
    st, invs = call("GET", f"/api/buildings/{bid}/inverters", prop)
    for iv in invs["items"]:
        st, _ = call("POST", f"/api/buildings/{bid}/generation", prop, {
            "period_month": "2026-10", "inverter_id": iv["id"], "kwh": 1200,
        })
        check(f"录入{iv['name']}10月电量", st == 200)
    st, r = call("POST", f"/api/buildings/{bid}/settlements", prop, {
        "period_month": "2026-10", "grid_price": 0.4153, "subsidy_per_kwh": 0.05,
    })
    check("10月结算生成", st == 200, str(r)[:200])
    octid = r["id"]
    st, sd = call("GET", f"/api/settlements/{octid}", prop)
    zrow = next(i for i in sd["items"] if i["name"].startswith("张伟"))
    check("张伟10月分成335.02", approx(zrow["amount"], 335.02), str(zrow["amount"]))
    st, _ = call("POST", f"/api/settlements/{octid}/finalize", prop)
    check("10月结算定稿", st == 200)
    # 定稿后相关组件分成才挂账：明细中应标注冻结挂账
    st, sd = call("GET", f"/api/settlements/{octid}", prop)
    zrow = next(i for i in sd["items"] if i["name"].startswith("张伟"))
    check("结算明细标注冻结挂账335.02", approx(zrow["frozen_amount"], 335.02) and zrow["freeze_status"] == "held",
          f"{zrow['frozen_amount']}/{zrow['freeze_status']}")
    st, me = call("GET", "/api/me", zhang)
    check("冻结期间张伟余额保持605.59（分成挂账不入账）", approx(me["balance"], 605.59), str(me["balance"]))
    st, fd = call("GET", f"/api/freezes/{fid}", prop)
    check("冻结单累计挂账335.02", approx(fd["frozen_total"], 335.02), str(fd["frozen_total"]))
    held = fd["items"][0]
    check("挂账明细归属张伟且状态held", held["name"].startswith("张伟") and held["status"] == "held", str(held))

    # G. 责任确认：施工方 60% / 运维基金 5% / 公共收益 20% / 业主自担 15%
    st, _ = call("POST", f"/api/freezes/{fid}/release", prop,
                 {"installer_pct": 60, "fund_pct": 5, "public_pct": 20, "owner_pct": 10})
    check("责任比例合计95拒绝(400)", st == 400, str(st))
    st, rel = call("POST", f"/api/freezes/{fid}/release", prop, {
        "installer_pct": 60, "fund_pct": 5, "public_pct": 20, "owner_pct": 15,
        "liability_note": "第三方检测认定穿层密封不达标，施工方主责",
    })
    check("按比例释放成功", st == 200, str(rel))
    # 335.02: 施工方 201.01 + 基金 16.75 + 公共 67.00 = 284.76 发放；业主自担 50.26
    check("施工方赔付201.01", approx(rel["installer_compensation"], 201.01), str(rel["installer_compensation"]))
    check("基金承担16.75", approx(rel["fund_borne"], 16.75), str(rel["fund_borne"]))
    check("公共收益承担67.00", approx(rel["public_borne"], 67.00), str(rel["public_borne"]))
    check("发放住户284.76", approx(rel["released_to_owner"], 284.76), str(rel["released_to_owner"]))
    check("业主自担50.26", approx(rel["owner_borne"], 50.26), str(rel["owner_borne"]))
    st, me = call("GET", "/api/me", zhang)
    check("张伟余额=605.59+284.76=890.35", approx(me["balance"], 890.35), str(me["balance"]))
    st, lg = call("GET", f"/api/buildings/{bid}/ledger", prop)
    # 10 月公共净收益 335.02 计提 20%=67.00；释放动用基金 16.75 → 23.26+67.00-16.75=73.51
    check("释放后维修基金=73.51", approx(lg["maintenance_fund_balance"], 73.51), str(lg["maintenance_fund_balance"]))
    # 公共现金：85920.78 + 10月电费996.72 + 补贴120 + 施工方赔付201.01 - 发放284.76 = 86953.75
    check("释放后公共账户=86953.75", approx(lg["public_balance"], 86953.75), str(lg["public_balance"]))
    st, _ = call("POST", f"/api/freezes/{fid}/release", prop, {"installer_pct": 100})
    check("冻结单重复释放拒绝(409)", st == 409, str(st))
    # 处理结论写入后续屋顶巡检计划
    st, ins = call("GET", f"/api/buildings/{bid}/inspections", prop)
    plan = next((x for x in ins["items"] if x["work_order_id"] == wid), None)
    check("结论已写入后续屋顶巡检计划", plan is not None and plan["status"] == "scheduled", str(plan)[:200])
    check("巡检计划含责任比例结论", "施工方60%" in plan["conclusion"], plan["conclusion"])
    st, _ = call("POST", f"/api/inspections/{plan['id']}", prop,
                 {"status": "done", "result_note": "雨后复查无复渗"})
    check("登记巡检完成", st == 200)

    # H. 房屋转让：张伟 -> 刘洋（历史月份已全部结清）
    st, subs = call("GET", f"/api/buildings/{bid}/subscriptions", prop)
    sid = next(x["id"] for x in subs["items"] if x["name"].startswith("张伟") and x["type"] == "shareholder")
    st, users = call("GET", "/api/users?q=13900000006", prop)
    liu_id = next(u["id"] for u in users["items"] if u["phone"] == "13900000006")
    st, tr = call("POST", f"/api/buildings/{bid}/transfers", zhang, {
        "subscription_id": sid, "to_user_id": liu_id, "rights_transfer": True,
    })
    check("发起转让", st == 201 and tr["history_settled"] is True, str(tr))
    tid = tr["id"]
    st, _ = call("POST", f"/api/transfers/{tid}/confirm", prop)
    check("物业确认转让", st == 200)

    # I. 11 月发电与结算：30% 份额应归刘洋；冻结单已释放，不再挂账
    for iv in invs["items"]:
        st, _ = call("POST", f"/api/buildings/{bid}/generation", prop, {
            "period_month": "2026-11", "inverter_id": iv["id"], "kwh": 1200,
        })
        check(f"录入{iv['name']}11月电量", st == 200)
    st, r = call("POST", f"/api/buildings/{bid}/settlements", prop, {
        "period_month": "2026-11", "grid_price": 0.4153, "subsidy_per_kwh": 0.05,
    })
    check("11月结算生成", st == 200, str(r)[:200])
    novid = r["id"]
    st, detail = call("GET", f"/api/settlements/{novid}", prop)
    holders = {i["name"]: i["amount"] for i in detail["items"] if i["item_type"] == "shareholder"}
    check("张伟已无11月分成", not any(k.startswith("张伟") for k in holders), str(holders))
    check("刘洋(新业主)获30%分成335.02", approx(holders.get("刘洋(新业主)", 0), 335.02), str(holders))
    lrow = next(i for i in detail["items"] if i["name"].startswith("刘洋"))
    check("刘洋分成未被冻结（冻结单已释放）", float(lrow["frozen_amount"]) == 0, str(lrow["frozen_amount"]))
    call("POST", f"/api/settlements/{novid}/finalize", prop)
    st, me = call("GET", "/api/me", liu)
    check("刘洋余额=335.02", approx(me["balance"], 335.02), str(me["balance"]))
    st, me = call("GET", "/api/me", zhang)
    check("张伟余额保持890.35（历史收益不转移）", approx(me["balance"], 890.35), str(me["balance"]))

    # J. 公告 / 异议 / 异常解释 / 年报（4 个定稿月份）
    st, _ = call("POST", f"/api/buildings/{bid}/announcements", prop,
                 {"title": "11月公示", "content": "见结算", "pinned": True})
    check("发布公告", st == 201)
    st, poll = call("POST", f"/api/buildings/{bid}/polls", zhang,
                    {"title": "验证用异议", "content": "测试"})
    pid = poll["id"]
    call("POST", f"/api/polls/{pid}/vote", zhang, {"support": True})
    call("POST", f"/api/polls/{pid}/vote", liu, {"support": True})
    st, r = call("POST", f"/api/polls/{pid}/close", prop)
    check("异议投票成立", r["result"] == "approved", str(r))
    st, al = call("POST", f"/api/buildings/{bid}/alerts", zhang,
                  {"period_month": "2026-11", "reason": "脚本验证异常"})
    aid = al["id"]
    st, _ = call("POST", f"/api/alerts/{aid}/explain", prop, {"explanation": "脚本验证：计划检修 1 天"})
    check("异常解释", st == 200)
    st, r = call("POST", f"/api/buildings/{bid}/annual-reports", prop, {"year": 2026})
    check("年度报告生成（4个定稿月份）", st == 200 and r["summary"]["finalized_months"] == 4,
          f"{st} months={r.get('summary', {}).get('finalized_months')}")
    rid = r["id"]
    st, _ = call("POST", f"/api/annual-reports/{rid}/publish", prop)
    check("年报发布", st == 200)

    # K. 阶段回退必须有原因
    st, _ = call("POST", f"/api/buildings/{bid}/stage", prop, {"to_stage": "construction"})
    check("无原因回退被拒(409)", st == 409, str(st))
    st, r = call("POST", f"/api/buildings/{bid}/stage", prop,
                 {"to_stage": "construction", "reason": "设备供应延迟，组件到货推迟"})
    check("有原因回退成功", st == 200, str(r))

    print("\n全部端到端断言通过 ✅")


if __name__ == "__main__":
    main()
