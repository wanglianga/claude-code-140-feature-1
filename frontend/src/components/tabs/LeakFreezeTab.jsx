import React, { useEffect, useMemo, useState } from 'react'
import { api, fmtMoney } from '../../lib/api'
import { Badge, handleErr, useToast } from '../ui'

const ATTACH_KINDS = {
  onsite_photo: '现场渗水照片',
  construction_photo: '施工照片',
  waterproof_acceptance: '屋顶防水验收',
  rainfall: '雨量记录',
  consent: '住户同意书',
  third_party_report: '第三方检测报告',
  other: '其他证据',
}
const CONSENT_STATUS = { unknown: '同意待核查', signed: '已签署同意', declined: '未签署/拒绝' }
const ACTION_LABELS = {
  temporary_stoppage: '记录临时止水',
  reassure: '记录住户安抚',
  third_party_test: '委托第三方检测',
}
const BEARERS = [
  { v: 'installer', l: '施工方赔付' },
  { v: 'fund', l: '运维（维修）基金' },
  { v: 'public', l: '楼栋公共收益' },
  { v: 'owner', l: '业主自负' },
]
const FREEZE_STATUS = { pending: '待生效', active: '冻结中', released: '已释放', cancelled: '已取消' }

export default function LeakFreezeTab({ user, building }) {
  const { notify } = useToast()
  const isManager = user.role === 'admin' || user.role === 'property'
  const [wos, setWos] = useState([])
  const [subs, setSubs] = useState([])
  const [members, setMembers] = useState([])
  const [consents, setConsents] = useState([])
  const [freezes, setFreezes] = useState([])
  const [inspections, setInspections] = useState([])
  const [openWO, setOpenWO] = useState(0)

  const load = async () => {
    const [w, sb, mb, cs, fz, ins] = await Promise.all([
      api('GET', `/buildings/${building.id}/work-orders`),
      api('GET', `/buildings/${building.id}/subscriptions`).catch(() => ({ items: [] })),
      api('GET', `/buildings/${building.id}/members`).catch(() => ({ items: [] })),
      api('GET', `/buildings/${building.id}/consents`).catch(() => ({ items: [] })),
      api('GET', `/buildings/${building.id}/freezes`).catch(() => ({ items: [] })),
      api('GET', `/buildings/${building.id}/roof-inspections`).catch(() => ({ items: [] })),
    ])
    setWos((w.items || []).filter((x) => x.wo_type === 'leak'))
    setSubs((sb.items || []).filter((x) => x.type === 'shareholder'))
    setMembers(mb.items || [])
    setConsents(cs.items || [])
    setFreezes(fz.items || [])
    setInspections(ins.items || [])
  }
  useEffect(() => { load() }, [building.id])

  const act = async (path, body, okMsg) => {
    try {
      await handleErr(api('POST', path, body), notify)
      notify(okMsg)
      load()
      return true
    } catch { return false }
  }

  const leakWos = wos
  const shareholders = useMemo(() => {
    const m = new Map((members).map((x) => [x.id, x]))
    return subs.map((s) => ({ ...s, member: m.get(s.user_id) }))
  }, [subs, members])

  return (
    <>
      <div className="card">
        <h2>顶楼漏水投诉处置</h2>
        <p className="muted small">
          雨后顶楼住户在新装光伏支架位置投诉渗水时，平台把<b>施工照片、屋顶防水验收、雨量记录、住户此前是否签署同意</b>
          归集到工单，并安排<b>临时止水、住户安抚、第三方检测</b>。维修期间可暂时冻结相关组件收益，
          责任确认后按比例决定由<b>施工方赔付、运维基金承担、楼栋公共收益支付或业主自负</b>；处理结论写入后续屋顶巡检计划。
        </p>
        {leakWos.length === 0 && <div className="list-empty">暂无屋顶漏水工单（可在「维修工单」页报修时选择类型“屋顶漏水”）</div>}
        {leakWos.map((wo) => (
          <LeakWO key={wo.id} wo={wo} isManager={isManager} open={openWO === wo.id}
            onToggle={() => setOpenWO(openWO === wo.id ? 0 : wo.id)} act={act} reload={load}
            shareholders={shareholders} consents={consents} notify={notify} />
        ))}
      </div>

      <div className="card">
        <h2>收益冻结与按责任释放</h2>
        <FreezePanel isManager={isManager} leakWos={leakWos} shareholders={shareholders}
          freezes={freezes} act={act} reload={load} notify={notify} building={building} />
      </div>

      <div className="card">
        <h2>顶楼住户屋顶使用同意书</h2>
        <ConsentPanel isManager={isManager} members={members} consents={consents} act={act} building={building} />
      </div>

      <div className="card">
        <h2>后续屋顶巡检计划</h2>
        <InspectionPanel isManager={isManager} inspections={inspections} act={act} building={building} />
      </div>
    </>
  )
}

