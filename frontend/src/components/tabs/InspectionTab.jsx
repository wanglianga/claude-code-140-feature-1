import React, { useEffect, useState } from 'react'
import { api } from '../../lib/api'
import { Badge, handleErr, useToast } from '../ui'

const STATUS_LABEL = { scheduled: '待巡检', done: '已完成', cancelled: '已取消' }

export default function InspectionTab({ user, building }) {
  const { notify } = useToast()
  const isManager = user.role === 'admin' || user.role === 'property'
  const [items, setItems] = useState([])
  const [form, setForm] = useState({
    scheduled_date: '2026-11-02', title: '', focus_area: '', conclusion: '', work_order_id: 0,
  })

  const load = async () => {
    const r = await api('GET', `/buildings/${building.id}/inspections`).catch(() => ({ items: [] }))
    setItems(r.items || [])
  }
  useEffect(() => { load() }, [building.id])

  const create = async () => {
    if (!form.scheduled_date) return notify('请选择计划日期', true)
    try {
      await handleErr(api('POST', `/buildings/${building.id}/inspections`, {
        scheduled_date: form.scheduled_date,
        title: form.title || '屋顶防水专项巡检',
        focus_area: form.focus_area,
        conclusion: form.conclusion,
        work_order_id: form.work_order_id || undefined,
      }), notify)
      notify('巡检计划已排期')
      setForm({ ...form, title: '', focus_area: '', conclusion: '' })
      load()
    } catch { /* toast */ }
  }

  const finish = async (it) => {
    const note = window.prompt('登记巡检结果（渗漏点复查情况）', it.result_note || '渗漏点未见复渗，防水收口完好')
    if (note === null) return
    try {
      await handleErr(api('POST', `/inspections/${it.id}`, { status: 'done', result_note: note }), notify)
      notify('巡检已完成')
      load()
    } catch { /* toast */ }
  }

  return (
    <>
      {isManager && (
        <div className="card">
          <h2>排期屋顶巡检</h2>
          <p className="muted small">
            漏水工单<b>责任确认并释放冻结收益时，处理结论会自动写入一项 30 天后的专项巡检</b>；也可在此手动排期。
          </p>
          <div className="form-row">
            <label className="field"><span>计划日期</span>
              <input type="date" value={form.scheduled_date} onChange={(e) => setForm({ ...form, scheduled_date: e.target.value })} />
            </label>
            <label className="field" style={{ flex: 2 }}><span>巡检主题</span>
              <input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} placeholder="屋顶防水专项巡检" />
            </label>
          </div>
          <label className="field"><span>重点部位</span>
            <input value={form.focus_area} onChange={(e) => setForm({ ...form, focus_area: e.target.value })} placeholder="支架穿层点 / 防水卷材收口 / 天沟 / 线管周边" />
          </label>
          <label className="field"><span>关联结论/要求</span>
            <textarea value={form.conclusion} onChange={(e) => setForm({ ...form, conclusion: e.target.value })} />
          </label>
          <button className="primary" onClick={create}>排期</button>
        </div>
      )}

      <div className="card">
        <h2>屋顶巡检计划</h2>
        <table>
          <thead>
            <tr><th>计划日期</th><th>主题 / 重点部位</th><th>工单结论</th><th>状态</th><th>巡检结果</th>{isManager && <th>操作</th>}</tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.id}>
                <td className="small">{it.scheduled_date}</td>
                <td>
                  <b>{it.title}</b>
                  {it.focus_area && <div className="small muted">{it.focus_area}</div>}
                  {it.work_order_id ? <div className="small">关联漏水工单 #{it.work_order_id}</div> : null}
                </td>
                <td className="small">{it.conclusion || '—'}</td>
                <td><Badge value={it.status === 'scheduled' ? 'processing' : it.status === 'done' ? 'resolved' : 'closed'} /><span style={{ marginLeft: 4 }} className="small">{STATUS_LABEL[it.status]}</span></td>
                <td className="small">{it.result_note || '—'}</td>
                {isManager && (
                  <td>
                    {it.status === 'scheduled' && <button className="sm primary" onClick={() => finish(it)}>登记完成</button>}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
        {items.length === 0 && <div className="list-empty">暂无巡检计划</div>}
      </div>
    </>
  )
}
