#!/usr/bin/env python3
"""端到端 API 验证脚本（依赖 python3，无第三方包）。

用法：
    python3 scripts/verify_api.py [BASE_URL]

前置：服务已启动且数据库为初始种子状态（SEED_ON_START=true 首次启动后的干净库）。
脚本会校验：驾驶舱 → 9 月结算（维修基金动用+分摊）→ 定稿入账 → 房屋转让 →
次月分成归属切换 → 公告/异议/异常解释/年报 → 阶段回退原因强制。
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

    # D. 房屋转让：张伟 -> 刘洋
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

    # E. 10 月发电与结算：30% 份额应归刘洋
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
    st, detail = call("GET", f"/api/settlements/{octid}", prop)
    holders = {i["name"]: i["amount"] for i in detail["items"] if i["item_type"] == "shareholder"}
    check("张伟已无10月分成", not any(k.startswith("张伟") for k in holders), str(holders))
    check("刘洋(新业主)获30%分成335.02", approx(holders.get("刘洋(新业主)", 0), 335.02), str(holders))
    call("POST", f"/api/settlements/{octid}/finalize", prop)
    st, me = call("GET", "/api/me", liu)
    check("刘洋余额=335.02", approx(me["balance"], 335.02), str(me["balance"]))
    st, me = call("GET", "/api/me", zhang)
    check("张伟余额保持605.59（历史收益不转移）", approx(me["balance"], 605.59), str(me["balance"]))

    # F. 公告 / 异议 / 异常解释 / 年报
    st, _ = call("POST", f"/api/buildings/{bid}/announcements", prop,
                 {"title": "10月公示", "content": "见结算", "pinned": True})
    check("发布公告", st == 201)
    st, poll = call("POST", f"/api/buildings/{bid}/polls", zhang,
                    {"title": "验证用异议", "content": "测试"})
    pid = poll["id"]
    call("POST", f"/api/polls/{pid}/vote", zhang, {"support": True})
    call("POST", f"/api/polls/{pid}/vote", liu, {"support": True})
    st, r = call("POST", f"/api/polls/{pid}/close", prop)
    check("异议投票成立", r["result"] == "approved", str(r))
    st, al = call("POST", f"/api/buildings/{bid}/alerts", zhang,
                  {"period_month": "2026-10", "reason": "脚本验证异常"})
    aid = al["id"]
    st, _ = call("POST", f"/api/alerts/{aid}/explain", prop, {"explanation": "脚本验证：计划检修 1 天"})
    check("异常解释", st == 200)
    st, r = call("POST", f"/api/buildings/{bid}/annual-reports", prop, {"year": 2026})
    check("年度报告生成（3个定稿月份）", st == 200 and r["summary"]["finalized_months"] == 3, str(st))
    rid = r["id"]
    st, _ = call("POST", f"/api/annual-reports/{rid}/publish", prop)
    check("年报发布", st == 200)

    # G. 阶段回退必须有原因
    st, _ = call("POST", f"/api/buildings/{bid}/stage", prop, {"to_stage": "construction"})
    check("无原因回退被拒(409)", st == 409, str(st))
    st, r = call("POST", f"/api/buildings/{bid}/stage", prop,
                 {"to_stage": "construction", "reason": "设备供应延迟，组件到货推迟"})
    check("有原因回退成功", st == 200, str(r))

    # H. 顶楼漏水投诉与收益冻结（种子工单 + 10 月结算定稿时自动生效的冻结单）
    st, wlist = call("GET", f"/api/buildings/{bid}/work-orders", prop)
    leak = next((w for w in wlist["items"]
                 if w["period_month"] == "2026-10" and "支架" in w["title"]), None)
    check("找到10月新装支架漏水工单", leak is not None)
    lwid = leak["id"]
    check("工单识别顶楼住户已签署同意", leak["resident_consent_status"] == "signed", leak["resident_consent_status"])
    check("已临时止水/安抚/第三方检测",
          leak["temporary_stoppage"] and leak["reassured"] and leak["third_party_tested"], str(leak))
    check("工单关联5条证据(照片/施工/验收/雨量/同意书)", leak["attachment_count"] == 5, str(leak["attachment_count"]))
    st, atts = call("GET", f"/api/work-orders/{lwid}/attachments", prop)
    kinds = {a["kind"] for a in atts["items"]}
    check("证据类型齐全", {"onsite_photo", "construction_photo", "waterproof_acceptance", "rainfall", "consent"} <= kinds, str(kinds))
    st, cs = call("GET", f"/api/buildings/{bid}/consents", prop)
    check("顶楼同意书可查(RC-2026-013)", any(c["document_no"] == "RC-2026-013" for c in cs["items"]), str(cs["items"]))

    # 10 月结算定稿后冻结单生效：张伟份额已转让给刘洋→其目标持有 0；李娜冻50%、王强冻50%
    st, fl = call("GET", f"/api/buildings/{bid}/freezes", prop)
    fz = next((f for f in fl["items"] if f["period_month"] == "2026-10" and f["work_order_id"] == lwid), None)
    check("10月冻结单已随结算定稿生效", fz is not None and fz["status"] == "active", str(fz and fz["status"]))
    tf = {t["name"]: t for t in fz["targets"]}
    frozen_li = tf["李娜"]["frozen_amount"]
    frozen_wang = tf["王强"]["frozen_amount"]
    check("李娜冻结≈139.59(279.18的50%)", approx(frozen_li, 139.59, 0.02), str(frozen_li))
    check("王强冻结≈83.76(167.51的50%)", approx(frozen_wang, 83.76, 0.02), str(frozen_wang))
    total_frozen = frozen_li + frozen_wang
    fid = fz["id"]

    # 责任确认前发起新的冻结单可取消；责任未定时不能释放未生效单
    st, w2 = call("POST", f"/api/buildings/{bid}/work-orders", prop,
                  {"period_month": "2026-11", "title": "脚本验证漏水工单", "wo_type": "leak"})
    check("新建11月漏水工单", st == 201, str(w2))
    st, f2 = call("POST", f"/api/buildings/{bid}/freezes", prop,
                  {"work_order_id": w2["id"], "period_month": "2026-11",
                   "targets": [{"user_id": liu_id, "ratio": 100}]})
    check("创建待生效冻结单", st == 201, str(f2))
    st, _ = call("POST", f"/api/freezes/{f2['id']}/cancel", prop)
    check("待生效冻结单可取消", st == 200, str(st))

    # 按责任比例释放：施工方 50% / 运维基金 50%（基金不足部分公共收益兜底），尾差业主自负
    st, lg0 = call("GET", f"/api/buildings/{bid}/ledger", prop)
    fund_before = float(lg0["maintenance_fund_balance"])
    st, users = call("GET", "/api/users?q=13900000002", prop)
    li_before = float(next(u["balance"] for u in users["items"] if u["phone"] == "13900000002"))
    st, rel = call("POST", f"/api/freezes/{fid}/release", prop, {
        "note": "第三方检测：安装密封缺陷与原防水搭接各占一半",
        "allocation": [{"bearer": "installer", "ratio": 50}, {"bearer": "fund", "ratio": 50}],
    })
    check("冻结收益按责任比例释放", st == 200, str(rel))
    check("释放总额=冻结总额", approx(rel["total_frozen"], total_frozen, 0.02), str(rel))
    check("施工方赔付≈一半", approx(rel["installer"], total_frozen / 2, 0.03), str(rel))
    check("维修基金动用≤基金余额", rel["fund"] <= fund_before + 0.01, str(rel))
    check("基金不足部分公共收益兜底", rel["fund_shortfall"] > 0, str(rel))
    check("业主仅承担舍入尾差(≤0.02)", rel["owner"] <= 0.02, str(rel))
    st, fl = call("GET", f"/api/buildings/{bid}/freezes", prop)
    fz = next(f for f in fl["items"] if f["id"] == fid)
    check("冻结单状态=已释放", fz["status"] == "released", fz["status"])
    st, users = call("GET", "/api/users?q=13900000002", prop)
    li_after = float(next(u["balance"] for u in users["items"] if u["phone"] == "13900000002"))
    check("李娜拿回冻结收益(扣尾差)", approx(li_after - li_before, frozen_li, 0.03), f"{li_before}->{li_after}")
    st, wlist = call("GET", f"/api/buildings/{bid}/work-orders", prop)
    leak = next(w for w in wlist["items"] if w["id"] == lwid)
    check("工单责任回写多方分担(shared)", leak["responsibility"] == "shared", leak["responsibility"])
    st, ins = call("GET", f"/api/buildings/{bid}/roof-inspections", prop)
    follow = next((i for i in ins["items"] if i["work_order_id"] == lwid), None)
    check("处理结论写入后续屋顶巡检计划", follow is not None and follow["status"] == "scheduled", str(follow))
    st, _ = call("POST", f"/api/roof-inspections/{follow['id']}/complete", prop,
                 {"result_note": "支架穿层封堵完好，雨后复查无复漏"})
    check("登记屋顶巡检结果", st == 200, str(st))

    print("\n全部端到端断言通过 ✅")


if __name__ == "__main__":
    main()
