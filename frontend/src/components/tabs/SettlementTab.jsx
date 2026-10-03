import React, { useEffect, useState } from 'react'
import { api, fmtMoney, fmtNum } from '../../lib/api'
import { Badge, handleErr, useToast } from '../ui'

export default function SettlementTab({ user, building }) {
  const { notify } = useToast()
  const isManager = user.role === 'admin' || user.role === 'property'
  const [periods, setPeriods] = useState([])
  const [detail, setDetail] = useState(null)
  const [preview, setPreview] = useState(null)
  const [form, setForm] = useState({
    period_month: '2026-09', grid_price: 0.4153, subsidy_per_kwh: 0.05,
    om_fee: 0, tax: 0, compensation: 0,
  })

  const load = async () => {
    const r = await api('GET', `/buildings/${building.id}/settlements`)
    setPeriods(r.items || [])
  }
  useEffect(() => { load() }, [building.id])

  const generate = async () => {
    try {
      const r = await handleErr(api('POST', `/buildings/${building.id}/settlements`, form), notify)
      setPreview(r.settlement)
      openDetail(r.id)
      notify('结算草稿已生成，核对后可定稿入账')
      load()
    } catch { /* toast */ }
  }

  const openDetail = async (id) => {
    try {
      const r = await api('GET', `/settlements/${id}`)
      setDetail(r)
    } catch (e) { notify(e.message, true) }
  }

  const finalize = async () => {
    if (!window.confirm('定稿后将把收益计入住户余额、公共账户与维修基金，且不可再修改。确认定稿？')) return
    try {
      await handleErr(api('POST', `/settlements/${detail.id}/finalize`), notify)
      notify('结算已定稿并入账')
      setDetail(null); setPreview(null)
      load()
    } catch { /* toast */ }
  }

  return (
    <>
      {isManager && (
        <div className="card">
          <h2>生成月度结算</h2>
          <p className="muted small">
            依据实际发电量、住户组件占比、公共部分比例、运维费、税费与当月维修工单生成明细。
            同一月份可反复试算覆盖草稿；<b>定稿后不可变更</b>，工单责任也将锁定。
          </p>
          <div className="form-row">
            <label className="field"><span>结算月份</span><input value={form.period_month} onChange={(e) => setForm({ ...form, period_month: e.target.value })} placeholder="YYYY-MM" /></label>
            <label className="field"><span>上网电价 元/kWh</span><input type="number" step="0.0001" value={form.grid_price} onChange={(e) => setForm({ ...form, grid_price: Number(e.target.value) })} /></label>
            <label className="field"><span>度电补贴 元/kWh</span><input type="number" step="0.0001" value={form.subsidy_per_kwh} onChange={(e) => setForm({ ...form, subsidy_per_kwh: Number(e.target.value) })} /></label>
            <label className="field"><span>运维费 元</span><input type="number" value={form.om_fee} onChange={(e) => setForm({ ...form, om_fee: Number(e.target.value) })} /></label>
            <label className="field"><span>税费 元</span><input type="number" value={form.tax} onChange={(e) => setForm({ ...form, tax: Number(e.target.value) })} /></label>
            <label className="field"><span>安装商/保险赔偿 元</span><input type="number" value={form.compensation} onChange={(e) => setForm({ ...form, compensation: Number(e.target.value) })} /></label>
            <button className="primary" onClick={generate}>试算/生成</button>
          </div>
          {preview && <PreviewBox s={preview} />}
        </div>
      )}

      <div className="card">
        <h2>月度结算单</h2>
        <table>
          <thead>
            <tr>
              <th>月份</th><th>状态</th><th className="num">发电量</th><th className="num">电费收入</th>
              <th className="num">补贴</th><th className="num">运维</th><th className="num">税费</th>
              <th className="num">维修支出</th><th className="num">冻结中</th><th className="num">可分配净额</th><th></th>
            </tr>
          </thead>
          <tbody>
            {periods.map((p) => (
              <tr key={p.id}>
                <td>{p.period_month}</td>
                <td><Badge value={p.status} /></td>
                <td className="num">{fmtNum(p.total_kwh)} kWh</td>
                <td className="num">{fmtMoney(p.gross_revenue)}</td>
                <td className="num">{fmtMoney(p.total_subsidy)}</td>
                <td className="num">{fmtMoney(p.om_fee)}</td>
                <td className="num">{fmtMoney(p.tax)}</td>
                <td className="num">{fmtMoney(p.work_order_cost)}</td>
                <td className="num">{Number(p.frozen_total) > 0 ? <span className="badge amber">{fmtMoney(p.frozen_total)}</span> : '—'}</td>
                <td className="num"><b>{fmtMoney(p.distributable)}</b></td>
                <td><button className="sm" onClick={() => openDetail(p.id)}>查看明细</button></td>
              </tr>
            ))}
          </tbody>
        </table>
        {periods.length === 0 && <div className="list-empty">暂无结算单</div>}
      </div>

      {detail && (
        <div className="card">
          <div className="card-title">
            <h2>{detail.period_month} 结算明细 {detail.status === 'finalized' && <Badge value="finalized" />}</h2>
            <button className="sm" onClick={() => setDetail(null)}>关闭</button>
          </div>
          <dl className="kv" style={{ marginBottom: 12 }}>
            <dt>发电量</dt><dd>{fmtNum(detail.total_kwh)} kWh（电价 {detail.grid_price} 元，补贴 {detail.subsidy_per_kwh} 元/kWh）</dd>
            <dt>住户/公共占比</dt><dd>{Number(100 - Number(detail.public_ratio)).toFixed(2)}% / {Number(detail.public_ratio).toFixed(2)}%</dd>
            <dt>维修基金</dt><dd>
              公共净收益计提 {detail.maintenance_fund_ratio}%（本期计提 ¥{fmtMoney(detail.maintenance_fund_addition)}，
              动用 ¥{fmtMoney(detail.maintenance_fund_used)}）
            </dd>
            <dt>责任赔偿</dt><dd>¥{fmtMoney(detail.compensation)}（安装商/保险责任，不冲减住户收益）</dd>
            {Number(detail.frozen_total) > 0 && (
              <>
                <dt>漏水冻结</dt>
                <dd>
                  本期暂时冻结 <b style={{ color: 'var(--amber)' }}>¥{fmtMoney(detail.frozen_total)}</b>，
                  实际入账 ¥{fmtMoney(detail.payable_total)}；冻结收益待责任确认后在「漏水处置/收益冻结」页按比例释放。
                </dd>
              </>
            )}
          </dl>
          <table>
            <thead>
              <tr>
                <th>分配对象</th><th>类型</th><th className="num">占比</th><th className="num">电量</th>
                <th className="num">电费</th><th className="num">补贴</th><th className="num">运维分摊</th>
                <th className="num">税分摊</th><th className="num">维修分摊</th>
                <th className="num">应分得</th><th className="num">冻结</th><th className="num">实入账</th>
              </tr>
            </thead>
            <tbody>
              {detail.items.map((it) => (
                <tr key={it.id}>
                  <td>{it.name}</td>
                  <td><Badge value={it.item_type === 'shareholder' ? 'shareholder' : it.item_type === 'public' ? 'public_only' : 'pending'} /></td>
                  <td className="num">{it.ratio ? Number(it.ratio).toFixed(2) + '%' : '—'}</td>
                  <td className="num">{fmtNum(it.kwh)}</td>
                  <td className="num">{fmtMoney(it.revenue)}</td>
                  <td className="num">{fmtMoney(it.subsidy)}</td>
                  <td className="num">{fmtMoney(it.om_share)}</td>
                  <td className="num">{fmtMoney(it.tax_share)}</td>
                  <td className="num">{fmtMoney(it.wo_share)}</td>
                  <td className="num"><b>{fmtMoney(it.amount)}</b></td>
                  <td className="num">{Number(it.frozen_amount) > 0 ? <span className="badge amber">{fmtMoney(it.frozen_amount)}</span> : '—'}</td>
                  <td className="num">{fmtMoney(it.payable_amount)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="small muted" style={{ marginTop: 8 }}>
            维修基金行：正数为本期从公共收益计提，负数为本期动用基金支付维修。
          </div>
          {isManager && detail.status === 'draft' && (
            <div className="btn-row" style={{ marginTop: 12 }}>
              <button className="primary" onClick={finalize}>定稿并入账</button>
              <span className="muted small">定稿后住户余额、公共账户、维修基金与台账流水同步更新</span>
            </div>
          )}
        </div>
      )}
    </>
  )
}

function PreviewBox({ s }) {
  return (
    <div className="muted-box" style={{ marginTop: 12 }}>
      <b>试算结果：</b>
      发电 {fmtNum(s.total_kwh)}kWh，电费 ¥{fmtMoney(s.gross_revenue)}，补贴 ¥{fmtMoney(s.total_subsidy)}；
      维修净支出 ¥{fmtMoney(s.repair_deduct)}（动用基金 ¥{fmtMoney(s.fund_used)}）；
      本期计提维修基金 ¥{fmtMoney(s.fund_addition)}；
      <b> 可分配净额 ¥{fmtMoney(s.distributable)}</b>。
    </div>
  )
}