function LeakWO({ wo, isManager, open, onToggle, act, reload, shareholders, consents, notify }) {
  const [attachs, setAttachs] = useState([])
  const [attForm, setAttForm] = useState({ kind: 'onsite_photo', title: '', detail: '', recorded_at: '' })
  const [actionNote, setActionNote] = useState('')
  const [testOrg, setTestOrg] = useState('')
  const [conclusion, setConclusion] = useState({ note: '', responsibility: '', cost: 0 })
  const [alloc, setAlloc] = useState([{ bearer: 'installer', ratio: 60 }, { bearer: 'fund', ratio: 40 }])

  const loadAtt = async () => {
    const r = await api('GET', `/work-orders/${wo.id}/attachments`).catch(() => ({ items: [] }))
    setAttachs(r.items || [])
  }
  useEffect(() => { if (open) loadAtt() }, [open, wo.id])

  const doAction = async (action, extra = {}) => {
    const note = action === 'third_party_test' ? actionNote : actionNote
    const body = { action, note, ...extra }
    if (await act(`/work-orders/${wo.id}/leak-action`, body,
      action === 'temporary_stoppage' ? '已记录临时止水'
        : action === 'reassure' ? '已记录住户安抚'
          : action === 'third_party_test' ? '已记录第三方检测安排' : '处理结论已保存，并写入后续屋顶巡检计划')) {
      setActionNote('')
    }
  }

  const addAttach = async () => {
    if (!attForm.title) return notify('请填写证据标题', true)
    if (await act(`/work-orders/${wo.id}/attachments`, attForm, '证据已关联到工单')) {
      setAttForm({ kind: 'onsite_photo', title: '', detail: '', recorded_at: '' })
    }
  }

  const conclude = async () => {
    if (!conclusion.note) return notify('请填写处理结论', true)
    const body = {
      action: 'conclude', note: conclusion.note,
      responsibility: conclusion.responsibility, cost: Number(conclusion.cost) || 0,
      allocation: conclusion.responsibility === 'shared'
        ? alloc.map((a) => ({ bearer: a.bearer, ratio: Number(a.ratio) || 0 })) : undefined,
    }
    await act(`/work-orders/${wo.id}/leak-action`, body, '处理结论已保存')
    setConclusion({ note: '', responsibility: '', cost: 0 })
  }

  const consent = consents.find((c) => wo.unit_no && c.unit_no === wo.unit_no)
  const allocSum = alloc.reduce((s, a) => s + (Number(a.ratio) || 0), 0)

  return (
    <div className="muted-box" style={{ marginTop: 12 }}>
      <div className="card-title" style={{ cursor: 'pointer' }} onClick={onToggle}>
        <div>
          <b>{wo.title}</b> <span className="small muted">（{wo.period_month} · {wo.unit_no || '顶楼'} · 报修人 {wo.reporter}）</span>
          <div style={{ marginTop: 4 }}>
            <Badge value={wo.status} />
            <span className="small" style={{ marginLeft: 6 }}>责任：{wo.responsibility === 'undetermined' ? '待确认' : wo.responsibility}</span>
            <span className="small" style={{ marginLeft: 8 }}>同意：{CONSENT_STATUS[wo.resident_consent_status] || wo.resident_consent_status}</span>
            {wo.freeze_count > 0 && <span className="small" style={{ marginLeft: 8 }}>· 收益冻结：{FREEZE_STATUS[wo.freeze_status] || wo.freeze_status}</span>}
          </div>
        </div>
        <button className="sm">{open ? '收起' : '展开处置'}</button>
      </div>

      {open && (
        <>
          <div className="small" style={{ whiteSpace: 'pre-line', margin: '6px 0' }}>{wo.description}</div>
          <div className="btn-row" style={{ marginBottom: 8 }}>
            {wo.temporary_stoppage ? <Badge value="resolved" /> : <span className="small muted">临时止水：未记录</span>}
            {wo.reassured ? <span className="small" style={{ marginLeft: 8 }}>✓ 已安抚住户</span> : <span className="small muted" style={{ marginLeft: 8 }}>住户安抚：未记录</span>}
            {wo.third_party_tested ? <span className="small" style={{ marginLeft: 8 }}>✓ 第三方检测：{wo.third_party_org}</span> : <span className="small muted" style={{ marginLeft: 8 }}>第三方检测：未安排</span>}
            {consent && <span className="small" style={{ marginLeft: 8 }}>✓ 同意书 {consent.document_no}（{String(consent.signed_at).slice(0, 10)}）</span>}
          </div>
          {wo.conclusion_note && (
            <div className="small muted-box" style={{ marginBottom: 8 }}>处理结论：{wo.conclusion_note}</div>
          )}

          <table>
            <thead><tr><th>证据类型</th><th>标题</th><th>详情/数据</th><th>记录时间</th><th>录入人</th></tr></thead>
            <tbody>
              {attachs.map((a) => (
                <tr key={a.id}>
                  <td className="small">{ATTACH_KINDS[a.kind] || a.kind}</td>
                  <td className="small">{a.title}</td>
                  <td className="small muted">{a.detail}{a.url ? `（${a.url}）` : ''}</td>
                  <td className="small">{String(a.recorded_at).slice(0, 10)}</td>
                  <td className="small">{a.created_by}</td>
                </tr>
              ))}
              {attachs.length === 0 && <tr><td colSpan={5} className="list-empty">暂无证据</td></tr>}
            </tbody>
          </table>

          {isManager && (
            <>
              <div className="form-row" style={{ marginTop: 8 }}>
                <label className="field"><span>证据类型</span>
                  <select value={attForm.kind} onChange={(e) => setAttForm({ ...attForm, kind: e.target.value })}>
                    {Object.entries(ATTACH_KINDS).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                  </select>
                </label>
                <label className="field" style={{ flex: 2 }}><span>证据标题 *</span><input value={attForm.title} onChange={(e) => setAttForm({ ...attForm, title: e.target.value })} /></label>
                <label className="field"><span>记录日期</span><input type="date" value={attForm.recorded_at} onChange={(e) => setAttForm({ ...attForm, recorded_at: e.target.value })} /></label>
              </div>
              <label className="field"><span>详情（验收结论/雨量 mm/检测说明）</span><input value={attForm.detail} onChange={(e) => setAttForm({ ...attForm, detail: e.target.value })} /></label>
              <div className="btn-row"><button className="sm primary" onClick={addAttach}>关联证据</button></div>

              <hr style={{ margin: '12px 0', border: 0, borderTop: '1px dashed var(--border)' }} />
              <label className="field"><span>处置记录</span><input value={actionNote} onChange={(e) => setActionNote(e.target.value)} placeholder="如：东侧支架临时做防水卷材覆盖止水；已上门安抚并告知检测安排" /></label>
              <div className="form-row">
                <input className="small" style={{ flex: 2 }} value={testOrg} onChange={(e) => setTestOrg(e.target.value)} placeholder="第三方检测机构（委托检测时填写）" />
              </div>
              <div className="btn-row">
                <button className="sm" onClick={() => doAction('temporary_stoppage')}>记录临时止水</button>
                <button className="sm" onClick={() => doAction('reassure')}>记录住户安抚</button>
                <button className="sm" onClick={() => doAction('third_party_test', { org: testOrg })}>委托第三方检测</button>
              </div>

              <hr style={{ margin: '12px 0', border: 0, borderTop: '1px dashed var(--border)' }} />
              <div className="small muted" style={{ marginBottom: 6 }}>责任一时无法确认时可保持“待确认”，先冻结收益；拿到第三方检测结论后再给出处理结论。</div>
              <div className="form-row">
                <label className="field" style={{ flex: 2 }}><span>处理结论 *</span><input value={conclusion.note} onChange={(e) => setConclusion({ ...conclusion, note: e.target.value })} placeholder="如：检测确认支架穿层密封失效，安装与原防水搭接均有缺陷" /></label>
                <label className="field"><span>核定维修费用</span><input type="number" value={conclusion.cost} onChange={(e) => setConclusion({ ...conclusion, cost: e.target.value })} /></label>
                <label className="field"><span>责任判定</span>
                  <select value={conclusion.responsibility} onChange={(e) => setConclusion({ ...conclusion, responsibility: e.target.value })}>
                    <option value="">维持待确认</option>
                    <option value="installer">施工方（安装商）</option>
                    <option value="property">物业/运维基金</option>
                    <option value="owner">业主</option>
                    <option value="insurance">保险</option>
                    <option value="shared">多方按比例分担</option>
                  </select>
                </label>
              </div>
              {conclusion.responsibility === 'shared' && (
                <div className="form-row">
                  {alloc.map((a, i) => (
                    <label key={i} className="field">
                      <span>{BEARERS.find((b) => b.v === a.bearer)?.l} %
                        <button className="sm" style={{ marginLeft: 6 }} onClick={() => setAlloc(alloc.filter((_, j) => j !== i))}>删</button>
                      </span>
                      <input type="number" value={a.ratio} onChange={(e) => setAlloc(alloc.map((x, j) => j === i ? { ...x, ratio: e.target.value } : x))} />
                    </label>
                  ))}
                  <button className="sm" onClick={() => setAlloc([...alloc, { bearer: 'public', ratio: 0 }])}>+ 分担方</button>
                  <span className={'small ' + (Math.abs(allocSum - 100) < 0.01 ? '' : 'muted')}>合计 {allocSum}%（须为 100%）</span>
                </div>
              )}
              <div className="btn-row"><button className="sm primary" onClick={conclude}>保存处理结论并生成巡检</button></div>
            </>
          )}
        </>
      )}
    </div>
  )
}

function FreezePanel({ isManager, leakWos, shareholders, freezes, act, reload, notify, building }) {
  const [form, setForm] = useState({ work_order_id: 0, period_month: '2026-10', estimated_cost: 0, note: '' })
  const [targets, setTargets] = useState({}) // {userId: ratio}
  const [releaseOf, setReleaseOf] = useState(0)
  const [ralloc, setRalloc] = useState([{ bearer: 'installer', ratio: 60 }, { bearer: 'fund', ratio: 40 }])
  const [rnote, setRnote] = useState('')

  const toggleTarget = (uid, on) => {
    const next = { ...targets }
    if (on) next[uid] = 100
    else delete next[uid]
    setTargets(next)
  }

  const create = async () => {
    if (!form.work_order_id) return notify('请选择漏水工单', true)
    const list = Object.entries(targets).map(([uid, ratio]) => ({ user_id: Number(uid), ratio: Number(ratio) || 0 }))
    if (list.length === 0) return notify('请勾选需要冻结收益的组件住户', true)
    if (list.some((t) => t.ratio <= 0 || t.ratio > 100)) return notify('冻结比例需在 0~100 之间', true)
    await act(`/buildings/${building.id}/freezes`, { ...form, targets: list }, '收益冻结单已创建，将在该月结算定稿时生效')
    setTargets({})
  }

  const release = async (fid) => {
    const sum = ralloc.reduce((s, a) => s + (Number(a.ratio) || 0), 0)
    if (Math.abs(sum - 100) >= 0.01) return notify('责任比例合计须为 100%', true)
    const r = await api('POST', `/freezes/${fid}/release`, {
      note: rnote, allocation: ralloc.map((a) => ({ bearer: a.bearer, ratio: Number(a.ratio) || 0 })),
    }).catch((e) => { notify(e.message, true); return null })
    if (r) {
      notify(`冻结收益已释放：施工方 ¥${fmtMoney(r.installer)} · 基金 ¥${fmtMoney(r.fund)} · 公共 ¥${fmtMoney(r.public)} · 业主自负 ¥${fmtMoney(r.owner)}`)
      setReleaseOf(0); setRnote('')
      reload()
    }
  }

  return (
    <>
      {isManager && (
        <div className="muted-box" style={{ marginBottom: 12 }}>
          <div className="form-row">
            <label className="field" style={{ flex: 2 }}><span>关联漏水工单</span>
              <select value={form.work_order_id} onChange={(e) => setForm({ ...form, work_order_id: Number(e.target.value) })}>
                <option value={0}>请选择…</option>
                {leakWos.map((w) => <option key={w.id} value={w.id}>{w.period_month} · {w.title}</option>)}
              </select>
            </label>
            <label className="field"><span>冻结收益月份</span><input value={form.period_month} onChange={(e) => setForm({ ...form, period_month: e.target.value })} placeholder="YYYY-MM" /></label>
            <label className="field"><span>预估维修费</span><input type="number" value={form.estimated_cost} onChange={(e) => setForm({ ...form, estimated_cost: Number(e.target.value) })} /></label>
          </div>
          <div className="small muted" style={{ margin: '4px 0' }}>勾选相关组件住户并设置冻结比例（对其该月结算收益冻结的百分比）：</div>
          <div className="form-row">
            {shareholders.map((s) => (
              <label key={s.id} className="field" style={{ minWidth: 180 }}>
                <span>
                  <input type="checkbox" checked={s.user_id in targets}
                    onChange={(e) => toggleTarget(s.user_id, e.target.checked)} /> {s.name}（{Number(s.ratio).toFixed(0)}%）
                </span>
                <input type="number" min="1" max="100" value={targets[s.user_id] ?? 0}
                  disabled={!(s.user_id in targets)}
                  onChange={(e) => setTargets({ ...targets, [s.user_id]: Number(e.target.value) })} />
              </label>
            ))}
            {shareholders.length === 0 && <span className="small muted">本楼暂无已缴费认购住户</span>}
          </div>
          <label className="field"><span>说明</span><input value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} placeholder="如：责任未明前冻结涉事支架相关组件收益" /></label>
          <div className="btn-row"><button className="sm primary" onClick={create}>创建冻结单</button></div>
        </div>
      )}

      <table>
        <thead><tr><th>月份</th><th>漏水工单</th><th>状态</th><th>冻结对象</th><th>操作/释放结果</th></tr></thead>
        <tbody>
          {freezes.map((f) => {
            const frozenSum = (f.targets || []).reduce((s, t) => s + Number(t.frozen_amount || 0), 0)
            return (
              <React.Fragment key={f.id}>
                <tr>
                  <td className="small">{f.period_month}</td>
                  <td className="small">{f.work_order_title}</td>
                  <td><Badge value={f.status === 'active' ? 'processing' : f.status === 'released' ? 'resolved' : f.status === 'cancelled' ? 'closed' : 'pending'} />
                    <span className="small" style={{ marginLeft: 4 }}>{FREEZE_STATUS[f.status]}</span></td>
                  <td className="small">
                    {(f.targets || []).map((t) => (
                      <div key={t.id}>{t.name}：{Number(t.ratio).toFixed(0)}%
                        {f.status === 'pending' ? '' : ` · 冻结 ¥${fmtMoney(t.frozen_amount)}`}
                      </div>
                    ))}
                    {f.status !== 'pending' && <div className="muted">合计冻结 ¥{fmtMoney(frozenSum)}</div>}
                  </td>
                  <td className="small">
                    {isManager && f.status === 'pending' && (
                      <button className="sm" onClick={() => act(`/freezes/${f.id}/cancel`, {}, '冻结单已取消')}>取消冻结</button>
                    )}
                    {isManager && f.status === 'active' && (
                      <button className="sm primary" onClick={() => setReleaseOf(releaseOf === f.id ? 0 : f.id)}>
                        {releaseOf === f.id ? '收起' : '按责任比例释放'}
                      </button>
                    )}
                    {f.status === 'released' && <span className="muted">{f.release_note || '已按责任比例释放'}</span>}
                  </td>
                </tr>
                {isManager && f.status === 'active' && releaseOf === f.id && (
                  <tr>
                    <td colSpan={5}>
                      <div className="muted-box">
                        <div className="small muted" style={{ marginBottom: 6 }}>
                          按责任比例拆分冻结额（合计须 100%）。施工方赔付/基金/公共承担的部分返还住户，业主自负部分从冻结收益扣减。
                          {f.responsibility && f.responsibility !== 'undetermined' && <> 工单当前责任：<b>{f.responsibility}</b>，可直接使用默认比例。</>}
                        </div>
                        <div className="form-row">
                          {ralloc.map((a, i) => (
                            <label key={i} className="field">
                              <span>{BEARERS.find((b) => b.v === a.bearer)?.l} %
                                <button className="sm" style={{ marginLeft: 6 }} onClick={() => setRalloc(ralloc.filter((_, j) => j !== i))}>删</button>
                              </span>
                              <input type="number" value={a.ratio} onChange={(e) => setRalloc(ralloc.map((x, j) => j === i ? { ...x, ratio: e.target.value } : x))} />
                            </label>
                          ))}
                          <button className="sm" onClick={() => setRalloc([...ralloc, { bearer: 'public', ratio: 0 }])}>+ 承担方</button>
                        </div>
                        <label className="field"><span>释放说明</span><input value={rnote} onChange={(e) => setRnote(e.target.value)} placeholder="如：第三方检测确认安装密封缺陷占主责，基金兜底" /></label>
                        <button className="sm primary" onClick={() => release(f.id)}>确认释放并写入责任结论</button>
                      </div>
                    </td>
                  </tr>
                )}
              </React.Fragment>
            )
          })}
        </tbody>
      </table>
      {freezes.length === 0 && <div className="list-empty">暂无收益冻结单</div>}
    </>
  )
}

