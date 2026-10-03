import React, { useEffect, useState } from 'react'
import { api, fmtMoney, EVIDENCE_TYPES, ACTION_TYPES, FREEZE_STATUS } from '../../lib/api'
import { Badge, handleErr, useToast } from '../ui'

const EVIDENCE_META = {
  construction_photo: { label: '施工照片', hint: '支架就位、穿层节点等现场照片' },
  waterproof_acceptance: { label: '屋顶防水验收', hint: '防水工程验收记录与结论' },
  rainfall: { label: '雨量记录', hint: '降雨时段与降雨量（mm）' },
  consent: { label: '顶楼住户同意签署', hint: '施工前该住户是否书面同意' },
  inspection_report: { label: '第三方检测报告', hint: '责任无法确认时的第三方检测' },
}

function StatusPill({ value, map, tone = '' }) {
  return (
    <span className={'badge ' + tone}>{map[value] || value}</span>
  )
}

export default function LeakCasePanel({ user, building, woId, onClose }) {
  const { notify } = useToast()
  const isManager = user.role === 'admin' || user.role === 'property'
  const [wo, setWo] = useState(null)
  const [subs, setSubs] = useState([])
  const [ev, setEv] = useState({ evidence_type: 'rainfall', title: '', detail: '', recorded_on: '', amount: '', source: '', file_url: '' })
  const [act, setAct] = useState({ action_type: 'temporary_seal', content: '', cost: 0 })
  const [fz, setFz] = useState({ scope: 'targeted', period_month: wo?.period_month || '2026-10', target_user_ids: [] })
  const [rel, setRel] = useState({ installer_pct: 0, fund_pct: 0, public_pct: 0, owner_pct: 100, liability_note: '' })

  const load = async () => {
    const d = await handleErr(api('GET', `/work-orders/${woId}`), notify)
    setWo(d)
    setFz((f) => ({ ...f, period_month: d.period_month, target_user_ids: f.target_user_ids.length ? f.target_user_ids : (d.reporter_id ? [d.reporter_id] : []) }))
    const s = await api('GET', `/buildings/${building.id}/subscriptions`).catch(() => ({ items: [] }))
    setSubs((s.items || []).filter((x) => x.type === 'shareholder' && x.status !== 'refunded'))
  }
  useEffect(() => { load() }, [woId])

  if (!wo) return <div className="card"><div className="muted">加载中…</div></div>

  const addEvidence = async () => {
    if (!ev.title) return notify('请填写材料标题/摘要', true)
    const body = { ...ev, amount: ev.amount === '' ? null : Number(ev.amount) }
    try {
      await handleErr(api('POST', `/work-orders/${woId}/evidences`, body), notify)
      notify('证据材料已关联')
      setEv({ ...ev, title: '', detail: '', amount: '', file_url: '' })
      load()
    } catch { /* toast */ }
  }

  const addAction = async () => {
    if (!act.content) return notify('请填写处置内容', true)
    try {
      await handleErr(api('POST', `/work-orders/${woId}/actions`, act), notify)
      notify('处置过程已记录')
      setAct({ ...act, content: '', cost: 0 })
      load()
    } catch { /* toast */ }
  }

  const createFreeze = async () => {
    try {
      await handleErr(api('POST', `/buildings/${building.id}/freezes`, {
        work_order_id: woId,
        period_month: fz.period_month,
        scope: fz.scope,
        target_user_ids: fz.scope === 'targeted' ? fz.target_user_ids : [],
      }), notify)
      notify('已设立收益冻结，相关组件后续月结分成将挂账')
      load()
    } catch { /* toast */ }
  }

  const toggleTarget = (uid) => {
    setFz((f) => ({
      ...f,
      target_user_ids: f.target_user_ids.includes(uid)
        ? f.target_user_ids.filter((x) => x !== uid)
        : [...f.target_user_ids, uid],
    }))
  }

  const release = async () => {
    const sum = Number(rel.installer_pct) + Number(rel.fund_pct) + Number(rel.public_pct) + Number(rel.owner_pct)
    if (Math.abs(sum - 100) > 0.001) return notify(`责任比例合计须为 100%，当前 ${sum}%`, true)
    if (!window.confirm('确认按该责任比例释放冻结收益？释放后将发放住户、登记赔付/基金/公共收益承担，并生成后续巡检计划。')) return
    try {
      const r = await handleErr(api('POST', `/freezes/${wo.freeze.id}/release`, {
        installer_pct: Number(rel.installer_pct), fund_pct: Number(rel.fund_pct),
        public_pct: Number(rel.public_pct), owner_pct: Number(rel.owner_pct),
        liability_note: rel.liability_note,
      }), notify)
      notify(`已释放：发放住户 ¥${fmtMoney(r.released_to_owner)}，业主自担 ¥${fmtMoney(r.owner_borne)}`)
      load()
    } catch { /* toast */ }
  }

  const evidences = wo.evidences || []
  const actions = wo.actions || []
  const signed = evidences.some((e) => e.evidence_type === 'consent')
  const reporterName = subs.find((s) => s.user_id === wo.reporter_id)
  const reporterText = reporterName ? `${reporterName.name}（${reporterName.unit_no}）` : (wo.reporter_id ? `住户#${wo.reporter_id}` : '—')

  return (
    <div className="card">
      <div className="card-title">
        <h2>漏水投诉档案 #{wo.id} {wo.title}</h2>
        <button className="sm" onClick={onClose}>关闭</button>
      </div>
      <p className="muted small">
        归属月份 {wo.period_month} · 报修人 {reporterText} ·
        工单状态 <Badge value={wo.status} />
        {signed ? <span className="badge green" style={{ marginLeft: 8 }}>住户此前已签署同意</span>
          : <span className="badge amber" style={{ marginLeft: 8 }}>未见同意签署记录</span>}
      </p>

      {/* 证据链 */}
      <h3>证据链（施工照片 / 防水验收 / 雨量 / 同意签署 / 第三方检测）</h3>
      <table>
        <thead>
          <tr><th>类型</th><th>材料</th><th>日期</th><th>数值/来源</th><th>录入人</th></tr>
        </thead>
        <tbody>
          {evidences.map((e) => (
            <tr key={e.id}>
              <td><StatusPill value={e.evidence_type} map={EVIDENCE_TYPES} tone="blue" /></td>
              <td>
                <b>{e.title}</b>
                {e.detail && <div className="small muted">{e.detail}</div>}
                {e.file_url && <div className="small">📎 {e.file_url}</div>}
              </td>
              <td className="small">{e.recorded_on || '—'}</td>
              <td className="small">
                {e.evidence_type === 'rainfall' && Number(e.amount) > 0 ? `${Number(e.amount)} mm` : ''}
                {e.source ? ` · ${e.source}` : ''}
              </td>
              <td className="small">{e.author}</td>
            </tr>
          ))}
          {evidences.length === 0 && (
            <tr><td colSpan="5" className="list-empty">尚未关联证据材料</td></tr>
          )}
        </tbody>
      </table>
      <div className="form-row" style={{ marginTop: 8 }}>
        <label className="field"><span>材料类型</span>
          <select value={ev.evidence_type} onChange={(e) => setEv({ ...ev, evidence_type: e.target.value })}>
            {Object.entries(EVIDENCE_META).map(([v, m]) => <option key={v} value={v}>{m.label}</option>)}
          </select>
        </label>
        <label className="field" style={{ flex: 2 }}><span>标题/摘要 *</span>
          <input value={ev.title} onChange={(e) => setEv({ ...ev, title: e.target.value })} placeholder={EVIDENCE_META[ev.evidence_type].hint} />
        </label>
        <label className="field"><span>日期</span>
          <input type="date" value={ev.recorded_on} onChange={(e) => setEv({ ...ev, recorded_on: e.target.value })} />
        </label>
        {ev.evidence_type === 'rainfall' && (
          <label className="field"><span>降雨量 mm</span>
            <input type="number" value={ev.amount} onChange={(e) => setEv({ ...ev, amount: e.target.value })} />
          </label>
        )}
        <label className="field"><span>来源</span>
          <input value={ev.source} onChange={(e) => setEv({ ...ev, source: e.target.value })} placeholder="气象局/施工方/检测机构" />
        </label>
      </div>
      <div className="form-row">
        <label className="field" style={{ flex: 3 }}><span>详细说明 / 附件引用</span>
          <input value={ev.detail} onChange={(e) => setEv({ ...ev, detail: e.target.value })} placeholder="验收结论、签署情况或附件 URL" />
        </label>
        <label className="field" style={{ flex: 2 }}><span>附件 URL</span>
          <input value={ev.file_url} onChange={(e) => setEv({ ...ev, file_url: e.target.value })} />
        </label>
        <button className="primary" onClick={addEvidence}>关联证据</button>
      </div>

      {/* 处置过程 */}
      <h3 style={{ marginTop: 16 }}>处置过程（责任无法确认时：临时止水 · 住户安抚 · 第三方检测）</h3>
      {actions.length === 0 && <div className="list-empty">暂无处置记录</div>}
      <ul className="timeline">
        {actions.map((a) => (
          <li key={a.id}>
            <StatusPill value={a.action_type} map={ACTION_TYPES} tone="amber" />
            <span style={{ marginLeft: 8 }}>{a.content}</span>
            {Number(a.cost) > 0 && <span className="small muted" style={{ marginLeft: 8 }}>费用 ¥{fmtMoney(a.cost)}</span>}
            <span className="small muted" style={{ marginLeft: 8 }}>— {a.operator}</span>
          </li>
        ))}
      </ul>
      {isManager && (
        <div className="form-row" style={{ marginTop: 8 }}>
          <label className="field"><span>处置类型</span>
            <select value={act.action_type} onChange={(e) => setAct({ ...act, action_type: e.target.value })}>
              {Object.entries(ACTION_TYPES).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
          </label>
          <label className="field" style={{ flex: 3 }}><span>处置内容 *</span>
            <input value={act.content} onChange={(e) => setAct({ ...act, content: e.target.value })} placeholder="如：屋面铺设临时防水卷材止水，安排上门安抚" />
          </label>
          <label className="field"><span>费用</span>
            <input type="number" value={act.cost} onChange={(e) => setAct({ ...act, cost: Number(e.target.value) })} />
          </label>
          <button onClick={addAction}>记录处置</button>
        </div>
      )}

      {/* 收益冻结 */}
      <h3 style={{ marginTop: 16 }}>相关组件收益冻结</h3>
      {!wo.freeze && (
        <>
          <p className="muted small">维修期间可暂时冻结渗漏点附近相关组件的收益，月结定稿时挂账，待责任确认后按比例释放。</p>
          {isManager && wo.status !== 'resolved' ? (
            <>
              <div className="form-row">
                <label className="field"><span>冻结起始月份</span>
                  <input value={fz.period_month} onChange={(e) => setFz({ ...fz, period_month: e.target.value })} placeholder="YYYY-MM" />
                </label>
                <label className="field"><span>冻结范围</span>
                  <select value={fz.scope} onChange={(e) => setFz({ ...fz, scope: e.target.value })}>
                    <option value="targeted">相关组件（指定持有人）</option>
                    <option value="all">整楼全部组件</option>
                  </select>
                </label>
              </div>
              {fz.scope === 'targeted' && (
                <div className="form-row">
                  {subs.map((s) => (
                    <label key={s.user_id} className="check-pill">
                      <input type="checkbox" checked={fz.target_user_ids.includes(s.user_id)} onChange={() => toggleTarget(s.user_id)} />
                      {s.name}（{s.unit_no}，{Number(s.ratio).toFixed(1)}%）
                    </label>
                  ))}
                </div>
              )}
              <div className="btn-row"><button className="primary" onClick={createFreeze}>设立收益冻结</button></div>
            </>
          ) : <div className="muted small">工单结案后不可再设立冻结。</div>}
        </>
      )}
      {wo.freeze && <FreezeBox freeze={wo.freeze} isManager={isManager} rel={rel} setRel={setRel} onRelease={release} />}
    </div>
  )
}

function FreezeBox({ freeze, isManager, rel, setRel, onRelease }) {
  const [detail, setDetail] = useState(null)
  const loadDetail = async () => {
    const d = await api('GET', `/freezes/${freeze.id}`)
    setDetail(d)
  }
  useEffect(() => { loadDetail() }, [freeze.id, freeze.status])
  const sum = Number(rel.installer_pct) + Number(rel.fund_pct) + Number(rel.public_pct) + Number(rel.owner_pct)
  const autoOwner = () => setRel({ ...rel, owner_pct: Math.round((100 - Number(rel.installer_pct) - Number(rel.fund_pct) - Number(rel.public_pct)) * 100) / 100 })

  return (
    <div className="muted-box">
      <div className="form-row">
        <StatusPill value={freeze.status} map={FREEZE_STATUS} tone={freeze.status === 'frozen' ? 'amber' : 'green'} />
        <span className="small">起始月份 {freeze.period_month} · 范围 {freeze.scope === 'all' ? '整楼全部组件' : '相关组件'}</span>
        {detail?.targets?.length > 0 && (
          <span className="small muted">持有人：{detail.targets.map((t) => `${t.name}(${t.unit_no})`).join('、')}</span>
        )}
      </div>
      <dl className="kv">
        <dt>累计冻结挂账</dt><dd>¥{fmtMoney(freeze.frozen_total)}</dd>
        <dt>已发放住户</dt><dd>¥{fmtMoney(freeze.released_to_owner)}</dd>
        <dt>业主自担</dt><dd>¥{fmtMoney(freeze.owner_borne_total)}</dd>
        {freeze.status === 'released' && <>
          <dt>施工方赔付</dt><dd>¥{fmtMoney(freeze.installer_compensation)}（{Number(freeze.installer_pct)}%）</dd>
          <dt>运维基金承担</dt><dd>¥{fmtMoney(freeze.fund_borne)}（{Number(freeze.fund_pct)}%）</dd>
          <dt>公共收益支付</dt><dd>¥{fmtMoney(freeze.public_borne)}（{Number(freeze.public_pct)}%）</dd>
        </>}
      </dl>
      {freeze.liability_note && <p className="small muted">责任说明：{freeze.liability_note}</p>}

      <table>
        <thead><tr><th>挂账月份</th><th>住户</th><th className="num">冻结金额</th><th className="num">已发放</th><th className="num">自担</th><th>状态</th></tr></thead>
        <tbody>
          {(detail?.items || []).map((it) => (
            <tr key={it.id}>
              <td className="small">{it.period_month}</td>
              <td>{it.name}（{it.unit_no}）</td>
              <td className="num">{fmtMoney(it.amount)}</td>
              <td className="num">{fmtMoney(it.released_amount)}</td>
              <td className="num">{fmtMoney(it.owner_borne)}</td>
              <td><StatusPill value={it.status} map={{ held: '挂账中', released: '已释放' }} tone={it.status === 'held' ? 'amber' : 'green'} /></td>
            </tr>
          ))}
          {detail && detail.items.length === 0 && (
            <tr><td colSpan="6" className="list-empty">冻结单已设立，后续月结定稿时相关住户分成将在此挂账</td></tr>
          )}
        </tbody>
      </table>

      {isManager && freeze.status === 'frozen' && Number(freeze.frozen_total) > 0 && (
        <div style={{ marginTop: 10 }}>
          <b>责任确认与按比例释放</b>
          <p className="small muted">四项责任比例合计 100%。施工方赔付与公共收益支付发放给住户；运维基金承担部分从维修基金列支；业主自担部分冲减不发放。</p>
          <div className="form-row">
            <label className="field"><span>施工方赔付 %</span><input type="number" step="0.01" value={rel.installer_pct} onChange={(e) => setRel({ ...rel, installer_pct: Number(e.target.value) })} onBlur={autoOwner} /></label>
            <label className="field"><span>运维基金承担 %</span><input type="number" step="0.01" value={rel.fund_pct} onChange={(e) => setRel({ ...rel, fund_pct: Number(e.target.value) })} onBlur={autoOwner} /></label>
            <label className="field"><span>公共收益支付 %</span><input type="number" step="0.01" value={rel.public_pct} onChange={(e) => setRel({ ...rel, public_pct: Number(e.target.value) })} onBlur={autoOwner} /></label>
            <label className="field"><span>业主自担 %</span><input type="number" step="0.01" value={rel.owner_pct} onChange={(e) => setRel({ ...rel, owner_pct: Number(e.target.value) })} /></label>
            <span className={'small ' + (Math.abs(sum - 100) < 0.001 ? '' : 'err-text')}>合计 {Math.round(sum * 100) / 100}%</span>
          </div>
          <label className="field" style={{ marginTop: 6 }}><span>责任认定说明（写入后续巡检计划）</span>
            <input value={rel.liability_note} onChange={(e) => setRel({ ...rel, liability_note: e.target.value })} placeholder="如：第三方检测认定支架穿层密封不达标，施工方主责" />
          </label>
          <div className="btn-row" style={{ marginTop: 8 }}>
            <button className="primary" onClick={onRelease}>按比例释放冻结</button>
            <button className="sm" onClick={loadDetail}>刷新挂账</button>
          </div>
        </div>
      )}
      {isManager && freeze.status === 'frozen' && Number(freeze.frozen_total) === 0 && (
        <p className="small muted">尚无挂账金额：待下一期月度结算定稿后，相关住户分成将被冻结。</p>
      )}
    </div>
  )
}
