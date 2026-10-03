import React, { useEffect, useState } from 'react'
import { api, fmtMoney, WO_RESP, FREEZE_STATUS } from '../../lib/api'
import { Badge, handleErr, useToast } from '../ui'
import LeakCasePanel from './LeakCasePanel'

const WO_TYPES = { leak: '屋顶漏水', inverter: '逆变器故障', channel: '维护通道', other: '其他' }
const RESP_OPTS = [
  { v: 'undetermined', l: '待判定' },
  { v: 'property', l: '物业' },
  { v: 'installer', l: '安装商' },
  { v: 'owner', l: '业主' },
  { v: 'insurance', l: '保险' },
]

export default function WorkOrderTab({ user, building }) {
  const { notify } = useToast()
  const isManager = user.role === 'admin' || user.role === 'property'
  const [wos, setWos] = useState([])
  const [inverters, setInverters] = useState([])
  const [openLeakId, setOpenLeakId] = useState(null)
  const [form, setForm] = useState({
    period_month: '2026-10', title: '', description: '', wo_type: 'leak',
    inverter_id: 0, cost: 0, assignee: '',
  })

  const load = async () => {
    const [w, iv] = await Promise.all([
      api('GET', `/buildings/${building.id}/work-orders`),
      api('GET', `/buildings/${building.id}/inverters`).catch(() => ({ items: [] })),
    ])
    setWos(w.items || [])
    setInverters(iv.items || [])
  }
  useEffect(() => { load() }, [building.id])

  const create = async () => {
    if (!form.title) return notify('请填写工单标题', true)
    try {
      const r = await handleErr(api('POST', `/buildings/${building.id}/work-orders`, form), notify)
      notify('维修工单已创建')
      setForm({ ...form, title: '', description: '' })
      await load()
      if (form.wo_type === 'leak') setOpenLeakId(r.id)
    } catch { /* toast */ }
  }

  const update = async (wo, patch) => {
    try {
      await handleErr(api('POST', `/work-orders/${wo.id}`, patch), notify)
      notify('工单已更新')
      load()
    } catch { /* toast */ }
  }

  if (openLeakId) {
    return <LeakCasePanel user={user} building={building} woId={openLeakId} onClose={() => { setOpenLeakId(null); load() }} />
  }

  return (
    <>
      <div className="card">
        <h2>报修 / 维修工单</h2>
        <p className="muted small">
          顶楼漏水、逆变器故障、维护通道占用等均可报单。<b>顶楼漏水</b>可建立专项档案：关联施工照片、屋顶防水验收、
          雨量记录与住户此前是否签署同意；责任无法确认时安排临时止水、住户安抚、第三方检测，
          并可在维修期间<b>暂时冻结相关组件收益</b>，责任确认后按施工方赔付 / 运维基金 / 楼栋公共收益 / 业主自担比例释放。
        </p>
        <div className="form-row">
          <label className="field">
            <span>归属结算月份</span>
            <input value={form.period_month} onChange={(e) => setForm({ ...form, period_month: e.target.value })} placeholder="YYYY-MM" />
          </label>
          <label className="field">
            <span>类型</span>
            <select value={form.wo_type} onChange={(e) => setForm({ ...form, wo_type: e.target.value })}>
              {Object.entries(WO_TYPES).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </select>
          </label>
          <label className="field">
            <span>关联逆变器</span>
            <select value={form.inverter_id} onChange={(e) => setForm({ ...form, inverter_id: Number(e.target.value) })}>
              <option value={0}>不关联（如漏水）</option>
              {inverters.map((iv) => <option key={iv.id} value={iv.id}>{iv.name}</option>)}
            </select>
          </label>
        </div>
        <div className="form-row">
          <label className="field" style={{ flex: 2 }}><span>标题 *</span><input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} /></label>
          <label className="field"><span>预估费用</span><input type="number" value={form.cost} onChange={(e) => setForm({ ...form, cost: Number(e.target.value) })} /></label>
          <label className="field"><span>指派处理人</span><input value={form.assignee} onChange={(e) => setForm({ ...form, assignee: e.target.value })} /></label>
          <button className="primary" onClick={create}>提交工单</button>
        </div>
        <label className="field"><span>问题描述（渗漏位置、雨后现象等）</span><textarea value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} /></label>
      </div>

      <div className="card">
        <h2>工单列表与责任判定</h2>
        <table>
          <thead>
            <tr>
              <th>月份</th><th>类型</th><th>问题</th><th>设备</th><th className="num">费用</th>
              <th>责任方</th><th>处理人</th><th>状态</th><th>收益冻结</th>{isManager && <th>操作</th>}
            </tr>
          </thead>
          <tbody>
            {wos.map((wo) => (
              <tr key={wo.id}>
                <td className="small">{wo.period_month}</td>
                <td>{WO_TYPES[wo.wo_type] || wo.wo_type}</td>
                <td>
                  <b>{wo.title}</b>
                  {wo.description && <div className="small muted">{wo.description}</div>}
                  <div className="small muted">报修人：{wo.reporter}</div>
                  {wo.wo_type === 'leak' && (
                    <button className="sm" style={{ marginTop: 4 }} onClick={() => setOpenLeakId(wo.id)}>📁 漏水投诉档案</button>
                  )}
                </td>
                <td className="small">{wo.inverter_name || '—'}</td>
                <td className="num">¥{fmtMoney(wo.cost)}</td>
                <td><Badge value={wo.responsibility === 'undetermined' ? 'pending' : wo.status} /><span style={{ marginLeft: 4 }}>{WO_RESP[wo.responsibility]}</span></td>
                <td className="small">{wo.assignee || '—'}</td>
                <td><Badge value={wo.status} /></td>
                <td>
                  {wo.freeze_status === 'frozen' && <span className="badge amber">{FREEZE_STATUS.frozen}</span>}
                  {wo.freeze_status === 'released' && <span className="badge green">{FREEZE_STATUS.released}</span>}
                  {!wo.freeze_status && <span className="small muted">—</span>}
                </td>
                {isManager && (
                  <td>
                    <div className="btn-row">
                      {wo.status === 'open' && (
                        <button className="sm" onClick={() => update(wo, { status: 'processing' })}>受理</button>
                      )}
                      {wo.status !== 'resolved' && (
                        <>
                          <select
                            className="sm"
                            style={{ width: 90, padding: '3px 4px' }}
                            value=""
                            onChange={(e) => update(wo, { responsibility: e.target.value, cost: wo.cost || form.cost })}
                          >
                            <option value="">判定责任…</option>
                            {RESP_OPTS.map((o) => <option key={o.v} value={o.v}>{o.l}</option>)}
                          </select>
                          <button className="sm primary" onClick={() => {
                            const cost = Number(window.prompt('核定费用（元）', String(wo.cost || 0)))
                            if (!Number.isNaN(cost)) update(wo, { status: 'resolved', cost })
                          }}>完工结费</button>
                        </>
                      )}
                    </div>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
        {wos.length === 0 && <div className="list-empty">暂无维修工单</div>}
      </div>
    </>
  )
}