function ConsentPanel({ isManager, members, consents, act, building }) {
  const [form, setForm] = useState({ user_id: 0, signed_at: '', document_no: '', note: '' })
  const residents = (members || []).filter((m) => m.member_type === 'resident')
  return (
    <>
      <table>
        <thead><tr><th>住户</th><th>房号</th><th>签署日期</th><th>协议编号</th><th>备注</th></tr></thead>
        <tbody>
          {consents.map((c) => (
            <tr key={c.id}>
              <td className="small">{c.name}</td><td className="small">{c.unit_no}</td>
              <td className="small">{String(c.signed_at).slice(0, 10)}</td>
              <td className="small">{c.document_no || '—'}</td><td className="small muted">{c.note}</td>
            </tr>
          ))}
          {consents.length === 0 && <tr><td colSpan={5} className="list-empty">暂无同意书记录</td></tr>}
        </tbody>
      </table>
      {isManager && (
        <div className="form-row" style={{ marginTop: 8 }}>
          <label className="field" style={{ flex: 2 }}><span>顶楼住户</span>
            <select value={form.user_id} onChange={(e) => setForm({ ...form, user_id: Number(e.target.value) })}>
              <option value={0}>请选择…</option>
              {residents.map((m) => <option key={m.id} value={m.id}>{m.name}（{m.unit_no}）</option>)}
            </select>
          </label>
          <label className="field"><span>签署日期</span><input type="date" value={form.signed_at} onChange={(e) => setForm({ ...form, signed_at: e.target.value })} /></label>
          <label className="field"><span>协议编号</span><input value={form.document_no} onChange={(e) => setForm({ ...form, document_no: e.target.value })} /></label>
          <label className="field" style={{ flex: 2 }}><span>备注</span><input value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} /></label>
          <button className="sm primary" onClick={async () => {
            if (!form.user_id) return
            await act(`/buildings/${building.id}/consents`, form, '同意书已登记')
            setForm({ user_id: 0, signed_at: '', document_no: '', note: '' })
          }}>登记同意书</button>
        </div>
      )}
    </>
  )
}

