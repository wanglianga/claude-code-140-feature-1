-- 顶楼漏水投诉处置与收益冻结：证据关联、同意书、临时止水/安抚/第三方检测、
-- 相关组件收益暂时冻结、责任确认后按责任比例释放（施工方赔付/运维基金/楼栋公共收益）、
-- 以及写入后续屋顶巡检计划。
--
-- 金额单位: 元; 比例单位: % (0~100), numeric 精确存储

-- ========== 1. work_orders 扩展 ==========
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS unit_no TEXT NOT NULL DEFAULT ''; -- 投诉房号（如 1-601）
-- 顶楼住户此前是否签署同意：signed=已签署, declined=明确拒绝/未签, unknown=待核查
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS resident_consent_status TEXT NOT NULL DEFAULT 'unknown'
    CHECK (resident_consent_status IN ('unknown','signed','declined'));
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS temporary_stoppage_at TIMESTAMPTZ; -- 临时止水完成时间
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS reassured_at TIMESTAMPTZ;         -- 住户安抚记录时间
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS third_party_test_at TIMESTAMPTZ;  -- 第三方检测委托/完成时间
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS third_party_test_note TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS third_party_test_org TEXT NOT NULL DEFAULT '';
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS conclusion_note TEXT NOT NULL DEFAULT ''; -- 平台处理结论
-- 安装商/施工方/保险责任：'shared' 表示经第三方检测后按比例分担
ALTER TABLE work_orders ADD COLUMN IF NOT EXISTS responsibility_allocation JSONB NOT NULL DEFAULT '[]'::jsonb;

-- 现有责任方枚举增加 shared（多方按比例分担）
ALTER TABLE work_orders DROP CONSTRAINT IF EXISTS work_orders_responsibility_check;
ALTER TABLE work_orders ADD CONSTRAINT work_orders_responsibility_check
    CHECK (responsibility IN ('undetermined','property','installer','owner','insurance','shared'));

-- 台账类别增加：冻结持有、冻结返还（原表无 CHECK，这里统一补齐全部实际使用类别）
ALTER TABLE ledger DROP CONSTRAINT IF EXISTS ledger_category_check;
ALTER TABLE ledger ADD CONSTRAINT ledger_category_check CHECK (category IN
    ('subscription','revenue','subsidy','public','maintenance_fund','repair','om','tax',
     'refund','compensation','transfer_in','freeze_hold','freeze_release'));

-- ========== 2. 结算明细：冻结额与释放后实发 ==========
ALTER TABLE settlement_items ADD COLUMN IF NOT EXISTS frozen_amount NUMERIC(12,2) NOT NULL DEFAULT 0;
ALTER TABLE settlement_items ADD COLUMN IF NOT EXISTS released_amount NUMERIC(12,2) NOT NULL DEFAULT 0;
ALTER TABLE settlement_periods ADD COLUMN IF NOT EXISTS frozen_total NUMERIC(12,2) NOT NULL DEFAULT 0;
ALTER TABLE settlement_periods ADD COLUMN IF NOT EXISTS payable_total NUMERIC(12,2) NOT NULL DEFAULT 0;

-- ========== 3. 顶楼住户屋顶使用同意书 ==========
CREATE TABLE IF NOT EXISTS rooftop_consents (
    id           BIGSERIAL PRIMARY KEY,
    building_id  BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    unit_no      TEXT NOT NULL DEFAULT '',
    signed_at    TIMESTAMPTZ NOT NULL, -- 签署日期
    document_no  TEXT NOT NULL DEFAULT '', -- 协议编号
    note         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (building_id, user_id)
);

-- ========== 4. 工单证据附件：施工照片、屋顶防水验收、雨量记录、同意书、第三方检测报告等 ==========
CREATE TABLE IF NOT EXISTS work_order_attachments (
    id          BIGSERIAL PRIMARY KEY,
    work_order_id BIGINT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    building_id BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN
        ('construction_photo','waterproof_acceptance','rainfall','consent','third_party_report','onsite_photo','other')),
    title       TEXT NOT NULL DEFAULT '',
    url         TEXT NOT NULL DEFAULT '',   -- 附件地址（演示环境为文字记录/链接描述）
    detail      TEXT NOT NULL DEFAULT '',   -- 文字详情，如防水验收结论、雨量数据
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(), -- 证据对应时间/记录时间
    created_by  BIGINT REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_woa_work_order ON work_order_attachments(work_order_id);

