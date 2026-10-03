// 极简 API 封装：JWT 存 localStorage，错误统一抛出 Error(message)
const TOKEN_KEY = 'pv_token'

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || ''
}
export function setToken(t) {
  localStorage.setItem(TOKEN_KEY, t)
}
export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
}

export async function api(method, path, body) {
  const headers = { 'Content-Type': 'application/json' }
  const token = getToken()
  if (token) headers.Authorization = 'Bearer ' + token
  const resp = await fetch('/api' + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await resp.text()
  let data = {}
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = { raw: text }
    }
  }
  if (!resp.ok) {
    throw new Error(data.error || `请求失败 (${resp.status})`)
  }
  return data
}

export const fmtMoney = (v) =>
  Number(v || 0).toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
export const fmtNum = (v, d = 0) =>
  Number(v || 0).toLocaleString('zh-CN', { minimumFractionDigits: d, maximumFractionDigits: d })

export const STAGES = [
  { key: 'draft', label: '立项建档' },
  { key: 'voting', label: '楼栋表决' },
  { key: 'subscribing', label: '认购缴费' },
  { key: 'construction', label: '施工排期' },
  { key: 'grid_acceptance', label: '并网验收' },
  { key: 'operating', label: '发电运营' },
]
export const stageLabel = (k) => STAGES.find((s) => s.key === k)?.label || k

export const ROLE_LABEL = { admin: '管理员', property: '物业', resident: '住户' }
export const WO_RESP = {
  undetermined: '待判定',
  property: '物业',
  installer: '安装商',
  owner: '业主',
  insurance: '保险',
}
export const EVIDENCE_TYPES = {
  construction_photo: '施工照片',
  waterproof_acceptance: '屋顶防水验收',
  rainfall: '雨量记录',
  consent: '顶楼住户同意签署',
  inspection_report: '第三方检测报告',
}
export const ACTION_TYPES = {
  temporary_seal: '临时止水',
  resident_reassurance: '住户安抚',
  third_party_inspection: '第三方检测',
  repair: '正式维修',
  other: '其他处置',
}
export const FREEZE_STATUS = { frozen: '冻结中', released: '已释放' }