function InspectionPanel({ isManager, inspections, act, building }) {
  const [form, setForm] = useState({ title: '', plan_date: '', focus: '' })
  return (
    <>
      <table>
        <thead><tr><th>计划日期</th><th>巡检内容</th><th>关联漏水工单</th><th>重点</th><th>状态</th>{isManager && <th>操作</th>}</tr></thead>
        <tbody>
          {inspections.map((it) => (
            <tr key={it.id}>
              <td className="small">{String(it.plan_date).slice(0, 10)}</td>
              <td className="small">{it.title}</td>
              <td className="small muted">{it.work_order_title || '—'}</td>
              <td className="small muted">{it.focus}</td>
              <td><Badge value={it.status === 'done' ? 'resolved' : it.status === 'cancelled' ? 'closed' : 'pending'} />
                {it.result_note && <div className="small muted">{it.result_note}</div>}</td>
              {isManager && (
                <td>{it.status === 'scheduled' && (
                  <button className="sm" onClick={() => {
                    const note = window.prompt('巡检结果（如：穿层封堵完好，雨后无复漏）')
                    if (note) act(`/roof-inspections/${it.id}/complete`, { result_note: note }, '巡检已登记完成')
                  }}>登记结果</button>
                )}</td>
              )}
            </tr>
          ))}
          {inspections.length === 0 && <tr><td colSpan={6} className="list-empty">暂无屋顶巡检计划</td></tr>}
        </tbody>
      </table>
      {isManager && (
        <div className="form-row" style={{ marginTop: 8 }}>
          <label className="field" style={{ flex: 2 }}><span>巡检内容 *</span><input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} /></label>
          <label className="field"><span>计划日期 *</span><input type="date" value={form.plan_date} onChange={(e) => setForm({ ...form, plan_date: e.target.value })} /></label>
          <label className="field" style={{ flex: 2 }}><span>巡检重点</span><input value={form.focus} onChange={(e) => setForm({ ...form, focus: e.target.value })} /></label>
          <button className="sm primary" onClick={async () => {
            if (!form.title || !form.plan_date) return
            await act(`/buildings/${building.id}/roof-inspections`, form, '巡检计划已创建')
            setForm({ title: '', plan_date: '', focus: '' })
          }}>新增巡检</button>
        </div>
      )}
    </>
  )
}