-- ========== 5. 收益冻结单 ==========
-- scope=component：冻结目标中列明的认购住户（按其当期结算金额 × target.ratio/100）
-- scope=public：冻结公共部分收益（用于"楼栋公共收益支付"）
CREATE TABLE IF NOT EXISTS revenue_freezes (
    id             BIGSERIAL PRIMARY KEY,
    building_id    BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    work_order_id  BIGINT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    period_month   TEXT NOT NULL CHECK (period_month ~ '^\d{4}-\d{2}$'),
    scope          TEXT NOT NULL DEFAULT 'component' CHECK (scope IN ('component','public')),
    freeze_ratio   NUMERIC(7,4) NOT NULL DEFAULT 100, -- 对目标当期收益的冻结比例 %
    estimated_cost NUMERIC(12,2) NOT NULL DEFAULT 0,  -- 预估维修/赔付金额
    status         TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','active','released','cancelled')),
    note           TEXT NOT NULL DEFAULT '',
    created_by     BIGINT REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at   TIMESTAMPTZ,  -- 结算定稿、holds 建立时
    released_at    TIMESTAMPTZ,
    release_note   TEXT NOT NULL DEFAULT '',
    UNIQUE (building_id, period_month, work_order_id)
);
CREATE INDEX IF NOT EXISTS idx_rf_work_order ON revenue_freezes(work_order_id);

-- 同一冻结单对住户/公共的目标与冻结结果
CREATE TABLE IF NOT EXISTS revenue_freeze_targets (
    id              BIGSERIAL PRIMARY KEY,
    freeze_id       BIGINT NOT NULL REFERENCES revenue_freezes(id) ON DELETE CASCADE,
    user_id         BIGINT REFERENCES users(id) ON DELETE CASCADE, -- NULL = 公共部分
    ratio           NUMERIC(7,4) NOT NULL DEFAULT 100,            -- 冻结该对象当期收益的百分比
    settled_amount  NUMERIC(12,2) NOT NULL DEFAULT 0,             -- 该对象当期结算金额（快照）
    frozen_amount   NUMERIC(12,2) NOT NULL DEFAULT 0,             -- 实际冻结金额
    released_amount NUMERIC(12,2) NOT NULL DEFAULT 0,             -- 已释放/已扣减金额
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_rft_freeze ON revenue_freeze_targets(freeze_id);

-- 冻结持有记录：结算定稿时生成；release 时回填释放信息
CREATE TABLE IF NOT EXISTS freeze_holds (
    id              BIGSERIAL PRIMARY KEY,
    building_id     BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    freeze_id       BIGINT NOT NULL REFERENCES revenue_freezes(id) ON DELETE CASCADE,
    target_id       BIGINT NOT NULL REFERENCES revenue_freeze_targets(id) ON DELETE CASCADE,
    period_id       BIGINT NOT NULL REFERENCES settlement_periods(id) ON DELETE CASCADE,
    item_id         BIGINT REFERENCES settlement_items(id) ON DELETE CASCADE,
    user_id         BIGINT REFERENCES users(id) ON DELETE CASCADE, -- NULL = 公共
    settlement_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    amount          NUMERIC(12,2) NOT NULL,                     -- 持有冻结额
    status          TEXT NOT NULL DEFAULT 'held' CHECK (status IN ('held','released','deducted','partially_released')),
    released_amount NUMERIC(12,2) NOT NULL DEFAULT 0,
    bearer          TEXT NOT NULL DEFAULT '' CHECK (bearer IN ('','installer','fund','public','owner')),
    note            TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_fh_building ON freeze_holds(building_id);
CREATE INDEX IF NOT EXISTS idx_fh_user ON freeze_holds(user_id);

-- ========== 6. 后续屋顶巡检计划 ==========
CREATE TABLE IF NOT EXISTS roof_inspections (
    id            BIGSERIAL PRIMARY KEY,
    building_id   BIGINT NOT NULL REFERENCES buildings(id) ON DELETE CASCADE,
    work_order_id BIGINT REFERENCES work_orders(id) ON DELETE SET NULL,
    title         TEXT NOT NULL,
    plan_date     DATE NOT NULL,
    focus         TEXT NOT NULL DEFAULT '',  -- 巡检重点：支架穿层、防水卷材搭接、雨水管口等
    status        TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN ('scheduled','done','cancelled')),
    result_note   TEXT NOT NULL DEFAULT '',
    created_by    BIGINT REFERENCES users(id),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_ri_building ON roof_inspections(building_id, plan_date);
